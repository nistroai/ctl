package smoke_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"nistro-ctl/test/testutils"
)

type canaryFixture struct {
	mu                                                     sync.Mutex
	t                                                      *testing.T
	c                                                      canaryCase
	id                                                     string
	mode, endpoint                                         string
	created, updated, deleting                             bool
	describes, deleteDescribes, modelRequests, completions int
	events                                                 []string
	beforeUpdate, beforeDelete                             func()
}

func (f *canaryFixture) record(event string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, event)
}

func (f *canaryFixture) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.events...)
}

func (f *canaryFixture) control(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := "/v1/postgres/deployments"
	if f.c.product == "inference" {
		path = "/v1/vllm-kubeai/deployments"
	}
	if r.Header.Get("Authorization") != "Bearer fixture-control-token" || r.URL.RawQuery != "" {
		f.t.Error("lifecycle request did not use the selected session or included a query")
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	write := func(value any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(value)
	}
	switch {
	case r.Method == "GET" && r.URL.Path == path:
		f.events = append(f.events, "list")
		rows := []any{map[string]any{"id": "preexisting-deployment"}}
		if f.created && !f.deleting {
			rows = append(rows, map[string]any{"id": f.id})
		}
		write(map[string]any{"deployments": rows})
	case r.Method == "POST" && r.URL.Path == "/v1/postgres/offers" && f.c.product == "postgres":
		f.events = append(f.events, "offers")
		write(map[string]any{"deployment_options": []any{map[string]any{
			"cloud": f.c.create["cloud"], "region": f.c.create["region"], "instance_type": f.c.create["instance_type"], "vcpu": 2, "ram_gib": 8,
		}}})
	case r.Method == "POST" && r.URL.Path == path:
		f.events = append(f.events, "create")
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			f.t.Error("create did not send JSON")
		}
		mode := "basic"
		if f.c.tier == "scale" {
			mode = "dedicated"
		}
		if body["pricing_mode"] != mode {
			f.t.Error("create selected the wrong pricing tier")
		}
		if f.c.product == "inference" {
			if body["model_url"] != f.c.create["model_url"] {
				f.t.Error("create did not preserve the configured inference model")
			}
			if f.c.tier == "scale" && (canaryNumber(body["min_replicas"]) != 1 || canaryNumber(body["max_replicas"]) != 1) {
				f.t.Error("inference create did not request one fixed replica")
			}
		} else if body["database_password"] != f.c.create["database_password"] || body["database_username"] != f.c.create["database_username"] || body["database_name"] != f.c.create["database_name"] {
			f.t.Error("Postgres create did not preserve unique connection credentials")
		}
		if f.mode == "reused-id" {
			write(map[string]any{"deployment": map[string]any{"id": "preexisting-deployment", "status": "running"}})
			return
		}
		f.created = true
		write(map[string]any{"deployment": map[string]any{"id": f.id, "status": "pending", "pricing_mode": mode}})
	case r.Method == "GET" && r.URL.Path == path+"/"+f.id:
		f.events = append(f.events, "describe")
		if f.deleting {
			f.deleteDescribes++
			if f.deleteDescribes > 1 {
				w.WriteHeader(http.StatusNotFound)
				write(map[string]string{"error": "deleted"})
				return
			}
			write(map[string]any{"deployment": map[string]any{"id": f.id, "status": "deleting"}})
			return
		}
		f.describes++
		status := "running"
		if f.mode == "terminal" {
			status = "failed"
		} else if f.mode == "never-ready" || f.describes <= 2 {
			status = "deploying"
		} else if f.c.product == "inference" && f.c.tier == "on-demand" {
			status = "idle"
		}
		if f.updated && f.mode != "ignored-update" && f.mode != "timestamp-update" && f.mode != "missing-update-fields" && f.describes == 1 {
			status = "updating"
		}
		mode := "basic"
		if f.c.tier == "scale" {
			mode = "dedicated"
		}
		resource := map[string]any{"id": f.id, "status": status, "pricing_mode": mode, "updated_at": "2026-11-03T01:00:00Z"}
		if f.mode == "timestamp-update" && f.updated {
			resource["updated_at"] = "2026-11-03T01:01:00Z"
		}
		if f.c.product == "inference" {
			resource["endpoint_url"], resource["vllm_api_key"] = f.endpoint, "fixture-deployment-key"
			resource["model_url"] = f.c.create["model_url"]
			if f.mode == "wrong-model" {
				resource["model_url"] = "Wrong/Model"
			}
			if f.c.tier == "scale" && f.mode != "timestamp-update" && f.mode != "missing-update-fields" {
				replicas := 1
				if f.updated && f.mode != "ignored-update" {
					replicas = 2
				}
				resource["min_replicas"], resource["max_replicas"] = replicas, replicas
			}
		} else {
			resource["writer_endpoint_url"] = f.endpoint
			resource["database_name"], resource["database_username"] = f.c.create["database_name"], f.c.create["database_username"]
			resource["storage_gib"] = f.c.create["storage_gib"]
			if f.c.tier == "scale" {
				resource["replicas"] = 1
			}
			if f.updated {
				for key, value := range f.c.update {
					resource[key] = value
				}
			}
		}
		write(map[string]any{"deployment": resource})
	case r.Method == "PATCH" && r.URL.Path == path+"/"+f.id:
		f.events = append(f.events, "update")
		if f.beforeUpdate != nil {
			f.beforeUpdate()
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			f.t.Error("update did not send JSON")
		}
		if f.c.product == "inference" {
			if canaryNumber(body["min_replicas"]) != 2 || canaryNumber(body["max_replicas"]) != 2 {
				f.t.Error("update did not change inference fixed replica counts")
			}
		} else {
			for key, want := range f.c.update {
				if !canaryEqual(body[key], want) {
					f.t.Errorf("Postgres update changed the wrong %s value", key)
				}
			}
		}
		f.updated, f.describes = true, 0
		status := "updating"
		if f.mode == "ignored-update" || f.mode == "timestamp-update" || f.mode == "missing-update-fields" {
			status = "running"
		}
		write(map[string]any{"deployment": map[string]any{"id": f.id, "status": status}})
	case r.Method == "DELETE" && r.URL.Path == path+"/"+f.id:
		f.events = append(f.events, "delete")
		if f.beforeDelete != nil {
			f.beforeDelete()
		}
		f.deleting = true
		write(map[string]any{"deployment": map[string]any{"id": f.id, "status": "deleting"}})
	default:
		f.t.Errorf("unexpected lifecycle mutation or route: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusBadRequest)
	}
}

func (f *canaryFixture) inference(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer fixture-deployment-key" {
		f.t.Error("data-plane request omitted deployment API key")
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == "GET" && r.URL.Path == "/openai/v1/models":
		f.modelRequests++
		f.events = append(f.events, "models")
		if f.modelRequests <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "warming up"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"id": "Unrelated/Other-Model"}, map[string]string{"id": canaryString(f.c.create, "model_url")}}})
	case r.Method == "POST" && r.URL.Path == "/openai/v1/chat/completions":
		f.events = append(f.events, "completion")
		f.completions++
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["model"] != f.c.create["model_url"] || body["stream"] != false {
			f.t.Error("canary did not generate a real nonstreaming model completion")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]string{"content": "Hello."}}},
			"usage":   map[string]int{"prompt_tokens": 6, "completion_tokens": 2, "total_tokens": 8},
		})
	default:
		f.t.Errorf("unexpected data-plane request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func fixtureCanary(product, tier string) canaryCase {
	c := canaryCase{product: product, tier: tier, marker: "fixturemarker", create: map[string]any{}}
	if product == "inference" {
		c.create = map[string]any{"model_url": "Qwen/Qwen2.5-0.5B-Instruct", "max_model_len": 2048}
		if tier == "scale" {
			c.create["replicas"], c.create["cloud"], c.create["region"], c.create["instance_type"] = 1, "aws", "us-east-1", "g5.xlarge"
			c.update = map[string]any{"replicas": 2}
		} else {
			c.create["location"] = "ANY"
		}
	} else {
		c.create = map[string]any{"vcpu": 2, "storage_gib": 50, "location": "US", "database_name": "canary_fixture", "database_username": "canary_fixture", "database_password": "FixturePassword2026"}
		c.update = map[string]any{"storage_gib": 60}
		if tier == "scale" {
			delete(c.create, "vcpu")
			delete(c.create, "location")
			c.create["replicas"], c.create["cloud"], c.create["region"], c.create["instance_type"] = 1, "aws", "us-east-1", "m7i.large"
			c.update = map[string]any{"replicas": 2}
		}
	}
	return c
}

