package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"nistro-ctl/internal/api"
)

func TestLoginSessionLifecycle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	oldRoot, oldLogin := rootCfg, loginCfg
	oldContext := loginCmd.Context()
	loginCmd.SetContext(t.Context())
	t.Cleanup(func() {
		rootCfg, loginCfg = oldRoot, oldLogin
		loginCmd.SetContext(oldContext)
		loginCmd.SetOut(nil)
		logoutCmd.SetOut(nil)
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/signin" {
			var request api.SignInRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			if request.Email != "user@example.com" || request.Password != "password" {
				t.Errorf("unexpected login credentials: email=%q", request.Email)
			}
			w.Write([]byte(`{"token":"session-token"}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer session-token" {
			t.Errorf("saved token was not used")
		}
		w.Write([]byte(`{"deployments":[]}`))
	}))
	defer server.Close()
	rootCfg.server, rootCfg.token = server.URL, ""
	loginCfg.email, loginCfg.password = "user@example.com", "password"
	var output bytes.Buffer
	loginCmd.SetOut(&output)
	logoutCmd.SetOut(&output)
	if err := loginCmd.RunE(loginCmd, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "session-token") {
		t.Fatal("login printed the token")
	}
	path, _ := sessionPath()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("session permissions: %o", info.Mode().Perm())
	}
	if err := rootCmd.PersistentPreRunE(usageCurrentCmd, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := client().Deployments(t.Context()); err != nil {
		t.Fatal(err)
	}
	rootCfg.token = "override"
	if err := rootCmd.PersistentPreRunE(usageCurrentCmd, nil); err != nil {
		t.Fatal(err)
	}
	if rootCfg.token != "override" {
		t.Fatal("saved session replaced an explicit token")
	}
	rootCfg.token = ""
	if err := logoutCmd.RunE(logoutCmd, nil); err != nil {
		t.Fatal(err)
	}
	if token, err := loadSession(server.URL); err != nil || token != "" {
		t.Fatalf("session remains after logout: %v", err)
	}
	if err := logoutCmd.RunE(logoutCmd, nil); err != nil {
		t.Fatalf("repeated logout failed: %v", err)
	}
}

func TestLoginCredentialsWithoutTerminal(t *testing.T) {
	for _, tc := range []struct {
		name, email, password string
		wantError             bool
	}{
		{"provided", "user@example.com", "password", false},
		{"missing both", "", "", true},
		{"missing email", "", "password", true},
		{"missing password", "user@example.com", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command := &cobra.Command{}
			command.SetIn(strings.NewReader(""))
			var output bytes.Buffer
			command.SetErr(&output)
			email, password, err := loginCredentials(command, tc.email, tc.password)
			if (err != nil) != tc.wantError {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tc.wantError && (email != tc.email || password != tc.password) {
				t.Fatal("provided credentials changed")
			}
			if output.Len() != 0 {
				t.Fatal("prompted without a terminal")
			}
		})
	}
}

func TestSessionsAreScopedToServer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := saveSession("https://one.example/", "one"); err != nil {
		t.Fatal(err)
	}
	if err := saveSession("https://two.example", "two"); err != nil {
		t.Fatal(err)
	}
	if err := saveSession("https://one.example", ""); err != nil {
		t.Fatal(err)
	}
	if token, err := loadSession("https://two.example/"); err != nil || token != "two" {
		t.Fatalf("logout affected another server: %v", err)
	}
}
