// Package canaryutils probes deployment data planes with bounded requests and
// credential-free diagnostics. The lifecycle suite owns readiness retries.
package canaryutils

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// PostgresCheckpoint retains one committed marker across deployment updates.
// Each operation connects independently, allowing the caller to supply refreshed
// endpoint credentials after a rollout. No connection credentials are retained.
type PostgresCheckpoint struct {
	table  string
	marker string
}

// NewPostgresCheckpoint creates and reads a uniquely named canary table. Failed
// attempts run bounded cleanup even when the main context has been canceled.
// The caller must Cleanup a successful checkpoint after Verify checks.
func NewPostgresCheckpoint(ctx context.Context, connectionString, marker string) (checkpoint *PostgresCheckpoint, result error) {
	if strings.TrimSpace(connectionString) == "" || marker == "" || len(marker) > 4096 {
		return nil, errors.New("Postgres canary requires a connection and a nonempty marker of at most 4096 bytes")
	}
	config, err := verifiedPostgresConfig(connectionString)
	if err != nil {
		return nil, err
	}
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, errors.New("Postgres canary could not generate a table name")
	}
	pending := &PostgresCheckpoint{table: pgx.Identifier{"nistro_canary_" + hex.EncodeToString(random[:])}.Sanitize(), marker: marker}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	connection, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return nil, postgresError("connect", err)
	}
	defer closePostgres(connection)
	defer func() {
		if checkpoint == nil {
			result = errors.Join(result, pending.Cleanup(context.WithoutCancel(ctx), connectionString))
		}
	}()
	if _, err := connection.Exec(ctx, "CREATE TABLE "+pending.table+" (marker text NOT NULL)"); err != nil {
		return nil, postgresError("create table", err)
	}
	if _, err := connection.Exec(ctx, "INSERT INTO "+pending.table+" (marker) VALUES ($1)", marker); err != nil {
		return nil, postgresError("insert marker", err)
	}
	var observed string
	if err := connection.QueryRow(ctx, "SELECT marker FROM "+pending.table).Scan(&observed); err != nil {
		return nil, postgresError("read marker", err)
	}
	if observed != marker {
		return nil, errors.New("Postgres canary read a different marker")
	}
	return pending, nil
}

// Verify reconnects and checks the same marker, without recreating its table.
func (checkpoint *PostgresCheckpoint) Verify(ctx context.Context, connectionString string) error {
	if checkpoint == nil || checkpoint.table == "" {
		return errors.New("Postgres canary checkpoint is missing")
	}
	config, err := verifiedPostgresConfig(connectionString)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	connection, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return postgresError("reconnect", err)
	}
	defer closePostgres(connection)
	var observed string
	if err := connection.QueryRow(ctx, "SELECT marker FROM "+checkpoint.table).Scan(&observed); err != nil {
		return postgresError("read persisted marker", err)
	}
	if observed != checkpoint.marker {
		return errors.New("Postgres canary marker did not persist across connections")
	}
	return nil
}

// Cleanup drops only this checkpoint's table. Repeating cleanup is safe.
func (checkpoint *PostgresCheckpoint) Cleanup(ctx context.Context, connectionString string) error {
	if checkpoint == nil || checkpoint.table == "" {
		return errors.New("Postgres canary checkpoint is missing")
	}
	config, err := verifiedPostgresConfig(connectionString)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	connection, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return postgresError("cleanup connect", err)
	}
	defer closePostgres(connection)
	if _, err := connection.Exec(ctx, "DROP TABLE IF EXISTS "+checkpoint.table); err != nil {
		return postgresError("cleanup table", err)
	}
	return nil
}

// CheckPostgres is the write/read/reconnect convenience probe. TLS always
// verifies the certificate and hostname, preserving sslrootcert configuration.
// The table is dropped through an independent bounded cleanup connection.
func CheckPostgres(ctx context.Context, connectionString, marker string) (result error) {
	checkpoint, err := NewPostgresCheckpoint(ctx, connectionString, marker)
	if err != nil {
		return err
	}
	defer func() {
		result = errors.Join(result, checkpoint.Cleanup(context.WithoutCancel(ctx), connectionString))
	}()
	return checkpoint.Verify(ctx, connectionString)
}

func verifiedPostgresConfig(connectionString string) (*pgx.ConnConfig, error) {
	config, err := pgx.ParseConfig(connectionString)
	if err != nil || config.Host == "" || strings.HasPrefix(config.Host, "/") {
		return nil, errors.New("Postgres canary has an invalid TCP connection configuration")
	}
	if config.TLSConfig == nil {
		config.TLSConfig = &tls.Config{}
	} else {
		config.TLSConfig = config.TLSConfig.Clone()
	}
	config.TLSConfig.MinVersion = tls.VersionTLS12
	config.TLSConfig.InsecureSkipVerify = false
	config.TLSConfig.ServerName = config.Host
	config.Fallbacks = nil
	config.ConnectTimeout = 20 * time.Second
	return config, nil
}

func closePostgres(connection *pgx.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = connection.Close(ctx)
}

func postgresError(stage string, err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("Postgres canary %s: %w", stage, context.DeadlineExceeded)
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("Postgres canary %s: %w", stage, context.Canceled)
	}
	var authority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	if errors.As(err, &authority) || errors.As(err, &hostname) || errors.As(err, &invalid) {
		return fmt.Errorf("Postgres canary %s failed: TLS certificate verification failed", stage)
	}
	var postgres *pgconn.PgError
	if errors.As(err, &postgres) && len(postgres.Code) == 5 {
		return fmt.Errorf("Postgres canary %s failed (SQLSTATE %s)", stage, postgres.Code)
	}
	return fmt.Errorf("Postgres canary %s failed", stage)
}
