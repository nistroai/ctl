package integration_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"nistro-ctl/test/testutils"
)

var binary string

// Build once per package. A supplied binary is exercised without rebuilding it.
func TestMain(m *testing.M) {
	path, cleanup, err := testutils.PrepareBinary()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	binary = path
	code := m.Run()
	cleanup()
	os.Exit(code)
}

func newCLI(t *testing.T, server string) *testutils.CLI {
	t.Helper()
	return testutils.New(t, testutils.Options{Binary: binary, Server: server, AfterRun: commandObserver(t)})
}

func decodeObject(t *testing.T, stdout string) map[string]any {
	t.Helper()
	var object map[string]any
	decoder := json.NewDecoder(strings.NewReader(stdout))
	decoder.UseNumber()
	if err := decoder.Decode(&object); err != nil || object == nil {
		t.Fatalf("expected JSON object: %v", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		t.Fatal("stdout must contain exactly one JSON object")
	}
	return object
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func TestBinaryVersionAndHelp(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	cli := newCLI(t, server.URL)
	version := cli.MustRun(t, "", "--version")
	if !strings.HasPrefix(version.Stdout, "nistro ") || !strings.Contains(version.Stdout, "commit ") {
		t.Fatalf("missing binary version metadata: %q", version.Stdout)
	}
	if expected := strings.TrimSpace(os.Getenv("NISTRO_TEST_EXPECTED_VERSION")); expected != "" {
		fields := strings.Fields(version.Stdout)
		if len(fields) < 2 || fields[1] != expected {
			t.Fatalf("binary version = %q, want %q", version.Stdout, expected)
		}
	}
	for _, args := range [][]string{{"--help"}, {"inference", "--help"}, {"postgres", "--help"}, {"login", "--help"}} {
		result := cli.MustRun(t, "", args...)
		if !strings.Contains(result.Stdout, "Usage:") || result.Stderr != "" {
			t.Fatalf("help did not render cleanly for %v", args)
		}
	}
	if requests.Load() != 0 {
		t.Fatal("version/help contacted the API")
	}
}

func testSavedSessionAcrossProcesses(t *testing.T) {
	newServer := func(token string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && r.URL.Path == "/v1/auth/signin" {
				var credentials map[string]string
				if err := json.NewDecoder(r.Body).Decode(&credentials); err != nil {
					t.Error(err)
				}
				if credentials["email"] != "binary-test@example.com" || credentials["password"] != "test-password" {
					t.Error("password-stdin did not reach the sign-in request")
				}
				if r.Header.Get("Authorization") != "" {
					t.Error("login sent an existing token")
				}
				writeJSON(w, map[string]string{"token": token})
				return
			}
			if r.Method != http.MethodGet || r.URL.Path != "/v1/vllm-kubeai/deployments" {
				t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if r.Header.Get("Authorization") != "Bearer "+token && r.Header.Get("Authorization") != "Bearer override-token" {
				w.WriteHeader(http.StatusUnauthorized)
				writeJSON(w, map[string]string{"error": "authentication required"})
				return
			}
			writeJSON(w, map[string]any{"deployments": []any{}, "auth": r.Header.Get("Authorization")})
		}))
	}
	first, second := newServer("first-session-token"), newServer("second-session-token")
	defer first.Close()
	defer second.Close()
	cli := newCLI(t, first.URL)
	login := cli.MustRun(t, "test-password\n", "login", "--email", "binary-test@example.com", "--password-stdin")
	if !strings.Contains(login.Stdout, "Logged in successfully.") || strings.Contains(login.Stdout+login.Stderr, "first-session-token") {
		t.Fatal("login did not return a safe success message")
	}
	path := filepath.Join(cli.Home(), ".nistro", "sessions.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("saved session permissions: %o", info.Mode().Perm())
	}
	list := decodeObject(t, cli.MustRun(t, "", "inference", "list", "-o", "json").Stdout)
	if list["auth"] != "Bearer first-session-token" {
		t.Fatal("the next CLI process did not reuse the saved session")
	}
	cli.SetToken("override-token")
	list = decodeObject(t, cli.MustRun(t, "", "inference", "list", "-o", "json").Stdout)
	if list["auth"] != "Bearer override-token" {
		t.Fatal("NISTRO_TOKEN did not override the saved session")
	}
	cli.SetToken("")
	unauthenticated := cli.Run("", "--server", second.URL, "inference", "list", "-o", "json")
	if unauthenticated.ExitCode == 0 || !strings.Contains(unauthenticated.Stderr, "401") {
		t.Fatal("saved session leaked to another server")
	}
	cli.MustRun(t, "test-password", "--server", second.URL+"/", "login", "--email", "binary-test@example.com", "--password-stdin")
	cli.MustRun(t, "", "logout")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var sessions map[string]string
	if err := json.Unmarshal(data, &sessions); err != nil {
		t.Fatal(err)
	}
	if _, exists := sessions[first.URL]; exists || sessions[second.URL] != "second-session-token" {
		t.Fatal("logout did not clear only the selected server session")
	}
	cli.MustRun(t, "", "--server", second.URL, "inference", "list", "-o", "json")
	if result := cli.Run("", "inference", "list", "-o", "json"); result.ExitCode == 0 {
		t.Fatal("logout left the first server authenticated")
	}
	cli.MustRun(t, "", "logout")
}

