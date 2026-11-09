package integration_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"nistro-ctl/test/testutils"
)

// README automation examples use flags, stdin, or environment credentials.
// Interactive terminal prompting requires a separate terminal test.
func testREADMEAuthAndConfig(t *testing.T) {
	t.Run("documented login credentials", func(t *testing.T) {
		for _, tc := range []struct {
			name, input, email, password string
			args                         []string
			env                          map[string]string
		}{
			{
				name: "NISTRO_EMAIL and NISTRO_PASSWORD", email: "env-login@example.com", password: "env-password",
				args: []string{"login"},
				env:  map[string]string{"NISTRO_EMAIL": "env-login@example.com", "NISTRO_PASSWORD": "env-password"},
			},
			{
				name: "credential flags override environment", email: "flag-login@example.com", password: "flag-password",
				args: []string{"login", "--email", "flag-login@example.com", "--password", "flag-password"},
				env:  map[string]string{"NISTRO_EMAIL": "unused-env@example.com", "NISTRO_PASSWORD": "unused-env-password"},
			},
			{
				name: "email flag and password stdin", input: "stdin-password\n", email: "stdin-login@example.com", password: "stdin-password",
				args: []string{"login", "--email", "stdin-login@example.com", "--password-stdin"},
				env:  map[string]string{"NISTRO_EMAIL": "unused-env@example.com", "NISTRO_PASSWORD": "unused-env-password"},
			},
			{
				name: "environment email and password stdin", input: "stdin-password", email: "env-stdin-login@example.com", password: "stdin-password",
				args: []string{"login", "--password-stdin"},
				env:  map[string]string{"NISTRO_EMAIL": "env-stdin-login@example.com", "NISTRO_PASSWORD": "unused-env-password"},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				const sessionToken = "readme-session-token"
				const overrideToken = "readme-automation-token"
				var loginCalls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodPost && r.URL.Path == "/v1/auth/signin" {
						loginCalls.Add(1)
						var credentials map[string]string
						decoder := json.NewDecoder(r.Body)
						if err := decoder.Decode(&credentials); err != nil || len(credentials) != 2 || credentials["email"] != tc.email || credentials["password"] != tc.password {
							t.Error("login did not send the expected email/password JSON")
						}
						if decoder.Decode(new(any)) != io.EOF || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Authorization") != "" {
							t.Error("login must send one JSON request without a prior session token")
						}
						writeJSON(w, map[string]string{"token": sessionToken})
						return
					}
					if r.Method != http.MethodGet || (r.URL.Path != "/v1/vllm-kubeai/deployments" && r.URL.Path != "/v1/postgres/deployments") {
						t.Errorf("unexpected auth scenario request: %s %s", r.Method, r.URL.Path)
						w.WriteHeader(http.StatusNotFound)
						return
					}
					if auth := r.Header.Get("Authorization"); auth != "Bearer "+sessionToken && auth != "Bearer "+overrideToken {
						w.WriteHeader(http.StatusUnauthorized)
						writeJSON(w, map[string]string{"error": "authentication required"})
						return
					}
					writeJSON(w, map[string]any{"deployments": []any{}, "auth": r.Header.Get("Authorization")})
				}))
				defer server.Close()
				cli := testutils.New(t, testutils.Options{
					Binary: binary, Server: server.URL, Env: tc.env,
					Secrets: []string{tc.password, "unused-env-password", sessionToken, overrideToken}, AfterRun: commandObserver(t),
				})
				login := cli.MustRun(t, tc.input, tc.args...)
				if login.Stdout != "Logged in successfully.\n" || login.Stderr != "" || loginCalls.Load() != 1 {
					t.Fatal("documented login did not return one clean success response")
				}
				sessionPath := filepath.Join(cli.Home(), ".nistro", "sessions.json")
				data, err := os.ReadFile(sessionPath)
				if err != nil {
					t.Fatal(err)
				}
				var sessions map[string]string
				if json.Unmarshal(data, &sessions) != nil || len(sessions) != 1 || sessions[server.URL] != sessionToken {
					t.Fatal("login did not persist its session for the selected server")
				}
				if runtime.GOOS != "windows" {
					for path, permission := range map[string]os.FileMode{sessionPath: 0600, filepath.Dir(sessionPath): 0700} {
						info, err := os.Stat(path)
						if err != nil || info.Mode().Perm() != permission {
							t.Fatal("saved session path does not have owner-only permissions")
						}
					}
				}
				for _, product := range []string{"inference", "postgres"} {
					result := cli.MustRun(t, "", product, "list", "--output", "json")
					object := decodeObject(t, result.Stdout)
					if object["auth"] != "Bearer "+sessionToken || result.Stderr != "" {
						t.Fatal("a new CLI process did not load the saved session")
					}
				}
				cli.SetToken(overrideToken)
				if object := decodeObject(t, cli.MustRun(t, "", "inference", "list", "-o", "json").Stdout); object["auth"] != "Bearer "+overrideToken {
					t.Fatal("NISTRO_TOKEN did not override the saved session")
				}
				logout := cli.MustRun(t, "", "logout")
				if logout.Stdout != "Logged out. Saved session cleared.\nNISTRO_TOKEN is still configured; unset it to stop using that token.\n" || logout.Stderr != "" {
					t.Fatal("logout did not report that the environment token remains configured")
				}
				data, err = os.ReadFile(sessionPath)
				if err != nil {
					t.Fatal("logout did not leave a session file")
				}
				// Unmarshal into a fresh map: decoding {} does not clear existing map keys.
				sessions = nil
				if json.Unmarshal(data, &sessions) != nil || len(sessions) != 0 {
					t.Fatal("logout did not clear the persisted session")
				}
				if object := decodeObject(t, cli.MustRun(t, "", "postgres", "list", "-o", "json").Stdout); object["auth"] != "Bearer "+overrideToken {
					t.Fatal("logout unexpectedly disabled the environment token override")
				}
				cli.SetToken("")
				result := cli.Run("", "inference", "list", "-o", "json")
				if result.ExitCode == 0 || !strings.Contains(result.Stderr, "401") || result.Stdout != "" {
					t.Fatal("clearing NISTRO_TOKEN after logout left the CLI authenticated")
				}
				if loginCalls.Load() != 1 {
					t.Fatal("reading deployments or logging out unexpectedly attempted sign-in")
				}
			})
		}
	})

	t.Run("server flag overrides environment and sessions stay scoped", func(t *testing.T) {
		newServer := func(token string, calls *atomic.Int32) *httptest.Server {
			return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method == http.MethodPost && r.URL.Path == "/v1/auth/signin" {
					var credentials map[string]string
					if json.NewDecoder(r.Body).Decode(&credentials) != nil || len(credentials) != 2 || credentials["email"] != "server-login@example.com" || credentials["password"] != "server-password" || r.Header.Get("Authorization") != "" {
						t.Error("selected server did not receive a clean login request")
					}
					writeJSON(w, map[string]string{"token": token})
					return
				}
				if r.Method != http.MethodGet || r.URL.Path != "/v1/postgres/deployments" {
					t.Errorf("unexpected server configuration request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if r.Header.Get("Authorization") != "Bearer "+token {
					if r.Header.Get("Authorization") != "" {
						t.Error("a session token crossed server boundaries")
					}
					w.WriteHeader(http.StatusUnauthorized)
					writeJSON(w, map[string]string{"error": "authentication required"})
					return
				}
				writeJSON(w, map[string]any{"deployments": []any{}, "server_marker": token})
			}))
		}
		var firstCalls, secondCalls atomic.Int32
		first := newServer("first-readme-server-token", &firstCalls)
		second := newServer("second-readme-server-token", &secondCalls)
		defer first.Close()
		defer second.Close()
		cli := testutils.New(t, testutils.Options{
			Binary: binary, Server: first.URL + "/", Env: map[string]string{"NISTRO_EMAIL": "server-login@example.com", "NISTRO_PASSWORD": "server-password"},
			Secrets: []string{"server-password", "first-readme-server-token", "second-readme-server-token"}, AfterRun: commandObserver(t),
		})
		cli.MustRun(t, "", "login")
		cli.MustRun(t, "", "postgres", "list", "-o", "json")
		firstBefore := firstCalls.Load()
		result := cli.Run("", "--server", second.URL, "postgres", "list", "-o", "json")
		if result.ExitCode == 0 || !strings.Contains(result.Stderr, "401") || firstCalls.Load() != firstBefore || secondCalls.Load() != 1 {
			t.Fatal("--server did not override NISTRO_SERVER without carrying the saved session")
		}
		cli.MustRun(t, "", "--server", second.URL+"/", "login")
		for _, tc := range []struct {
			args   []string
			marker string
		}{
			{[]string{"postgres", "list", "-o", "json"}, "first-readme-server-token"},
			{[]string{"--server", second.URL, "postgres", "list", "-o", "json"}, "second-readme-server-token"},
		} {
			if object := decodeObject(t, cli.MustRun(t, "", tc.args...).Stdout); object["server_marker"] != tc.marker {
				t.Fatal("a new process selected the wrong server or session")
			}
		}
		cli.MustRun(t, "", "--server", second.URL, "logout")
		cli.MustRun(t, "", "postgres", "list", "-o", "json")
		result = cli.Run("", "--server", second.URL, "postgres", "list", "-o", "json")
		if result.ExitCode == 0 || !strings.Contains(result.Stderr, "401") {
			t.Fatal("logout did not clear only the explicitly selected server session")
		}
	})
}
