package canaryutils

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPostgresRequiresVerifiedTLS(t *testing.T) {
	for _, mode := range []string{"disable", "prefer", "require", "verify-ca", "verify-full"} {
		config, err := verifiedPostgresConfig("postgres://test:fake-db-secret@localhost:5432/test?sslmode=" + mode)
		if err != nil {
			t.Fatal(err)
		}
		if config.TLSConfig == nil || config.TLSConfig.InsecureSkipVerify || config.TLSConfig.ServerName != "localhost" || config.TLSConfig.MinVersion < tls.VersionTLS12 || len(config.Fallbacks) != 0 {
			t.Fatalf("sslmode=%s allowed unverified TLS or plaintext fallback", mode)
		}
	}
}

func TestCheckPostgresDeadlineAndSafeErrors(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		connection, err := listener.Accept()
		if err == nil {
			defer connection.Close()
			<-done // Simulate a database that never completes the TLS handshake.
		}
	}()
	uri := "postgres://test:fake-db-secret@" + listener.Addr().String() + "/test?sslmode=verify-full"
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	err = CheckPostgres(ctx, uri, "fake-marker-secret")
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatalf("database probe ignored its caller deadline: %v", err)
	}
	for _, secret := range []string{uri, "fake-db-secret", "fake-marker-secret"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatal("database diagnostic exposed connection credentials or marker")
		}
	}
}

