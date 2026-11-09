package canaryutils

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
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
	t.Run("database search_path update preserves checkpoint", func(t *testing.T) {
		var initialPath string
		if err := connection.QueryRow(t.Context(), "SELECT current_setting('search_path')").Scan(&initialPath); err != nil {
			t.Fatal(postgresError("test initial search path", err))
		}
		initialURI := postgresFixtureSearchPath(uri, initialPath)
		checkpoint, err := NewPostgresCheckpoint(t.Context(), initialURI, "search-path-marker")
		if err != nil {
			t.Fatal(err)
		}
		defer checkpoint.Cleanup(context.Background(), initialURI)
		database := pgx.Identifier{config.Database}.Sanitize()
		defer connection.Exec(context.Background(), "ALTER DATABASE "+database+" SET search_path FROM CURRENT")
		if _, err := connection.Exec(t.Context(), "ALTER DATABASE "+database+" SET search_path TO pg_catalog"); err != nil {
			t.Fatal(postgresError("test search path update", err))
		}
		// Force a fresh connection to use the changed server configuration,
		// regardless of an initial search_path query parameter in the fixture URL.
		updated, _ := url.Parse(uri)
		query := updated.Query()
		query.Del("search_path")
		updated.RawQuery = query.Encode()
		settings, err := ReadPostgresSettings(t.Context(), updated.String(), []string{"search_path"})
		if err != nil || settings["search_path"].Value != "pg_catalog" {
			t.Fatal("fresh connection did not observe the updated search_path")
		}
		if err := checkpoint.Verify(t.Context(), updated.String()); err != nil {
			t.Fatalf("checkpoint verification followed the changed search_path: %v", err)
		}
		if err := checkpoint.Cleanup(t.Context(), updated.String()); err != nil {
			t.Fatal(err)
		}
		if err := checkpoint.Verify(t.Context(), initialURI); err == nil || !strings.Contains(err.Error(), "42P01") {
			t.Fatal("changed-search-path cleanup did not remove the original checkpoint")
		}
	})
	t.Run("initial quoted schema and shadow table preserve identity", func(t *testing.T) {
		var random [8]byte
		if _, err := rand.Read(random[:]); err != nil {
			t.Fatal("generate disposable test schema names")
		}
		marker := hex.EncodeToString(random[:])
		initialSchema := "canary initial \"schema " + marker
		shadowSchema := "canary_shadow_" + marker
		for _, schema := range []string{initialSchema, shadowSchema} {
			identifier := pgx.Identifier{schema}.Sanitize()
			if _, err := connection.Exec(t.Context(), "CREATE SCHEMA "+identifier); err != nil {
				t.Fatal(postgresError("test schema creation", err))
			}
			defer connection.Exec(context.Background(), "DROP SCHEMA "+identifier+" CASCADE")
		}
		initialURI := postgresFixtureSearchPath(uri, pgx.Identifier{initialSchema}.Sanitize())
		checkpoint, err := NewPostgresCheckpoint(t.Context(), initialURI, "initial-schema-marker")
		if err != nil {
			t.Fatal(err)
		}
		defer checkpoint.Cleanup(context.Background(), initialURI)
		initialConfig, err := verifiedPostgresConfig(initialURI)
		if err != nil {
			t.Fatal(err)
		}
		initialConnection, err := pgx.ConnectConfig(t.Context(), initialConfig)
		if err != nil {
			t.Fatal(postgresError("test initial schema connect", err))
		}
		defer closePostgres(initialConnection)
		var table, schema string
		if err := initialConnection.QueryRow(t.Context(), `SELECT c.relname, n.nspname FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE c.oid=to_regclass($1)`, checkpoint.table).Scan(&table, &schema); err != nil || schema != initialSchema {
			t.Fatal("checkpoint was not created in its initial intended schema")
		}
		shadow := pgx.Identifier{shadowSchema, table}.Sanitize()
		if _, err := connection.Exec(t.Context(), "CREATE TABLE "+shadow+" (marker text NOT NULL)"); err != nil {
			t.Fatal(postgresError("test shadow creation", err))
		}
		if _, err := connection.Exec(t.Context(), "INSERT INTO "+shadow+" VALUES ($1)", "unrelated-shadow-marker"); err != nil {
			t.Fatal(postgresError("test shadow insert", err))
		}
		changedURI := postgresFixtureSearchPath(uri, pgx.Identifier{shadowSchema}.Sanitize())
		if err := checkpoint.Verify(t.Context(), changedURI); err != nil {
			t.Fatalf("checkpoint verification followed an unrelated shadow table: %v", err)
		}
		if err := checkpoint.Cleanup(t.Context(), changedURI); err != nil {
			t.Fatal(err)
		}
		var shadowMarker string
		if err := connection.QueryRow(t.Context(), "SELECT marker FROM "+shadow).Scan(&shadowMarker); err != nil || shadowMarker != "unrelated-shadow-marker" {
			t.Fatal("checkpoint cleanup modified the unrelated shadow table")
		}
		if err := checkpoint.Verify(t.Context(), initialURI); err == nil || !strings.Contains(err.Error(), "42P01") {
			t.Fatal("cleanup did not remove the checkpoint from its initial schema")
		}
	})
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
		settings, err := ReadPostgresSettings(t.Context(), untrusted.String(), []string{"work_mem"})
		if settings != nil || err == nil || strings.Contains(err.Error(), untrusted.String()) {
			t.Fatal("settings snapshot accepted untrusted TLS or exposed connection credentials")
		}
	})
	t.Run("settings snapshots verify replacement resets", func(t *testing.T) {
		names := []string{"work_mem", "maintenance_work_mem", "work_mem"}
		baseline, err := ReadPostgresSettings(t.Context(), uri, names)
		if err != nil {
			t.Fatal(err)
		}
		if len(baseline) != 2 || names[0] != "work_mem" || names[1] != "maintenance_work_mem" || names[2] != "work_mem" {
			t.Fatal("settings snapshot failed to deduplicate names or mutated its input")
		}
		for name, setting := range baseline {
			var expected PostgresSetting
			if err := connection.QueryRow(t.Context(), `SELECT current_setting($1), unit, vartype FROM pg_settings WHERE name=$1`, name).Scan(&expected.Value, &expected.Unit, &expected.Kind); err != nil {
				t.Fatal(postgresError("test baseline read", err))
			}
			if setting != expected || setting.Value == "" || setting.Unit != "kB" || setting.Kind != "integer" {
				t.Fatal("settings snapshot did not capture the effective value and metadata")
			}
		}
		database := pgx.Identifier{config.Database}.Sanitize()
		custom := map[string]string{"work_mem": "32MB", "maintenance_work_mem": "128MB"}
		if PostgresSettingMatches(baseline["work_mem"], custom["work_mem"]) {
			custom["work_mem"] = "64MB"
		}
		if PostgresSettingMatches(baseline["maintenance_work_mem"], custom["maintenance_work_mem"]) {
			custom["maintenance_work_mem"] = "256MB"
		}
		for name, value := range custom {
			reset := "ALTER DATABASE " + database + " RESET " + pgx.Identifier{name}.Sanitize()
			defer connection.Exec(context.Background(), reset)
			if _, err := connection.Exec(t.Context(), "ALTER DATABASE "+database+" SET "+pgx.Identifier{name}.Sanitize()+"='"+value+"'"); err != nil {
				t.Fatal(postgresError("test custom settings setup", err))
			}
		}
		customized, err := ReadPostgresSettings(t.Context(), uri, names)
		if err != nil {
			t.Fatal(err)
		}
		for name, value := range custom {
			if !PostgresSettingMatches(customized[name], value) || PostgresSettingMatches(customized[name], baseline[name].Value) {
				t.Fatal("initial customization was not observed independently of the baseline")
			}
		}
		// An ignored subset replacement still satisfies the retained key. Reading
		// the removed key is necessary to reject the stale database configuration.
		if err := CheckPostgresSettings(t.Context(), uri, map[string]string{"work_mem": custom["work_mem"]}); err != nil {
			t.Fatal(err)
		}
		if PostgresSettingMatches(customized["maintenance_work_mem"], baseline["maintenance_work_mem"].Value) {
			t.Fatal("baseline comparison accepted an ignored parameter removal")
		}
		if _, err := connection.Exec(t.Context(), "ALTER DATABASE "+database+" RESET maintenance_work_mem"); err != nil {
			t.Fatal(postgresError("test subset replacement", err))
		}
		subset, err := ReadPostgresSettings(t.Context(), uri, names)
		if err != nil {
			t.Fatal(err)
		}
		if !PostgresSettingMatches(subset["work_mem"], custom["work_mem"]) || !PostgresSettingMatches(subset["maintenance_work_mem"], baseline["maintenance_work_mem"].Value) {
			t.Fatal("subset replacement did not retain one value and restore the removed baseline")
		}
		if _, err := connection.Exec(t.Context(), "ALTER DATABASE "+database+" RESET work_mem"); err != nil {
			t.Fatal(postgresError("test empty replacement", err))
		}
		empty, err := ReadPostgresSettings(t.Context(), uri, names)
		if err != nil {
			t.Fatal(err)
		}
		for name, setting := range baseline {
			if !PostgresSettingMatches(empty[name], setting.Value) {
				t.Fatal("empty replacement did not restore all captured baseline values")
			}
		}
		invalidName := "work_mem');SELECT 'snapshot-injection-secret'--"
		partial, err := ReadPostgresSettings(t.Context(), uri, []string{"maintenance_work_mem", invalidName})
		if partial != nil || err == nil || !strings.Contains(err.Error(), "42704") || strings.Contains(err.Error(), "snapshot-injection-secret") || strings.Contains(err.Error(), uri) {
			t.Fatal("snapshot failed to bind a setting name safely or returned partial results")
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

func postgresFixtureSearchPath(uri, path string) string {
	parsed, _ := url.Parse(uri)
	query := parsed.Query()
	query.Set("search_path", path)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}
