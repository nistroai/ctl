// Package smoke_test exercises a released CLI against a real Nistro server.
// TestProductionSmoke checks login, logout, reads, and discovery.
// TestProductionCanary additionally provisions and verifies dedicated resources.
package smoke_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"nistro-ctl/test/testutils"
)

func TestProductionSmoke(t *testing.T) {
	enabled := strings.TrimSpace(os.Getenv("ENABLE_SMOKE_TEST"))
	if enabled == "" {
		t.Skip("set ENABLE_SMOKE_TEST=true to run against a real server")
	}
	run, err := strconv.ParseBool(enabled)
	if err != nil {
		t.Fatal("ENABLE_SMOKE_TEST must be a boolean")
	}
	if !run {
		t.Skip("live smoke tests are disabled")
	}

	cli := newLiveCLI(t)

	for _, product := range []string{"inference", "postgres"} {
		t.Run(product+"/deployments", func(t *testing.T) {
			response := runJSON(t, cli, product, "list", "-o", "json")
			rows := array(t, response, "deployments", false)
			for _, raw := range rows {
				row, ok := raw.(map[string]any)
				if !ok {
					t.Fatal("deployment list entries must be objects")
				}
				textField(t, row, "id")
			}
			id := strings.TrimSpace(os.Getenv("NISTRO_SMOKE_" + strings.ToUpper(product) + "_ID"))
			if id == "" && len(rows) > 0 {
				id = rows[0].(map[string]any)["id"].(string)
			}
			t.Run("describe", func(t *testing.T) {
				if id == "" {
					t.Skip("no deployment fixture or existing deployment to describe")
				}
				details := runJSON(t, cli, product, "describe", id, "-o", "json")
				// The raw resource endpoint can return an envelope or the resource.
				if deployment, ok := details["deployment"].(map[string]any); ok {
					details = deployment
				}
				if details["id"] != id {
					t.Fatal("describe returned a different deployment ID")
				}
			})
		})
	}

	t.Run("postgres/discovery", func(t *testing.T) {
		catalog := runJSON(t, cli, "postgres", "placements", "scale", "-o", "json")
		cloud, region, instance := postgresPlacement(t, catalog)
		for _, tier := range []string{"on-demand", "scale"} {
			t.Run(tier, func(t *testing.T) {
				args := []string{"postgres", "offers", tier, "--storage-gib", "50", "-o", "json"}
				if tier == "on-demand" {
					args = append(args, "--vcpu", "2", "--location", "ANY")
				} else {
					args = append(args, "--cloud", cloud, "--region", region, "--instance-type", instance, "--replicas", "1")
				}
				response := runJSON(t, cli, args...)
				assertOffers(t, response, "estimated_price_per_hour_usd")
				assertNoField(t, response, "estimated_price_per_month_usd")
			})
		}
	})

	t.Run("inference/discovery", func(t *testing.T) {
		model := setting("NISTRO_SMOKE_MODEL", "Qwen/Qwen2.5-7B-Instruct")
		contextLength := setting("NISTRO_SMOKE_MAX_MODEL_LEN", "4096")
		n, err := strconv.Atoi(contextLength)
		if err != nil || n <= 0 {
			t.Fatal("NISTRO_SMOKE_MAX_MODEL_LEN must be a positive integer")
		}
		modelArgs := []string{"--model-url", model, "--max-model-len", contextLength, "-o", "json"}
		args := append([]string{"inference", "recommend-hardware", "scale"}, modelArgs...)
		args = append(args, "--location", setting("NISTRO_SMOKE_LOCATION", "US"))
		recommendation := runJSON(t, cli, args...)
		options := array(t, recommendation, "deployment_options", true)
		placement, ok := options[0].(map[string]any)
		if !ok {
			t.Fatal("hardware recommendation must contain objects")
		}
		cloud := textField(t, placement, "cloud")
		region := textField(t, placement, "region")
		instance := textField(t, placement, "sku")
		for _, tier := range []string{"on-demand", "scale"} {
			t.Run(tier, func(t *testing.T) {
				args := append([]string{"inference", "offers", tier}, modelArgs...)
				if tier == "on-demand" {
					args = append(args, "--location", "ANY")
				} else {
					args = append(args, "--cloud", cloud, "--region", region, "--instance-type", instance, "--replicas", "1")
					if gpu, ok := placement["gpu_type"].(string); ok && gpu != "" {
						args = append(args, "--gpu-type", gpu)
					}
				}
				assertOffers(t, runJSON(t, cli, args...), "price_per_hour_usd")
			})
		}
	})
}

