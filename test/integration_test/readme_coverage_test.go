package integration_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"nistro-ctl/test/testutils"
)

type readmeInvocation struct {
	text string
	args []string
}

var readmeCodeLine = regexp.MustCompile(`(?m)^nistro[ \t]+([^\r\n]+)$`)
var readmeInlineCommand = regexp.MustCompile("`(nistro [^`\\r\\n]+)`")

// README command lines are read from the checkout, never fetched from a remote
// site or evaluated by a shell. Placeholder IDs/emails are local fixture values.
func TestREADMECommands(t *testing.T) {
	repository, err := testutils.RepositoryRoot()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(repository, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	readme := strings.ReplaceAll(string(data), "\r\n", "\n")
	var examples, documented []readmeInvocation
	for _, match := range readmeCodeLine.FindAllStringSubmatch(readme, -1) {
		invocation := parseREADMEInvocation(t, "nistro "+match[1])
		examples = append(examples, invocation)
		documented = append(documented, invocation)
	}
	for _, match := range readmeInlineCommand.FindAllStringSubmatch(readme, -1) {
		documented = append(documented, parseREADMEInvocation(t, match[1]))
	}
	if len(examples) == 0 || len(documented) == 0 {
		t.Fatal("README must provide executable command examples")
	}

	var unexpectedRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		unexpectedRequests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	cli := newCLI(t, server.URL)
	leaves, _ := discoverCommandTree(t, cli)
	paths := make(map[string]bool)
	for _, invocation := range documented {
		path := documentedCommandPath(invocation.args, leaves)
		if path == "" {
			t.Fatalf("README documents an unavailable command: %s", invocation.text)
		}
		paths[path] = true
	}
	if unexpectedRequests.Load() != 0 {
		t.Fatal("README command discovery contacted an API")
	}

	recorder := &commandRecorder{}
	activeCommandRecorders.Store(t.Name(), recorder)
	t.Cleanup(func() { activeCommandRecorders.Delete(t.Name()) })
	// A newly documented invocation must actually run. This also catches a
	// scenario skipped before it can verify the request or output.
	t.Cleanup(func() {
		recorder.Lock()
		defer recorder.Unlock()
		for _, invocation := range documented {
			found := false
			for _, run := range recorder.runs {
				if readmeInvocationMatches(invocation.args, run.args) && invocationFormat(invocation.args) == invocationFormat(run.args) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("README invocation has no successful execution in its documented output format: %s", invocation.text)
			}
		}
		for _, path := range []string{
			"inference recommend-hardware scale", "inference offers on-demand", "inference offers scale",
			"postgres offers on-demand", "postgres offers scale",
		} {
			for _, source := range []string{"file", "stdin"} {
				found := false
				for _, run := range recorder.runs {
					if !invocationMatches(path, run.args) {
						continue
					}
					for i, arg := range run.args {
						if arg == "--config-file" && i+1 < len(run.args) && (source == "stdin" && run.args[i+1] == "-" || source == "file" && run.args[i+1] != "-") {
							found = true
						}
					}
				}
				if !found {
					t.Errorf("README config support was not exercised for %s using %s", path, source)
				}
			}
		}
		if !t.Failed() {
			t.Logf("executed %d README command-line examples and covered %d documented command paths", len(examples), len(paths))
		}
	})

	for i, invocation := range examples {
		path := documentedCommandPath(invocation.args, leaves)
		t.Run(fmt.Sprintf("examples/%02d/%s", i+1, strings.ReplaceAll(path, " ", "/")), func(t *testing.T) {
			testREADMEExample(t, invocation.args)
		})
	}
	t.Run("authentication and environment", testREADMEAuthAndConfig)
	t.Run("lists describes and deletes", testREADMEDocumentedReadsAndDeletes)
	t.Run("discovery config files and stdin", testREADMEDiscoveryConfig)
	t.Run("documented binary help", func(t *testing.T) {
		// This line includes shell substitutions for platform naming in README;
		// the harness already selects the binary, so only its --help is needed.
		if !strings.Contains(readme, "./dist/nistro-$(go env GOOS)-$(go env GOARCH) --help") {
			t.Fatal("update the README binary help scenario when its build example changes")
		}
		assertUsagePath(t, newCLI(t, server.URL).MustRun(t, "", "--help"), "")
	})
}

func parseREADMEInvocation(t *testing.T, text string) readmeInvocation {
	t.Helper()
	fields := strings.Fields(text)
	if len(fields) < 2 || fields[0] != "nistro" {
		t.Fatalf("invalid README CLI invocation: %s", text)
	}
	args := slices.Clone(fields[1:])
	for i, value := range args {
		if value == "<id>" {
			args[i] = "fixture-deployment"
		}
	}
	return readmeInvocation{text: text, args: args}
}

func documentedCommandPath(args []string, leaves map[string]bool) string {
	best := ""
	for path := range leaves {
		if invocationMatches(path, args) && len(path) > len(best) {
			best = path
		}
	}
	return best
}

func readmeInvocationMatches(expected, actual []string) bool {
	if len(actual) < len(expected) {
		return false
	}
	for i, value := range expected {
		if value == "<email>" {
			if actual[i] == "" || strings.HasPrefix(actual[i], "--") {
				return false
			}
		} else if actual[i] != value {
			return false
		}
	}
	return true
}

func testREADMEDocumentedReadsAndDeletes(t *testing.T) {
	for _, product := range []struct{ name, path string }{
		{"inference", "/v1/vllm-kubeai/deployments"},
		{"postgres", "/v1/postgres/deployments"},
	} {
		t.Run(product.name, func(t *testing.T) {
			deployment := map[string]any{
				"id": "fixture-deployment", "status": "running", "pricing_mode": "basic", "location": "ANY",
				"database_password": "readme-db-password", "vllm_api_key": "readme-api-key",
			}
			type resourceRequest struct {
				method, path string
				list         bool
			}
			steps := []resourceRequest{
				{http.MethodGet, product.path, true},
				{http.MethodGet, product.path + "/fixture-deployment", false},
				{http.MethodDelete, product.path + "/fixture-deployment", false},
				{http.MethodGet, product.path, true},
				{http.MethodGet, product.path + "/fixture-deployment", false},
				{http.MethodDelete, product.path + "/fixture-deployment", false},
			}
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				step := requests.Add(1)
				if step > int32(len(steps)) {
					t.Errorf("unexpected README resource request %d: %s %s", step, r.Method, r.URL.RequestURI())
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				want := steps[step-1]
				if r.Method != want.method || r.URL.Path != want.path {
					t.Errorf("README resource request %d = %s %s, want %s %s", step, r.Method, r.URL.RequestURI(), want.method, want.path)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if r.Header.Get("Authorization") != "Bearer readme-reads-token" || r.Header.Get("Accept") != "application/json" || r.URL.RawQuery != "" {
					t.Error("README resource command omitted auth/JSON headers or added an unexpected query")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if want.list {
					writeJSON(w, map[string]any{"deployments": []any{deployment}})
				} else {
					writeJSON(w, map[string]any{"deployment": deployment})
				}
			}))
			defer server.Close()
			cli := newCLI(t, server.URL)
			cli.SetToken("readme-reads-token")
			for _, format := range []string{"table", "json"} {
				for _, action := range []string{"list", "describe", "delete"} {
					args := []string{product.name, action}
					if action != "list" {
						args = append(args, "fixture-deployment")
					}
					if format == "json" {
						args = append(args, "--output", "json")
					}
					result := cli.MustRun(t, "", args...)
					if format == "json" {
						decodeObject(t, result.Stdout)
						if result.Stderr != "" || !strings.Contains(result.Stdout, "readme-db-password") {
							t.Fatal("README JSON resource output must preserve API fields without progress")
						}
					} else {
						if !strings.Contains(result.Stdout, "fixture-deployment") || !strings.Contains(result.Stdout, "RUNNING") {
							t.Fatal("README resource table omitted identity or status")
						}
						if strings.Contains(result.Stdout, "readme-db-password") || strings.Contains(result.Stdout, "readme-api-key") {
							t.Fatal("README resource table exposed credentials")
						}
					}
				}
			}
			if requests.Load() != int32(len(steps)) {
				t.Fatal("README list/describe/delete did not each issue one request per output format")
			}
		})
	}
}