func TestLoginPasswordFlag(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/auth/signin" {
			t.Errorf("unexpected login route %s %s", r.Method, r.URL.Path)
		}
		var credentials map[string]string
		if err := json.NewDecoder(r.Body).Decode(&credentials); err != nil {
			t.Error(err)
		}
		if credentials["email"] != "binary-test@example.com" || credentials["password"] != "flag-password" {
			t.Error("login flags did not populate credentials")
		}
		writeJSON(w, map[string]string{"token": "flag-session-token"})
	}))
	defer server.Close()
	cli := newCLI(t, server.URL)
	result := cli.MustRun(t, "", "login", "--email", "binary-test@example.com", "--password", "flag-password")
	if !strings.Contains(result.Stdout, "Logged in successfully.") || strings.Contains(result.Stdout+result.Stderr, "flag-session-token") || requests.Load() != 1 {
		t.Fatal("password flag login did not return a safe success message")
	}
	data, err := os.ReadFile(filepath.Join(cli.Home(), ".nistro", "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	var sessions map[string]string
	if err := json.Unmarshal(data, &sessions); err != nil || sessions[server.URL] != "flag-session-token" {
		t.Fatal("password flag login did not persist the session")
	}
}

func testDeploymentReadsAndOutput(t *testing.T) {
	for _, product := range []struct{ command, path string }{
		{"inference", "/v1/vllm-kubeai/deployments"},
		{"postgres", "/v1/postgres/deployments"},
	} {
		t.Run(product.command, func(t *testing.T) {
			deployment := map[string]any{
				"id": "deployment-123", "status": "running", "pricing_mode": "basic", "location": "ANY",
				"database_password": "fake-db-password", "vllm_api_key": "fake-api-key",
				"connection":    map[string]any{"access_token": "fake-access-token"},
				"request_count": json.Number("9007199254740993"),
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("read command sent %s", r.Method)
				}
				switch r.URL.Path {
				case product.path:
					writeJSON(w, map[string]any{"deployments": []any{deployment}})
				case product.path + "/deployment-123":
					writeJSON(w, map[string]any{"deployment": deployment})
				default:
					t.Errorf("unexpected read path %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			cli := newCLI(t, server.URL)
			for _, args := range [][]string{{product.command, "list", "--output", "json"}, {product.command, "describe", "deployment-123", "-o", "json"}} {
				result := cli.MustRun(t, "", args...)
				decodeObject(t, result.Stdout)
				if result.Stderr != "" || !strings.Contains(result.Stdout, "9007199254740993") || !strings.Contains(result.Stdout, "fake-db-password") {
					t.Fatal("JSON output lost API data or contained progress messages")
				}
			}
			for _, args := range [][]string{{product.command, "list"}, {product.command, "describe", "deployment-123"}} {
				result := cli.MustRun(t, "", args...)
				if !strings.Contains(result.Stdout, "deployment-123") || !strings.Contains(result.Stdout, "RUNNING") {
					t.Fatal("table output did not contain deployment summary")
				}
				for _, secret := range []string{"fake-db-password", "fake-api-key", "fake-access-token"} {
					if strings.Contains(result.Stdout, secret) {
						t.Fatal("table output exposed a credential")
					}
				}
			}
		})
	}
}

func TestErrorsFailTheBinary(t *testing.T) {
	for _, tc := range []struct {
		name, response, diagnostic string
		status                     int
	}{
		{"unauthorized", `{"error":"authentication required"}`, "server returned 401", 401},
		{"server error", `{"error":"backend unavailable"}`, "server returned 503", 503},
		{"malformed response", `{"deployments":`, "unexpected end of JSON input", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.response)
			}))
			defer server.Close()
			result := newCLI(t, server.URL).Run("", "postgres", "list", "-o", "json")
			if result.ExitCode == 0 || result.Err == nil || result.Stdout != "" || !strings.Contains(result.Stderr, tc.diagnostic) {
				t.Fatalf("binary did not expose API failure through exit status/stderr: exit=%d stderr=%q", result.ExitCode, result.Stderr)
			}
		})
	}
}