// newLiveCLI verifies the selected executable and authenticates an isolated
// session. Callers register resource cleanup afterward so logout happens last.
func newLiveCLI(t *testing.T) *testutils.CLI {
	t.Helper()
	// An enabled run must never build from source or silently skip missing auth.
	binary := strings.TrimSpace(os.Getenv("NISTRO_TEST_BINARY"))
	if binary == "" {
		t.Fatal("NISTRO_TEST_BINARY must point to the binary to validate")
	}
	email := strings.TrimSpace(os.Getenv("NISTRO_EMAIL"))
	password := os.Getenv("NISTRO_PASSWORD")
	token := strings.TrimSpace(os.Getenv("NISTRO_TOKEN"))
	if (email == "") != (password == "") {
		t.Fatal("set both NISTRO_EMAIL and NISTRO_PASSWORD, or use only NISTRO_TOKEN")
	}
	if email == "" && token == "" {
		t.Fatal("enabled smoke tests require NISTRO_EMAIL and NISTRO_PASSWORD, or NISTRO_TOKEN")
	}
	cli := testutils.New(t, testutils.Options{
		Binary: binary, Server: setting("NISTRO_SERVER", "https://api.nistro.ai"),
		Timeout: 90 * time.Second, Secrets: []string{email, password, token},
	})

	if !t.Run("version", func(t *testing.T) {
		result := cli.MustRun(t, "", "--version")
		fields := strings.Fields(result.Stdout)
		if len(fields) < 2 || fields[0] != "nistro" || result.Stderr != "" {
			t.Fatal("expected nistro version on stdout and no stderr")
		}
		if expected := os.Getenv("NISTRO_TEST_EXPECTED_VERSION"); expected != "" && fields[1] != expected {
			t.Fatalf("binary version %q does not match release %q", fields[1], expected)
		}
	}) {
		t.Fatal("selected binary did not pass release version verification")
	}

	if email != "" {
		if !t.Run("login", func(t *testing.T) {
			result := cli.MustRun(t, password+"\n", "login", "--email", email, "--password-stdin")
			if !strings.Contains(result.Stdout, "Logged in successfully.") || result.Stderr != "" {
				t.Fatal("login did not confirm success cleanly")
			}
			path := filepath.Join(cli.Home(), ".nistro", "sessions.json")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal("login did not persist a session")
			}
			var sessions map[string]string
			if json.Unmarshal(data, &sessions) != nil || sessions[strings.TrimRight(setting("NISTRO_SERVER", "https://api.nistro.ai"), "/")] == "" {
				t.Fatal("login did not save a token for the selected server")
			}
			for _, saved := range sessions {
				if saved != "" && strings.Contains(result.Stdout+result.Stderr, saved) {
					t.Fatal("login exposed the saved token")
				}
			}
			if runtime.GOOS != "windows" {
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm()&0077 != 0 {
					t.Fatal("saved session is accessible to other users")
				}
			}
		}) {
			t.Fatal("login failed; authenticated checks cannot run")
		}
		t.Cleanup(func() {
			cli.MustRun(t, "", "logout")
			data, err := os.ReadFile(filepath.Join(cli.Home(), ".nistro", "sessions.json"))
			var sessions map[string]string
			if err != nil || json.Unmarshal(data, &sessions) != nil || len(sessions) != 0 {
				t.Error("logout did not clear the smoke test session")
			}
		})
	} else {
		cli.SetToken(token)
		t.Run("login", func(t *testing.T) { t.Skip("token authentication configured; password login is not exercised") })
	}

	return cli
}

func setting(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func runJSON(t *testing.T, cli *testutils.CLI, args ...string) map[string]any {
	t.Helper()
	result := cli.MustRun(t, "", args...)
	if result.Stderr != "" {
		t.Fatal("successful JSON command emitted stderr")
	}
	var response map[string]any
	if err := json.Unmarshal([]byte(result.Stdout), &response); err != nil || response == nil {
		// Do not print API responses: describe JSON can include credentials.
		t.Fatal("command must return exactly one JSON object")
	}
	return response
}

func array(t *testing.T, object map[string]any, key string, nonempty bool) []any {
	t.Helper()
	rows, ok := object[key].([]any)
	if !ok || nonempty && len(rows) == 0 {
		t.Fatalf("response must contain %s as an array (nonempty=%t)", key, nonempty)
	}
	return rows
}

func textField(t *testing.T, object map[string]any, key string) string {
	t.Helper()
	value, ok := object[key].(string)
	if !ok || strings.TrimSpace(value) == "" {
		t.Fatalf("response must contain nonempty %s", key)
	}
	return value
}

func postgresPlacement(t *testing.T, catalog map[string]any) (string, string, string) {
	t.Helper()
	for _, rawCloud := range array(t, catalog, "clouds", true) {
		cloud, ok := rawCloud.(map[string]any)
		if !ok {
			t.Fatal("catalog clouds must be objects")
		}
		for _, rawRegion := range array(t, cloud, "regions", false) {
			region, ok := rawRegion.(map[string]any)
			if !ok {
				t.Fatal("catalog regions must be objects")
			}
			for _, rawInstance := range array(t, region, "instance_types", false) {
				if instance, ok := rawInstance.(string); ok && strings.TrimSpace(instance) != "" {
					return textField(t, cloud, "cloud"), textField(t, region, "region"), instance
				}
			}
		}
	}
	t.Fatal("catalog has no usable Postgres placement")
	return "", "", ""
}

func assertOffers(t *testing.T, response map[string]any, priceField string) {
	t.Helper()
	for _, raw := range array(t, response, "deployment_options", true) {
		offer, ok := raw.(map[string]any)
		if !ok {
			t.Fatal("offers must be objects")
		}
		price, ok := offer[priceField].(float64)
		if !ok || price < 0 {
			t.Fatalf("offer must include a nonnegative numeric %s", priceField)
		}
	}
}

func assertNoField(t *testing.T, value any, field string) {
	t.Helper()
	switch value := value.(type) {
	case map[string]any:
		if _, exists := value[field]; exists {
			t.Fatalf("JSON must omit %s", field)
		}
		for _, child := range value {
			assertNoField(t, child, field)
		}
	case []any:
		for _, child := range value {
			assertNoField(t, child, field)
		}
	}
}
