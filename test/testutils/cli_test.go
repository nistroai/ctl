package testutils

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func helperCLI(t *testing.T, options Options) *CLI {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	options.Binary = binary
	return New(t, options)
}

// The test executable serves as a portable child process, including on Windows.
func TestCLIHelperProcess(t *testing.T) {
	index := -1
	for i, arg := range os.Args {
		if arg == "--" {
			index = i + 1
			break
		}
	}
	if index < 0 || index >= len(os.Args) {
		return
	}
	switch os.Args[index] {
	case "environment":
		input, _ := io.ReadAll(os.Stdin)
		state := map[string]any{"input": string(input), "args": os.Args[index+1:]}
		for _, name := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "NISTRO_SERVER", "NISTRO_TOKEN", "NISTRO_EMAIL", "NISTRO_PASSWORD", "NISTRO_TEST_BINARY", "CLI_TEST_PASSTHROUGH"} {
			state[name] = os.Getenv(name)
		}
		_ = json.NewEncoder(os.Stdout).Encode(state)
	case "exit":
		fmt.Fprint(os.Stdout, "stdout-only")
		fmt.Fprint(os.Stderr, "stderr-only")
		os.Exit(7)
	case "timeout":
		time.Sleep(10 * time.Second)
	default:
		os.Exit(9)
	}
	os.Exit(0)
}

func TestCLIIsolatesCredentialsAndPassesArgumentsLiterally(t *testing.T) {
	for name, value := range map[string]string{
		"NISTRO_TOKEN": "parent-token", "NISTRO_EMAIL": "parent-email", "NISTRO_PASSWORD": "parent-password",
		"NISTRO_SERVER": "https://parent.example", "NISTRO_TEST_BINARY": "parent-binary", "CLI_TEST_PASSTHROUGH": "preserved",
	} {
		t.Setenv(name, value)
	}
	c := helperCLI(t, Options{Server: "https://selected.example", Token: "selected-token"})
	argument := "$(this-must-remain-literal); with spaces"
	result := c.MustRun(t, "stdin-password", "-test.run=^TestCLIHelperProcess$", "--", "environment", argument)
	var state struct {
		Home        string   `json:"HOME"`
		UserProfile string   `json:"USERPROFILE"`
		Config      string   `json:"XDG_CONFIG_HOME"`
		Server      string   `json:"NISTRO_SERVER"`
		Token       string   `json:"NISTRO_TOKEN"`
		Email       string   `json:"NISTRO_EMAIL"`
		Password    string   `json:"NISTRO_PASSWORD"`
		TestBinary  string   `json:"NISTRO_TEST_BINARY"`
		Passthrough string   `json:"CLI_TEST_PASSTHROUGH"`
		Input       string   `json:"input"`
		Args        []string `json:"args"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &state); err != nil {
		t.Fatalf("decode child environment: %v", err)
	}
	if state.Home != c.Home() || state.UserProfile != c.Home() || state.Config != filepath.Join(c.Home(), ".config") {
		t.Fatal("child did not receive isolated home/config paths")
	}
	if state.Server != "https://selected.example" || state.Token != "selected-token" {
		t.Fatal("child did not receive selected server/token")
	}
	if state.Email != "" || state.Password != "" || state.TestBinary != "" {
		t.Fatal("parent Nistro configuration leaked into child environment")
	}
	if state.Passthrough != "preserved" || state.Input != "stdin-password" || len(state.Args) != 1 || state.Args[0] != argument {
		t.Fatal("child environment, stdin, or literal arguments were not preserved")
	}
	c.SetToken("")
	result = c.MustRun(t, "", "-test.run=^TestCLIHelperProcess$", "--", "environment")
	if err := json.Unmarshal([]byte(result.Stdout), &state); err != nil {
		t.Fatal(err)
	}
	if state.Token != "" {
		t.Fatal("empty token override did not clear the child environment")
	}
}

func TestCLIPreservesExitCodeAndSeparateStreams(t *testing.T) {
	c := helperCLI(t, Options{})
	result := c.Run("", "-test.run=^TestCLIHelperProcess$", "--", "exit")
	if result.Err == nil || result.ExitCode != 7 || result.Stdout != "stdout-only" || result.Stderr != "stderr-only" {
		t.Fatal("runner did not preserve the failed process result")
	}
}

func TestCLITimeoutTerminatesChild(t *testing.T) {
	c := helperCLI(t, Options{Timeout: 100 * time.Millisecond})
	start := time.Now()
	result := c.Run("", "-test.run=^TestCLIHelperProcess$", "--", "timeout")
	if !errors.Is(result.Err, context.DeadlineExceeded) || result.ExitCode == 0 {
		t.Fatal("hanging process did not report a command timeout")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("command timeout did not stop the child promptly")
	}
}

func TestCLIRedactsKnownAndPersistedCredentials(t *testing.T) {
	c := helperCLI(t, Options{Token: "known-token", Secrets: []string{"overlap", "overlap-long-secret", "quote\"password"}})
	sessions := filepath.Join(c.Home(), ".nistro", "sessions.json")
	if err := os.MkdirAll(filepath.Dir(sessions), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sessions, []byte(`{"https://selected.example":"saved-session-token"}`), 0600); err != nil {
		t.Fatal(err)
	}
	text := `known-token overlap-long-secret saved-session-token quote\"password {"api_key":"unknown-api-key","nested":{"access_token":"unknown-token"},"password":"unknown-password"}`
	redacted := c.Redact(text)
	for _, secret := range []string{"known-token", "overlap-long-secret", "saved-session-token", `quote\"password`, "unknown-api-key", "unknown-token", "unknown-password"} {
		if strings.Contains(redacted, secret) {
			t.Fatal("credential remained in redacted diagnostics")
		}
	}
	if err := os.Remove(sessions); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(c.Redact("saved-session-token"), "saved-session-token") {
		t.Fatal("session credential was forgotten after logout removed the session file")
	}
}

func TestPrepareBinaryRejectsInvalidExplicitBinary(t *testing.T) {
	t.Setenv("NISTRO_TEST_BINARY", filepath.Join(t.TempDir(), "does-not-exist"))
	_, cleanup, err := PrepareBinary()
	defer cleanup()
	if err == nil {
		t.Fatal("invalid explicit binary must not silently build a replacement")
	}
}

func TestRepositoryRootFindsCheckoutAboveNestedModules(t *testing.T) {
	repository := t.TempDir()
	if err := os.WriteFile(filepath.Join(repository, "go.mod"), []byte("module nistro-ctl\n"), 0600); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(repository, "test", "nested")
	if err := os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "go.mod"), []byte("module unrelated-module\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if root := findRepositoryRoot(nested); root != repository {
		t.Fatal("repository lookup selected a nested unrelated module")
	}
}

func TestRepositoryRootRejectsUnrelatedCheckout(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "go.mod"), []byte("module unrelated-module\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if root := findRepositoryRoot(directory); root != "" {
		t.Fatal("repository lookup accepted an unrelated module")
	}
}