func TestInvalidInputNeverContactsServer(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		writeJSON(w, map[string]bool{"ok": true})
	}))
	defer server.Close()
	for _, tc := range []struct {
		name, input, diagnostic string
		args                    []string
	}{
		{"unknown command", "", "unknown command", []string{"does-not-exist"}},
		{"unknown flag", "", "unknown flag", []string{"postgres", "list", "--does-not-exist"}},
		{"invalid output", "", "output must be table or json", []string{"inference", "list", "-o", "yaml"}},
		{"invalid resource id", "", "invalid resource ID", []string{"postgres", "delete", "../other", "-o", "json"}},
		{"missing create field", "", "model_url is required", []string{"inference", "create", "on-demand", "--location", "US"}},
		{"malformed config", "[1]", "JSON object", []string{"postgres", "create", "on-demand", "--config-file", "-"}},
		{"extra config object", "{} {}", "exactly one JSON object", []string{"postgres", "offers", "on-demand", "--config-file", "-"}},
		{"unsupported config field", `{"location":"US","unknown":1}`, "field unknown is not supported", []string{"postgres", "create", "on-demand", "--config-file", "-"}},
		{"zero inference replicas", "", "replicas must be an integer from 1 to 8", []string{"inference", "update", "scale", "deployment-123", "--replicas", "0"}},
		{"noninteractive login", "", "interactive login requires a terminal", []string{"login"}},
		{"empty stdin password", "", "password from stdin is empty", []string{"login", "--email", "binary-test@example.com", "--password-stdin"}},
		{"mutually exclusive password flags", "test-password", "password password-stdin", []string{"login", "--email", "binary-test@example.com", "--password", "test-password", "--password-stdin"}},
		{"unavailable inference on-demand update", "", "unknown flag: --replicas", []string{"inference", "update", "on-demand", "deployment-123", "--replicas", "2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := newCLI(t, server.URL).Run(tc.input, tc.args...)
			if result.ExitCode == 0 || !strings.Contains(result.Stderr, tc.diagnostic) {
				t.Fatalf("invalid input did not fail clearly: exit=%d stderr=%q", result.ExitCode, result.Stderr)
			}
		})
	}
	if requests.Load() != 0 {
		t.Fatalf("invalid inputs made %d API calls", requests.Load())
	}
}