// Set this only to a disposable local PostgreSQL server with a trusted TLS cert.
// Normal unit tests never start containers or contact a configured production DB.
func TestPostgresCheckpointLocalDatabase(t *testing.T) {
	uri := os.Getenv("NISTRO_CANARY_TEST_POSTGRES_URL")
	if uri == "" {
		t.Skip("set NISTRO_CANARY_TEST_POSTGRES_URL for a disposable local TLS database")
	}
	parsed, err := url.Parse(uri)
	if err != nil || (parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1") {
		t.Fatal("the database probe test requires a loopback-only PostgreSQL URL")
	}
	config, err := verifiedPostgresConfig(uri)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := pgx.ConnectConfig(t.Context(), config)
	if err != nil {
		t.Fatal(postgresError("test connect", err))
	}
	defer closePostgres(connection)
	if _, err := connection.Exec(t.Context(), "CREATE TABLE canary_test_sentinel (value text NOT NULL)"); err != nil {
		t.Fatal(postgresError("test sentinel", err))
	}
	defer connection.Exec(context.Background(), "DROP TABLE IF EXISTS canary_test_sentinel")
	if _, err := connection.Exec(t.Context(), "INSERT INTO canary_test_sentinel VALUES ('outside the probe')"); err != nil {
		t.Fatal(postgresError("test sentinel insert", err))
	}
	marker := "quote ' ; DROP TABLE canary_test_sentinel; -- and Unicode ✓"
	checkpoint, err := NewPostgresCheckpoint(t.Context(), uri, marker)
	if err != nil {
		t.Fatal(err)
	}
	defer checkpoint.Cleanup(context.Background(), uri)
	if err := checkpoint.Verify(t.Context(), uri); err != nil {
		t.Fatal(err)
	}
	// A fresh credential string must verify the existing row without recreating it.
	refreshed := *parsed
	query := refreshed.Query()
	query.Set("application_name", "post-update-check")
	refreshed.RawQuery = query.Encode()
	if err := checkpoint.Verify(t.Context(), refreshed.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(t.Context(), "UPDATE "+checkpoint.table+" SET marker=$1", "tampered-marker"); err != nil {
		t.Fatal(postgresError("test changed marker", err))
	}
	if err := checkpoint.Verify(t.Context(), uri); err == nil || !strings.Contains(err.Error(), "did not persist") {
		t.Fatal("checkpoint verification accepted a changed marker")
	}
	if _, err := connection.Exec(t.Context(), "UPDATE "+checkpoint.table+" SET marker=$1", marker); err != nil {
		t.Fatal(postgresError("test restore marker", err))
	}
	var sentinel string
	if err := connection.QueryRow(t.Context(), "SELECT value FROM canary_test_sentinel").Scan(&sentinel); err != nil || sentinel != "outside the probe" {
		t.Fatal("marker interpolation modified unrelated data")
	}
	if err := checkpoint.Cleanup(t.Context(), uri); err != nil {
		t.Fatal(err)
	}
	if err := checkpoint.Cleanup(t.Context(), uri); err != nil {
		t.Fatal("checkpoint cleanup was not idempotent")
	}
	if err := checkpoint.Verify(t.Context(), uri); err == nil || !strings.Contains(err.Error(), "42P01") {
		t.Fatal("cleanup did not remove the checkpoint table")
	}
	if err := CheckPostgres(t.Context(), uri, "convenience-marker"); err != nil {
		t.Fatal(err)
	}
	var leftovers int
	if err := connection.QueryRow(t.Context(), "SELECT count(*) FROM pg_tables WHERE schemaname=current_schema() AND tablename LIKE 'nistro\\_canary\\_%'").Scan(&leftovers); err != nil || leftovers != 0 {
		t.Fatal("database probe left canary tables behind")
	}
	t.Run("configured parameters are observed", func(t *testing.T) {
		database := pgx.Identifier{config.Database}.Sanitize()
		defer connection.Exec(context.Background(), "ALTER DATABASE "+database+" RESET work_mem")
		defer connection.Exec(context.Background(), "ALTER DATABASE "+database+" RESET statement_timeout")
		defer connection.Exec(context.Background(), "ALTER DATABASE "+database+" RESET synchronous_commit")
		for _, statement := range []string{"ALTER DATABASE " + database + " SET work_mem='32MB'", "ALTER DATABASE " + database + " SET statement_timeout='1s'", "ALTER DATABASE " + database + " SET synchronous_commit='true'"} {
			if _, err := connection.Exec(t.Context(), statement); err != nil {
				t.Fatal(postgresError("test settings setup", err))
			}
		}
		var blocks string
		if err := connection.QueryRow(t.Context(), "SELECT setting FROM pg_settings WHERE name='shared_buffers'").Scan(&blocks); err != nil {
			t.Fatal(postgresError("test block setting", err))
		}
		settings := map[string]string{"work_mem": "32768kB", "statement_timeout": "1000ms", "shared_buffers": blocks, "enable_seqscan": "true", "synchronous_commit": "true"}
		if err := CheckPostgresSettings(t.Context(), uri, settings); err != nil {
			t.Fatal(err)
		}
		if err := CheckPostgresSettings(t.Context(), uri, map[string]string{"work_mem": "16MB"}); err == nil {
			t.Fatal("parameter verification accepted a different server setting")
		}
		if err := CheckPostgresSettings(t.Context(), uri, map[string]string{"work_mem');SELECT 'injection-secret'--": "1"}); err == nil || strings.Contains(err.Error(), "injection-secret") {
			t.Fatal("parameter name was not safely bound or diagnostic exposed input")
		}
	})
	t.Run("untrusted TLS is rejected", func(t *testing.T) {
		untrusted := *parsed
		query := untrusted.Query()
		query.Del("sslrootcert")
		query.Set("sslmode", "require")
		untrusted.RawQuery = query.Encode()
		err := CheckPostgres(t.Context(), untrusted.String(), "must-not-be-written")
		if err == nil || !strings.Contains(err.Error(), "connect") || strings.Contains(err.Error(), untrusted.String()) {
			t.Fatalf("untrusted certificate was not rejected safely: %v", err)
		}
	})
	t.Run("failed insert cleans its table", func(t *testing.T) {
		// A disposable-database event trigger injects a real INSERT failure only
		// into canary tables, after successful table creation has committed.
		setup := []string{
			`CREATE FUNCTION canary_test_reject_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'provider-secret-fake-db-secret'; END $$`,
			`CREATE FUNCTION canary_test_attach_reject() RETURNS event_trigger LANGUAGE plpgsql AS $$ DECLARE command record; BEGIN FOR command IN SELECT * FROM pg_event_trigger_ddl_commands() LOOP IF command.object_type='table' AND command.object_identity LIKE '%nistro_canary_%' THEN EXECUTE format('CREATE TRIGGER canary_test_reject BEFORE INSERT ON %s FOR EACH ROW EXECUTE FUNCTION canary_test_reject_insert()', command.object_identity); END IF; END LOOP; END $$`,
			`CREATE EVENT TRIGGER canary_test_event ON ddl_command_end WHEN TAG IN ('CREATE TABLE') EXECUTE FUNCTION canary_test_attach_reject()`,
		}
		defer connection.Exec(context.Background(), "DROP FUNCTION IF EXISTS canary_test_reject_insert()")
		defer connection.Exec(context.Background(), "DROP FUNCTION IF EXISTS canary_test_attach_reject()")
		defer connection.Exec(context.Background(), "DROP EVENT TRIGGER IF EXISTS canary_test_event")
		for _, statement := range setup {
			if _, err := connection.Exec(t.Context(), statement); err != nil {
				t.Fatal(postgresError("test failure setup", err))
			}
		}
		checkpoint, err := NewPostgresCheckpoint(t.Context(), uri, "rejected-marker")
		if checkpoint != nil || err == nil || !strings.Contains(err.Error(), "insert marker") || !strings.Contains(err.Error(), "P0001") || strings.Contains(err.Error(), "provider-secret") {
			t.Fatalf("failed insert did not return a credential-free SQL error: %v", err)
		}
		if err := connection.QueryRow(t.Context(), "SELECT count(*) FROM pg_tables WHERE schemaname=current_schema() AND tablename LIKE 'nistro\\_canary\\_%'").Scan(&leftovers); err != nil || leftovers != 0 {
			t.Fatal("failed checkpoint creation left its table behind")
		}
	})
}