func TestCanaryLocalInferenceLifecycle(t *testing.T) {
	binary, cleanup, err := testutils.PrepareBinary()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	for _, tc := range []struct{ name, tier, mode, wantError string }{
		{"on-demand idle and warmup", "on-demand", "", ""},
		{"scale replica update", "scale", "", ""},
		{"timestamp without observed targets is rejected", "scale", "timestamp-update", "actual desired update values"},
		{"terminal state cleans owned deployment", "scale", "terminal", "terminal failure"},
		{"readiness deadline cleans owned deployment", "scale", "never-ready", "deadline"},
		{"ignored update is rejected and cleaned", "scale", "ignored-update", "updated readiness"},
		{"missing update fields and rollout is rejected", "scale", "missing-update-fields", "actual desired update values"},
		{"wrong model identity is rejected", "scale", "wrong-model", "configured model"},
		{"preexisting ID is never deleted", "scale", "reused-id", "preexisting"},
	} {
		var fixture *canaryFixture
		t.Run(tc.name, func(t *testing.T) {
			fixture = &canaryFixture{t: t, c: fixtureCanary("inference", tc.tier), id: "owned-inference", mode: tc.mode}
			data := httptest.NewServer(http.HandlerFunc(fixture.inference))
			t.Cleanup(data.Close)
			fixture.endpoint = data.URL
			control := httptest.NewServer(http.HandlerFunc(fixture.control))
			t.Cleanup(control.Close)
			cli := testutils.New(t, testutils.Options{Binary: binary, Server: control.URL, Token: "fixture-control-token", Timeout: 5 * time.Second})
			timing := canaryTiming{lifecycle: 15 * time.Second, cleanup: 5 * time.Second, poll: 10 * time.Millisecond, probe: time.Second}
			if tc.mode == "never-ready" || tc.mode == "ignored-update" || tc.mode == "missing-update-fields" || tc.mode == "timestamp-update" || tc.mode == "wrong-model" {
				timing.lifecycle = 5 * time.Second
			}
			err := runCanaryLifecycle(t, cli, fixture.c, timing)
			if tc.wantError == "" && err != nil {
				t.Fatalf("local canary failed: %v", err)
			}
			if tc.wantError != "" && (err == nil || !strings.Contains(err.Error(), tc.wantError)) {
				t.Fatalf("expected %s failure, got %v", tc.wantError, err)
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
		if tc.mode == "reused-id" {
			if deletes != 0 {
				t.Fatal("cleanup deleted a preexisting resource")
			}
			continue
		}
		if deletes != 1 || len(events) == 0 || events[len(events)-1] != "list" {
			t.Fatalf("owned resource was not deleted and confirmed after %s: %v", tc.name, events)
		}
		if tc.mode == "ignored-update" || tc.mode == "missing-update-fields" || tc.mode == "timestamp-update" {
			updates := 0
			for _, event := range events {
				if event == "update" {
					updates++
				}
			}
			if updates != 1 || fixture.completions != 1 {
				t.Fatal("update rejection fixture did not first establish initial data-plane readiness and execute PATCH")
			}
		}
		if tc.wantError == "" {
			wantCompletions := 1
			if tc.tier == "scale" {
				wantCompletions = 2
			}
			if fixture.completions != wantCompletions {
				t.Fatal("canary did not recheck the actual inference data plane after update")
			}
			if fixture.modelRequests <= wantCompletions {
				t.Fatal("fixture did not exercise asynchronous serving warmup")
			}
		}
	}
}

func TestCanaryLocalPostgresLifecycle(t *testing.T) {
	binary, cleanup, err := testutils.PrepareBinary()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	for _, tier := range []string{"on-demand", "scale"} {
		t.Run(tier, func(t *testing.T) {
			c := fixtureCanary("postgres", tier)
			endpoint := localCanaryPostgres(t, &c)
			fixture := &canaryFixture{t: t, c: c, id: "owned-postgres", endpoint: endpoint}
			connectionString, err := canaryPostgresConnection(map[string]any{"deployment": map[string]any{"writer_endpoint_url": endpoint}}, c.create)
			if err != nil {
				t.Fatal(err)
			}
			query := func(want int) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				connection, err := pgx.Connect(ctx, connectionString)
				if err != nil {
					t.Error("connect to local SQL checkpoint fixture")
					return
				}
				defer connection.Close(ctx)
				var count int
				if err := connection.QueryRow(ctx, "SELECT count(*) FROM pg_tables WHERE schemaname='public' AND tablename LIKE 'nistro_canary_%'").Scan(&count); err != nil || count != want {
					t.Error("SQL checkpoint was not retained across update or cleaned before deletion")
				}
			}
			fixture.beforeUpdate = func() { query(1) }
			fixture.beforeDelete = func() { query(0) }
			control := httptest.NewServer(http.HandlerFunc(fixture.control))
			t.Cleanup(control.Close)
			cli := testutils.New(t, testutils.Options{Binary: binary, Server: control.URL, Token: "fixture-control-token", Timeout: 5 * time.Second})
			if err := runCanaryLifecycle(t, cli, c, canaryTiming{lifecycle: 20 * time.Second, cleanup: 5 * time.Second, poll: time.Millisecond, probe: time.Second}); err != nil {
				t.Fatalf("local TLS SQL lifecycle failed: %v", err)
			}
		})
	}
}

func TestCanaryCreateOutputErrorCleansOwnedID(t *testing.T) {
	realBinary, cleanup, err := testutils.PrepareBinary()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	directory := t.TempDir()
	source := `package main
import ("fmt"; "os"; "os/exec")
func main() {
 args := os.Args[1:]
 command := exec.Command(os.Getenv("CANARY_FIXTURE_REAL_BINARY"), args...)
 command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
 if err := command.Run(); err != nil {
  if exit, ok := err.(*exec.ExitError); ok { os.Exit(exit.ExitCode()) }
  os.Exit(2)
 }
 if len(args) >= 2 && args[1] == "create" {
  fmt.Fprintln(os.Stderr, "fixture create output failure after accepted request")
  os.Exit(1)
 }
}
`
	mainFile := filepath.Join(directory, "main.go")
	if err := os.WriteFile(mainFile, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(directory, "cli-output-fixture")
	if runtime.GOOS == "windows" {
		wrapper += ".exe"
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	err = exec.CommandContext(ctx, "go", "build", "-o", wrapper, mainFile).Run()
	cancel()
	if err != nil {
		t.Fatal("compile CLI create-output regression fixture")
	}
	t.Setenv("CANARY_FIXTURE_REAL_BINARY", realBinary)
	for _, mode := range []string{"owned-id", "reused-id"} {
		var fixture *canaryFixture
		t.Run(mode, func(t *testing.T) {
			fixture = &canaryFixture{t: t, c: fixtureCanary("inference", "scale"), id: "owned-output-regression", mode: mode}
			control := httptest.NewServer(http.HandlerFunc(fixture.control))
			t.Cleanup(control.Close)
			cli := testutils.New(t, testutils.Options{Binary: wrapper, Server: control.URL, Token: "fixture-control-token", Timeout: 5 * time.Second})
			err := runCanaryLifecycle(t, cli, fixture.c, canaryTiming{lifecycle: 15 * time.Second, cleanup: 5 * time.Second, poll: 10 * time.Millisecond, probe: time.Second})
			if err == nil {
				t.Fatal("nonzero create result with known ID unexpectedly passed")
			}
			if mode == "reused-id" && !strings.Contains(err.Error(), "preexisting") {
				t.Fatal("failed create did not reject the preexisting response ID")
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
		if mode == "owned-id" && (deletes != 1 || events[len(events)-1] != "list") {
			t.Fatal("owned ID from failed create invocation was not deleted and confirmed")
		}
		if mode == "reused-id" && deletes != 0 {
			t.Fatal("cleanup of failed create deleted a preexisting response ID")
		}
	}
}

func localCanaryPostgres(t *testing.T, c *canaryCase) string {
	t.Helper()
	if supplied := strings.TrimSpace(os.Getenv("NISTRO_CANARY_TEST_POSTGRES_URL")); supplied != "" {
		parsed, err := url.Parse(supplied)
		if err != nil || parsed.Hostname() == "" || parsed.User == nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
			t.Fatal("NISTRO_CANARY_TEST_POSTGRES_URL must be a valid PostgreSQL TCP URI")
		}
		host := strings.ToLower(parsed.Hostname())
		if address := net.ParseIP(host); host != "localhost" && (address == nil || !address.IsLoopback()) {
			t.Fatal("NISTRO_CANARY_TEST_POSTGRES_URL must select a loopback fixture")
		}
		password, hasPassword := parsed.User.Password()
		database, err := url.PathUnescape(strings.TrimPrefix(parsed.EscapedPath(), "/"))
		if !hasPassword || password == "" || database == "" || err != nil {
			t.Fatal("local PostgreSQL fixture URI must include credentials and database name")
		}
		c.create["database_name"], c.create["database_username"], c.create["database_password"] = database, parsed.User.Username(), password
		if ca := parsed.Query().Get("sslrootcert"); ca != "" {
			t.Setenv("NISTRO_CANARY_POSTGRES_SSLROOTCERT", ca)
		}
		return supplied
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("Docker is required for the local real PostgreSQL lifecycle fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, err := exec.CommandContext(ctx, "docker", "info").Output()
	cancel()
	if err != nil {
		t.Skip("Docker daemon is required for the local real PostgreSQL lifecycle fixture")
	}
	directory := canaryPostgresTLS(t)
	marker, err := canaryMarker()
	if err != nil {
		t.Fatal(err)
	}
	container := "nistro-canary-fixture-" + marker
	script := "cp /tls/server.crt /var/lib/postgresql/server.crt && cp /tls/server.key /var/lib/postgresql/server.key && chown postgres:postgres /var/lib/postgresql/server.crt /var/lib/postgresql/server.key && chmod 600 /var/lib/postgresql/server.key && exec docker-entrypoint.sh postgres -c ssl=on -c ssl_cert_file=/var/lib/postgresql/server.crt -c ssl_key_file=/var/lib/postgresql/server.key"
	ctx, cancel = context.WithTimeout(context.Background(), 90*time.Second)
	_, err = exec.CommandContext(ctx, "docker", "run", "--detach", "--rm", "--name", container, "--publish", "127.0.0.1::5432", "--env", "POSTGRES_USER="+canaryString(c.create, "database_username"), "--env", "POSTGRES_PASSWORD="+canaryString(c.create, "database_password"), "--env", "POSTGRES_DB="+canaryString(c.create, "database_name"), "--volume", directory+":/tls:ro", "postgres:18-alpine", "sh", "-c", script).CombinedOutput()
	cancel()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = exec.CommandContext(ctx, "docker", "rm", "--force", container).Run()
	})
	if err != nil {
		t.Fatal("start local TLS PostgreSQL fixture")
	}
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	port, err := exec.CommandContext(ctx, "docker", "port", container, "5432/tcp").Output()
	cancel()
	if err != nil {
		t.Fatal("resolve local PostgreSQL fixture port")
	}
	address := strings.TrimSpace(string(port))
	if _, _, err := net.SplitHostPort(address); err != nil {
		t.Fatal("local PostgreSQL fixture did not expose one loopback TCP port")
	}
	t.Setenv("NISTRO_CANARY_POSTGRES_SSLROOTCERT", filepath.Join(directory, "ca.crt"))
	return "postgres://" + address
}

func canaryPostgresTLS(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Nistro canary fixture CA"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, ca, &leafKey.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	privateKey, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		t.Fatal(err)
	}
	for name, block := range map[string]*pem.Block{"ca.crt": {Type: "CERTIFICATE", Bytes: caDER}, "server.crt": {Type: "CERTIFICATE", Bytes: leafDER}, "server.key": {Type: "EC PRIVATE KEY", Bytes: privateKey}} {
		if err := os.WriteFile(filepath.Join(directory, name), pem.EncodeToMemory(block), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return directory
}

func TestCanaryConfigurationAndConnectionSafety(t *testing.T) {
	if !canaryEqual(json.Number("2.0"), 2) || canaryEqual("2", 2) || canaryEqual(false, 0) {
		t.Fatal("target comparison did not preserve JSON numeric semantics and field types")
	}
	for _, value := range []string{"", "postgres/unknown", "postgres/scale,postgres/scale"} {
		if _, err := canaryCaseNames(value); err == nil {
			t.Fatal("invalid canary selection was accepted")
		}
	}
	if names, err := canaryCaseNames("postgres/on-demand,inference/scale"); err != nil || len(names) != 2 {
		t.Fatal("valid explicit partial canary selection was rejected")
	}
	config := map[string]any{}
	for _, value := range []string{"[]", "{} {}", "null"} {
		if mergeCanaryConfig(config, value) == nil {
			t.Fatal("malformed create/update configuration was accepted")
		}
	}
	t.Setenv("NISTRO_CANARY_POSTGRES_SSLROOTCERT", "/fixture/ca.crt")
	c := fixtureCanary("postgres", "on-demand")
	uri, err := canaryPostgresConnection(map[string]any{"deployment": map[string]any{"writer_endpoint_url": "postgres://db.example:5432"}}, c.create)
	if err != nil || !strings.Contains(uri, "sslmode=verify-full") || !strings.Contains(uri, "sslrootcert=%2Ffixture%2Fca.crt") {
		t.Fatal("Postgres connection did not require verified TLS with configured CA")
	}
	if canaryUpdateObserved(map[string]any{"status": "running"}, "inference", map[string]any{"replicas": 2}) {
		t.Fatal("accepted PATCH without fields or rollout evidence counted as applied update")
	}
	if canaryUpdateObserved(map[string]any{"replicas": 1}, "postgres", map[string]any{"replicas": 2}) {
		t.Fatal("rollout evidence overrode a reported incorrect target value")
	}
	uri, err = canaryPostgresConnection(map[string]any{"deployment": map[string]any{"writer_endpoint_url": "https://writer.example"}, "detailedNetworkTopology": map[string]any{"observer": map[string]any{"clusterEndpoint": "wrong-observer.example"}, "reader": map[string]any{"clusterEndpoint": "wrong-reader.example"}}}, c.create)
	parsed, parseErr := url.Parse(uri)
	if err != nil || parseErr != nil || parsed.Host != "writer.example:5432" {
		t.Fatal("unrelated topology replaced the declared writer endpoint")
	}
}