func testDiscoveryRequestsAndJSON(t *testing.T) {
	for _, tc := range []struct {
		name, input, method, path string
		args                      []string
		body                      map[string]any
		postgres                  bool
	}{
		{
			name: "postgres catalog", method: "GET", path: "/v1/postgres/scale/catalog",
			args: []string{"postgres", "placements", "scale", "-o", "json"},
		},
		{
			name: "postgres on-demand stdin with flag override", input: `{"vcpu":1,"storage_gib":50,"location":" eu "}`,
			method: "POST", path: "/v1/postgres/offers", postgres: true,
			args: []string{"postgres", "offers", "on-demand", "--config-file", "-", "--vcpu", "2", "-o", "json"},
			body: map[string]any{"vcpu": float64(2), "memory_gib": float64(8), "storage_gib": float64(50), "location": "EU", "pricing_mode": "basic"},
		},
		{
			name: "postgres scale defaults", method: "POST", path: "/v1/postgres/offers", postgres: true,
			args: []string{"postgres", "offers", "scale", "--storage-gib", "100", "--cloud", "aws", "--region", "us-east-1", "-o", "json"},
			body: map[string]any{"storage_gib": float64(100), "cloud": "aws", "region": "us-east-1", "vcpu": float64(1), "memory_gib": float64(1), "replicas": float64(1), "pricing_mode": "dedicated"},
		},
		{
			name: "inference recommendation", method: "POST", path: "/v1/vllm/hardware-recommendations",
			args: []string{"inference", "recommend-hardware", "scale", "--model-url", "Qwen/Qwen2.5-7B-Instruct", "--max-model-len", "4096", "--location", "US", "-o", "json"},
			body: map[string]any{"model": "Qwen/Qwen2.5-7B-Instruct", "context_length": float64(4096), "location": "US", "pricing_mode": "dedicated", "dedicated": true},
		},
		{
			name: "inference on-demand URL", method: "POST", path: "/v1/vllm/price-estimates",
			args: []string{"inference", "offers", "on-demand", "--model-url", "https://huggingface.co/Qwen/Qwen2.5-7B-Instruct", "--max-model-len", "4096", "--location", "any", "-o", "json"},
			body: map[string]any{"url": "https://huggingface.co/Qwen/Qwen2.5-7B-Instruct", "context_length": float64(4096), "location": "ANY", "pricing_mode": "basic", "dedicated": false},
		},
		{
			name: "inference scale replica price", method: "POST", path: "/v1/vllm/price-estimates",
			args: []string{"inference", "offers", "scale", "--model-url", "Qwen/Qwen2.5-7B-Instruct", "--max-model-len", "4096", "--cloud", "aws", "--region", "us-east-1", "--instance-type", "g5.xlarge", "--replicas", "2", "-o", "json"},
			body: map[string]any{"model": "Qwen/Qwen2.5-7B-Instruct", "context_length": float64(4096), "cloud": "aws", "region": "us-east-1", "instance_type": "g5.xlarge", "node_count": float64(2), "pricing_mode": "dedicated", "dedicated": true},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != tc.method || r.URL.Path != tc.path {
					t.Errorf("got %s %s, want %s %s", r.Method, r.URL.Path, tc.method, tc.path)
				}
				if tc.body != nil {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if !reflect.DeepEqual(body, tc.body) {
						t.Errorf("request body = %#v, want %#v", body, tc.body)
					}
					if r.Header.Get("Content-Type") != "application/json" {
						t.Error("request did not use JSON content type")
					}
				}
				if tc.method == "GET" {
					writeJSON(w, map[string]any{"clouds": []any{map[string]any{"cloud": "aws", "regions": []any{map[string]any{"region": "us-east-1", "location": "US", "instance_types": []string{"m7i.large"}}}}}})
					return
				}
				writeJSON(w, map[string]any{"deployment_options": []any{map[string]any{"cloud": "aws", "region": "us-east-1", "sku": "g5.xlarge", "instance_type": "m7i.large", "estimated_price_per_hour_usd": 0.25, "price_per_hour_usd": 0.5, "estimated_price_per_month_usd": 180}}, "warnings": []string{}})
			}))
			defer server.Close()
			result := newCLI(t, server.URL).MustRun(t, tc.input, tc.args...)
			object := decodeObject(t, result.Stdout)
			if requests.Load() != 1 || result.Stderr != "" {
				t.Fatal("discovery did not return clean JSON after one API call")
			}
			if tc.method == "GET" {
				if _, ok := object["clouds"].([]any); !ok {
					t.Fatal("catalog response lost clouds")
				}
			} else if tc.postgres {
				if strings.Contains(result.Stdout, "estimated_price_per_month_usd") || !strings.Contains(result.Stdout, "estimated_price_per_hour_usd") {
					t.Fatal("Postgres quote JSON did not preserve hourly price while removing monthly estimates")
				}
			} else if !strings.Contains(result.Stdout, "price_per_hour_usd") {
				t.Fatal("inference JSON lost price information")
			}
		})
	}
}

