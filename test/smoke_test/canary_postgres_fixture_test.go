package smoke_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"nistro-ctl/test/canaryutils"
	"nistro-ctl/test/testutils"
)

// These cases run beneath TestCanaryLocalPostgresLifecycle, including through
// the mandatory disposable TLS PostgreSQL script. SQL mutations stay local.
func testLocalPostgresTransitions(t *testing.T, binary string) {
	custom := map[string]string{"work_mem": "32MB", "synchronous_commit": "off"}
	for _, tc := range []struct {
		name, mode, wantError string
		initial, replacement  map[string]string
		update                map[string]any
		patches               int
	}{
		{name: "CLI default version is rejected before PATCH", update: map[string]any{"postgres_version": "ghcr.io/cloudnative-pg/postgresql:18"}, wantError: "does not change effective pre-update values"},
		{name: "reported version transition is verified", update: map[string]any{"postgres_version": "ghcr.io/cloudnative-pg/postgresql:19"}, patches: 1},
		{name: "empty default parameter set is rejected before PATCH", replacement: map[string]string{}, wantError: "does not change effective pre-update values"},
		{name: "custom create parameters are verified", initial: map[string]string{"work_mem": "32MB"}, update: map[string]any{"replicas": 2}, patches: 1},
		{name: "search path update preserves and cleans the checkpoint", replacement: map[string]string{"search_path": "pg_catalog"}, patches: 1},
		{name: "adjacent large integer values are a transition", initial: map[string]string{"vacuum_failsafe_age": "1600000000"}, replacement: map[string]string{"vacuum_failsafe_age": "1600000001"}, patches: 2},
		{name: "ignored large integer change fails despite replica update", mode: "ignored-replacement", initial: map[string]string{"vacuum_failsafe_age": "1600000000"}, replacement: map[string]string{"vacuum_failsafe_age": "1600000001"}, update: map[string]any{"replicas": 2}, wantError: "configured setting did not match its requested value", patches: 2},
		{name: "ignored custom create parameters fail before PATCH", mode: "ignored-create", initial: map[string]string{"work_mem": "32MB"}, update: map[string]any{"replicas": 2}, wantError: "initial readiness/data plane"},
		{name: "subset replacement restores removed setting", initial: custom, replacement: map[string]string{"work_mem": "64MB"}, patches: 2},
		{name: "retained value with removed setting is a transition", initial: custom, replacement: map[string]string{"work_mem": "32MB"}, patches: 2},
		{name: "ignored removed setting fails despite new metadata", mode: "ignored-removal", initial: custom, replacement: map[string]string{"work_mem": "64MB"}, wantError: "removed setting did not return to its platform baseline", patches: 2},
		{name: "ignored subset with unchanged retained value is rejected", mode: "ignored-replacement", initial: custom, replacement: map[string]string{"work_mem": "32MB"}, wantError: "removed setting did not return to its platform baseline", patches: 2},
		{name: "metadata update with stale SQL is rejected", mode: "ignored-replacement", initial: custom, replacement: map[string]string{"work_mem": "64MB"}, wantError: "configured setting did not match its requested value", patches: 2},
		{name: "empty replacement restores all initial settings", initial: custom, replacement: map[string]string{}, patches: 2},
		{name: "ignored empty replacement is rejected", mode: "ignored-replacement", initial: custom, replacement: map[string]string{}, wantError: "removed setting did not return to its platform baseline", patches: 2},
		{name: "unit equivalent replacement is rejected before tested PATCH", initial: map[string]string{"work_mem": "32MB"}, replacement: map[string]string{"work_mem": "32768kB"}, wantError: "does not change effective pre-update values", patches: 1},
	} {
		var fixture *canaryFixture
		t.Run(tc.name, func(t *testing.T) {
			c := fixtureCanary("postgres", "scale")
			if tc.initial != nil {
				c.create["parameters"] = tc.initial
			}
			c.update = tc.update
			if tc.replacement != nil {
				c.update = map[string]any{"parameters": tc.replacement}
				for key, value := range tc.update {
					c.update[key] = value
				}
			}
			endpoint := localCanaryPostgres(t, &c)
			uri, err := canaryPostgresConnection(map[string]any{"writer_endpoint_url": endpoint}, c.create)
			if err != nil {
				t.Fatal(err)
			}
			connection := localCanarySQL(t, uri)
			names := canaryParameterNames(tc.initial, tc.replacement)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			baseline, err := canaryutils.ReadPostgresSettings(ctx, uri, names)
			cancel()
			if err != nil {
				t.Fatal("read local PostgreSQL baseline")
			}
			parsed, _ := url.Parse(uri)
			database := strings.TrimPrefix(parsed.Path, "/")
			set := func(name, value string) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				statement := "ALTER DATABASE " + pgx.Identifier{database}.Sanitize() + " SET " + pgx.Identifier{name}.Sanitize() + " TO '" + strings.ReplaceAll(value, "'", "''") + "'"
				if name == "search_path" {
					// Preserve GUC list syntax instead of quoting the entire
					// comma-separated value as a single schema identifier.
					var observed string
					if err := connection.QueryRow(ctx, "SELECT pg_catalog.set_config($1, $2, false)", name, value).Scan(&observed); err != nil {
						t.Error("apply local PostgreSQL search path")
						return
					}
					statement = "ALTER DATABASE " + pgx.Identifier{database}.Sanitize() + " SET search_path FROM CURRENT"
				}
				if _, err := connection.Exec(ctx, statement); err != nil {
					t.Error("apply local PostgreSQL setting")
				}
			}
			reset := func() {
				for name, value := range baseline {
					set(name, value.Value)
				}
			}
			t.Cleanup(reset)
			apply := func(parameters map[string]string, restoreRemoved bool) {
				if restoreRemoved {
					reset()
				}
				for name, value := range parameters {
					set(name, value)
				}
			}
			fixture = &canaryFixture{t: t, c: c, id: "owned-postgres-transition", endpoint: endpoint, mode: tc.mode, bootstrapParameters: tc.replacement != nil && len(tc.initial) > 0}
			customVerifiedBeforeTestedPatch := false
			fixture.applyCreate = func(body map[string]any) {
				parameters, supplied, err := canaryParameters(body)
				if err != nil {
					t.Error(err)
					return
				}
				if supplied && tc.mode != "ignored-create" {
					apply(parameters, true)
				}
			}
			fixture.applyUpdate = func(body map[string]any, number int) {
				if fixture.bootstrapParameters && number == 1 {
					parameters, _, err := canaryParameters(body)
					if err != nil {
						t.Error(err)
						return
					}
					apply(parameters, true)
					return
				}
				if len(tc.initial) > 0 {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					err := canaryutils.CheckPostgresSettings(ctx, uri, tc.initial)
					cancel()
					if err != nil {
						t.Error("tested PATCH ran before custom initial SQL settings were applied")
					} else {
						customVerifiedBeforeTestedPatch = true
					}
				}
				parameters, supplied, err := canaryParameters(body)
				if err != nil {
					t.Error(err)
					return
				}
				if supplied && tc.mode != "ignored-replacement" {
					apply(parameters, tc.mode != "ignored-removal")
				}
			}
			checkpointCount := func(want int) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				var count int
				if err := connection.QueryRow(ctx, "SELECT count(*) FROM pg_tables WHERE schemaname='public' AND tablename LIKE 'nistro_canary_%'").Scan(&count); err != nil || count != want {
					t.Error("checkpoint did not survive customization/update or cleanup")
				}
			}
			fixture.beforeUpdate = func() { checkpointCount(1) }
			fixture.beforeDelete = func() { checkpointCount(0) }
			control := httptest.NewServer(http.HandlerFunc(fixture.control))
			t.Cleanup(control.Close)
			cli := testutils.New(t, testutils.Options{Binary: binary, Server: control.URL, Token: "fixture-control-token", Timeout: 5 * time.Second})
			timing := canaryTiming{lifecycle: 20 * time.Second, cleanup: 5 * time.Second, poll: 10 * time.Millisecond, probe: time.Second}
			if tc.mode == "ignored-create" || tc.mode == "ignored-removal" || tc.mode == "ignored-replacement" {
				timing.lifecycle = 5 * time.Second
			}
			err = runCanaryLifecycle(t, cli, c, timing)
			if tc.wantError == "" && err != nil {
				t.Fatalf("PostgreSQL transition failed: %v", err)
			}
			if tc.wantError != "" && (err == nil || !strings.Contains(err.Error(), tc.wantError)) {
				t.Fatalf("expected %s, got %v", tc.wantError, err)
			}
			fixture.mu.Lock()
			patches, initialVerified := fixture.updates, customVerifiedBeforeTestedPatch
			fixture.mu.Unlock()
			if patches != tc.patches {
				t.Errorf("PATCH count = %d, want %d", patches, tc.patches)
			}
			if tc.replacement != nil && len(tc.initial) > 0 && tc.patches == 2 && !initialVerified {
				t.Error("replacement fixture did not first verify custom initial SQL settings")
			}
			if tc.patches == 2 && tc.wantError != "" && !strings.Contains(err.Error(), "updated readiness/data plane") {
				t.Error("replacement failure occurred before the tested PATCH/data-plane phase")
			}
		})
		if fixture == nil {
			continue
		}
		events := fixture.snapshot()
		deletes := 0
		for _, event := range events {
			if event == "delete" {
				deletes++
			}
		}
		if deletes != 1 || len(events) == 0 || events[len(events)-1] != "list" {
			t.Errorf("PostgreSQL transition fixture did not delete and confirm its owned deployment after %s", tc.name)
		}
	}
}

func localCanarySQL(t *testing.T, uri string) *pgx.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for {
		connection, err := pgx.Connect(ctx, uri)
		if err == nil {
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = connection.Close(ctx)
			})
			return connection
		}
		if err := canaryPause(ctx, 50*time.Millisecond); err != nil {
			t.Fatal("connect to local TLS PostgreSQL fixture")
		}
	}
}
