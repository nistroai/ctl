package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPasswordStdin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("NISTRO_EMAIL", "")
	t.Setenv("NISTRO_PASSWORD", "env-password")
	previous, previousLogin := rootCfg, loginCfg
	previousContext := loginCmd.Context()
	t.Cleanup(func() {
		rootCfg, loginCfg = previous, previousLogin
		loginCmd.SetIn(nil)
		loginCmd.SetOut(nil)
		loginCmd.SetContext(previousContext)
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]string
		json.NewDecoder(r.Body).Decode(&payload)
		if payload["email"] != "user@example.com" || payload["password"] != " password with spaces " {
			t.Error("stdin credentials changed")
		}
		fmt.Fprint(w, `{"token":"stdin-token"}`)
	}))
	defer server.Close()
	rootCfg.server = server.URL
	loginCfg.email, loginCfg.password = "user@example.com", ""
	loginCfg.passwordStdin = true
	loginCmd.SetContext(t.Context())
	loginCmd.SetOut(&bytes.Buffer{})
	loginCmd.SetIn(strings.NewReader(" password with spaces \r\n"))
	if err := loginCmd.RunE(loginCmd, nil); err != nil {
		t.Fatal(err)
	}
	loginCmd.SetIn(strings.NewReader("\n"))
	if err := loginCmd.RunE(loginCmd, nil); err == nil {
		t.Fatal("empty stdin password accepted")
	}
	loginCfg.email = ""
	if err := loginCmd.RunE(loginCmd, nil); err == nil {
		t.Fatal("stdin login without email accepted")
	}
}