// All mutation requests here target local fixtures, never a live deployment.
func testLocalMutationLifecycle(t *testing.T) {
	for _, tc := range []struct {
		product, path, tier, input string
		create, update             []string
		createBody, updateBody     map[string]any
		resolvePostgresOffer       bool
	}{
		{
			product: "postgres", path: "/v1/postgres/deployments", tier: "on-demand",
			input:      `{"vcpu":1,"storage_gib":50,"location":"US"}`,
			create:     []string{"postgres", "create", "on-demand", "--vcpu", "2"},
			update:     []string{"postgres", "update", "on-demand", "deployment-123", "--vcpu", "4", "--storage-gib", "100"},
			createBody: map[string]any{"vcpu": float64(2), "memory_gib": float64(8), "storage_gib": float64(50), "location": "US", "pricing_mode": "basic", "postgres_version": "ghcr.io/cloudnative-pg/postgresql:18"},
			updateBody: map[string]any{"vcpu": float64(4), "memory_gib": float64(16), "storage_gib": float64(100)},
		},
		{
			product: "postgres", path: "/v1/postgres/deployments", tier: "scale", resolvePostgresOffer: true,
			input:  `{"storage_gib":100,"cloud":"aws","region":"us-east-1","instance_type":"m7i.xlarge","replicas":1,"parameters":{"work_mem":"4MB"},"extensions":["vector","pg_stat_statements"],"database_name":"testdb","database_username":"testuser","database_password":"fake-password"}`,
			create: []string{"postgres", "create", "scale", "--replicas", "2"},
			update: []string{"postgres", "update", "scale", "deployment-123", "--storage-gib", "200", "--postgres-version", "ghcr.io/cloudnative-pg/postgresql:19", "--replicas", "0", "--parameters", "work_mem=32MB", "--parameters", "max_connections=100"},
			createBody: map[string]any{
				"storage_gib": float64(100), "cloud": "aws", "region": "us-east-1", "instance_type": "m7i.xlarge", "replicas": float64(2),
				"parameters": map[string]any{"work_mem": "4MB"}, "extensions": []any{"vector", "pg_stat_statements"},
				"database_name": "testdb", "database_username": "testuser", "database_password": "fake-password",
				"vcpu": float64(4), "memory_gib": float64(16), "pricing_mode": "dedicated", "postgres_version": "ghcr.io/cloudnative-pg/postgresql:18",
			},
			updateBody: map[string]any{
				"storage_gib": float64(200), "postgres_version": "ghcr.io/cloudnative-pg/postgresql:19", "replicas": float64(0),
				"parameters": map[string]any{"work_mem": "32MB", "max_connections": "100"},
			},
		},
		{
			product: "inference", path: "/v1/vllm-kubeai/deployments", tier: "on-demand",
			input:  `{"model_url":"Qwen/Qwen2.5-7B-Instruct","max_model_len":4096,"location":" any "}`,
			create: []string{"inference", "create", "on-demand", "--max-model-len", "2048", "--quantization", "Auto"},
			createBody: map[string]any{
				"model_url": "Qwen/Qwen2.5-7B-Instruct", "max_model_len": float64(2048), "location": "ANY", "pricing_mode": "basic",
				"min_replicas": float64(0), "max_replicas": float64(1), "enable_prefix_caching": true, "enable_chunked_prefill": true,
			},
		},
		{
			product: "inference", path: "/v1/vllm-kubeai/deployments", tier: "scale",
			input:      `{"model_url":"Qwen/Qwen2.5-7B-Instruct","max_model_len":4096,"cloud":"aws","region":"us-east-1","instance_type":"g5.xlarge","replicas":1}`,
			create:     []string{"inference", "create", "scale", "--replicas", "2"},
			update:     []string{"inference", "update", "scale", "deployment-123", "--replicas", "3"},
			createBody: map[string]any{"model_url": "Qwen/Qwen2.5-7B-Instruct", "max_model_len": float64(4096), "cloud": "aws", "region": "us-east-1", "instance_type": "g5.xlarge", "min_replicas": float64(2), "max_replicas": float64(2), "pricing_mode": "dedicated", "enable_prefix_caching": true, "enable_chunked_prefill": true},
			updateBody: map[string]any{"min_replicas": float64(3), "max_replicas": float64(3)},
		},
	} {
		for _, format := range []string{"json", "table"} {
			t.Run(tc.product+"/"+tc.tier+"/"+format, func(t *testing.T) {
				type requestStep struct {
					method, path string
					body         map[string]any
				}
				steps := []requestStep{}
				if tc.resolvePostgresOffer {
					offerBody := map[string]any{}
					for key, value := range tc.createBody {
						offerBody[key] = value
					}
					offerBody["vcpu"], offerBody["memory_gib"] = 0.5, float64(2)
					steps = append(steps, requestStep{"POST", "/v1/postgres/offers", offerBody})
				}
				steps = append(steps, requestStep{"POST", tc.path, tc.createBody})
				if tc.update != nil {
					steps = append(steps, requestStep{"GET", tc.path + "/deployment-123", nil}, requestStep{"PATCH", tc.path + "/deployment-123", tc.updateBody})
				}
				steps = append(steps, requestStep{"DELETE", tc.path + "/deployment-123", nil})
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					step := requests.Add(1)
					if step > int32(len(steps)) {
						t.Errorf("unexpected lifecycle step %d: %s %s", step, r.Method, r.URL.Path)
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					want := steps[step-1]
					if r.Method != want.method || r.URL.Path != want.path {
						t.Errorf("lifecycle step %d = %s %s, want %s %s", step, r.Method, r.URL.Path, want.method, want.path)
					}
					if want.body != nil {
						var body map[string]any
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							t.Error(err)
						}
						if !reflect.DeepEqual(body, want.body) {
							t.Errorf("body = %#v, want %#v", body, want.body)
						}
					}
					if r.URL.Path == "/v1/postgres/offers" {
						// Include an unrelated offer first to prove creation selects the pinned placement.
						writeJSON(w, map[string]any{"deployment_options": []any{
							map[string]any{"cloud": "aws", "region": "us-west-2", "instance_type": "m7i.xlarge", "vcpu": float64(8), "ram_gib": float64(32)},
							map[string]any{"cloud": "aws", "region": "us-east-1", "instance_type": "m7i.xlarge", "vcpu": float64(4), "ram_gib": float64(16)},
						}})
						return
					}
					mode := "basic"
					if tc.tier == "scale" {
						mode = "dedicated"
					}
					writeJSON(w, map[string]any{"deployment": map[string]any{
						"id": "deployment-123", "pricing_mode": mode, "status": "running",
						"database_password": "fake-password", "vllm_api_key": "fake-api-key",
					}})
				}))
				defer server.Close()
				cli := newCLI(t, server.URL)
				config := filepath.Join(t.TempDir(), "deployment.json")
				if err := os.WriteFile(config, []byte(tc.input), 0600); err != nil {
					t.Fatal(err)
				}
				create := append(append([]string{}, tc.create...), "--config-file", config)
				commands := [][]string{create}
				if tc.update != nil {
					commands = append(commands, tc.update)
				}
				commands = append(commands, []string{tc.product, "delete", "deployment-123"})
				for _, args := range commands {
					if format == "json" {
						args = append(append([]string{}, args...), "-o", "json")
					}
					result := cli.MustRun(t, "", args...)
					if format == "json" {
						decodeObject(t, result.Stdout)
						if result.Stderr != "" {
							t.Fatal("mutation JSON output included progress")
						}
					} else {
						if !strings.Contains(result.Stdout, "deployment-123") || !strings.Contains(result.Stdout, "RUNNING") {
							t.Fatal("mutation table output omitted deployment identity or status")
						}
						if strings.Contains(result.Stdout, "fake-password") || strings.Contains(result.Stdout, "fake-api-key") {
							t.Fatal("mutation table output exposed credentials")
						}
					}
				}
				if requests.Load() != int32(len(steps)) {
					t.Fatalf("lifecycle made %d calls, want %d", requests.Load(), len(steps))
				}
			})
		}
	}
}

func TestUpdateRejectsWrongTier(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != "GET" || r.URL.Path != "/v1/vllm-kubeai/deployments/deployment-123" {
			t.Errorf("wrong-tier update sent mutation: %s %s", r.Method, r.URL.Path)
		}
		writeJSON(w, map[string]any{"deployment": map[string]any{"pricing_mode": "basic"}})
	}))
	defer server.Close()
	result := newCLI(t, server.URL).Run("", "inference", "update", "scale", "deployment-123", "--replicas", "2", "-o", "json")
	if result.ExitCode == 0 || !strings.Contains(result.Stderr, "deployment does not belong") || requests.Load() != 1 {
		t.Fatal("the binary did not reject updating a deployment in the wrong tier")
	}
}
