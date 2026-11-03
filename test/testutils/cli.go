// Package testutils runs the CLI as a separate process with an isolated session.
package testutils

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// Options selects the executable and credentials used by one isolated CLI.
type Options struct {
	Binary  string
	Server  string
	Token   string
	Timeout time.Duration
	Secrets []string
	// Env supplies explicitly controlled Nistro environment settings for tests.
	// Server and Token remain selected through their dedicated options.
	Env map[string]string
	// AfterRun lets a suite track which commands actually executed. The callback
	// receives raw streams; apply Redact before putting them in diagnostics.
	AfterRun func(args []string, result Result)
}

// Result preserves both process streams for assertions. Never log them without
// redacting credentials first.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Err      error
}

// CLI owns a temporary home directory shared by its successive invocations.
// Each invocation starts a new process, so login persistence is tested through
// the same session file that an installed CLI uses.
type CLI struct {
	binary   string
	server   string
	home     string
	env      []string
	timeout  time.Duration
	mu       sync.Mutex
	token    string
	secrets  []string
	afterRun func([]string, Result)
}

func New(t *testing.T, opts Options) *CLI {
	t.Helper()
	binary, err := resolveBinary(opts.Binary)
	if err != nil {
		t.Fatalf("resolve CLI binary: %v", err)
	}
	if opts.Timeout == 0 {
		opts.Timeout = 45 * time.Second
	}
	if opts.Timeout < 0 {
		t.Fatal("CLI command timeout must be positive")
	}
	c := &CLI{
		binary:   binary,
		server:   opts.Server,
		home:     t.TempDir(),
		timeout:  opts.Timeout,
		token:    opts.Token,
		secrets:  append(append([]string(nil), opts.Secrets...), opts.Token),
		afterRun: opts.AfterRun,
	}
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		name = strings.ToUpper(name)
		if strings.HasPrefix(name, "NISTRO_") {
			continue
		}
		switch name {
		case "HOME", "USERPROFILE", "HOMEDRIVE", "HOMEPATH", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "NO_COLOR", "TERM":
			continue
		}
		c.env = append(c.env, entry)
	}
	c.env = append(c.env,
		"HOME="+c.home,
		"USERPROFILE="+c.home,
		"XDG_CONFIG_HOME="+filepath.Join(c.home, ".config"),
		"XDG_CACHE_HOME="+filepath.Join(c.home, ".cache"),
		"XDG_DATA_HOME="+filepath.Join(c.home, ".local", "share"),
		"NO_COLOR=1", "TERM=dumb",
	)
	if runtime.GOOS == "windows" {
		volume := filepath.VolumeName(c.home)
		c.env = append(c.env, "HOMEDRIVE="+volume, "HOMEPATH="+strings.TrimPrefix(c.home, volume))
	}
	keys := make([]string, 0, len(opts.Env))
	for key := range opts.Env {
		if !strings.HasPrefix(key, "NISTRO_") || key == "NISTRO_SERVER" || key == "NISTRO_TOKEN" || strings.ContainsAny(key, "=\x00") {
			t.Fatalf("unsupported test environment key %q; use Server/Token options for those overrides", key)
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		c.env = append(c.env, key+"="+opts.Env[key])
		c.secrets = append(c.secrets, opts.Env[key])
	}
	return c
}

func (c *CLI) Home() string { return c.home }

// SetToken changes the explicit environment override for future invocations.
// Passing an empty token makes the CLI load its saved session instead.
func (c *CLI) SetToken(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = token
	c.secrets = append(c.secrets, token)
}

// Run executes arguments directly, without a shell, and never logs its streams.
// An empty input supplies EOF, preventing accidental interactive login prompts.
func (c *CLI) Run(input string, args ...string) Result {
	c.rememberSessionSecrets()
	c.mu.Lock()
	token := c.token
	if input != "" {
		c.secrets = append(c.secrets, input, strings.TrimRight(input, "\r\n"))
	}
	c.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	command := exec.CommandContext(ctx, c.binary, args...)
	command.Dir = c.home
	command.Env = append(append([]string(nil), c.env...), "NISTRO_SERVER="+c.server, "NISTRO_TOKEN="+token)
	command.Stdin = strings.NewReader(input)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	code := -1
	if command.ProcessState != nil {
		code = command.ProcessState.ExitCode()
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	c.rememberSessionSecrets()
	result := Result{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: code, Err: err}
	if c.afterRun != nil {
		c.afterRun(append([]string(nil), args...), result)
	}
	return result
}

func (c *CLI) MustRun(t *testing.T, input string, args ...string) Result {
	t.Helper()
	result := c.Run(input, args...)
	if result.Err != nil || result.ExitCode != 0 {
		t.Fatalf("nistro %s failed (exit %d): %v\nstderr: %s", c.Redact(strings.Join(args, " ")), result.ExitCode, result.Err, c.Redact(result.Stderr))
	}
	return result
}

var credentialField = regexp.MustCompile(`(?i)("[^"\n]*(?:password|token|api_key|apikey|secret|authorization)[^"\n]*"\s*:\s*)("(?:\\.|[^"\\])*"|[^,}\]\n]+)`)

// Redact removes known secrets and credential fields from diagnostic text. It
// also reads tokens saved by a previous login, without exposing session files.
func (c *CLI) Redact(value string) string {
	c.rememberSessionSecrets()
	c.mu.Lock()
	secrets := append([]string(nil), c.secrets...)
	c.mu.Unlock()
	value = credentialField.ReplaceAllString(value, `${1}"[REDACTED]"`)
	var variants []string
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		variants = append(variants, secret, url.QueryEscape(secret))
		encoded, _ := json.Marshal(secret)
		variants = append(variants, string(encoded[1:len(encoded)-1]))
	}
	sort.Slice(variants, func(i, j int) bool { return len(variants[i]) > len(variants[j]) })
	for _, secret := range variants {
		value = strings.ReplaceAll(value, secret, "[REDACTED]")
	}
	return value
}

func (c *CLI) rememberSessionSecrets() {
	data, err := os.ReadFile(filepath.Join(c.home, ".nistro", "sessions.json"))
	if err != nil {
		return
	}
	var sessions map[string]string
	if json.Unmarshal(data, &sessions) != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, token := range sessions {
		c.secrets = append(c.secrets, token)
	}
}

func resolveBinary(binary string) (string, error) {
	if strings.TrimSpace(binary) == "" {
		return "", errors.New("a CLI binary is required")
	}
	path, err := exec.LookPath(binary)
	if err != nil {
		return "", fmt.Errorf("%q: %w", binary, err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%q is not a regular executable file", path)
	}
	return path, nil
}

// RepositoryRoot locates this checkout through the compiled source path or,
// when -trimpath removes that path, by walking up from the working directory.
// The module declaration prevents selecting an unrelated enclosing repository.
func RepositoryRoot() (string, error) {
	_, source, _, _ := runtime.Caller(0)
	if filepath.IsAbs(source) {
		if root := findRepositoryRoot(filepath.Dir(source)); root != "" {
			return root, nil
		}
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("locate repository working directory: %w", err)
	}
	if root := findRepositoryRoot(workingDirectory); root != "" {
		return root, nil
	}
	return "", errors.New("cannot locate nistro-ctl repository; run tests from its checkout")
}

func findRepositoryRoot(directory string) string {
	for directory = filepath.Clean(directory); ; directory = filepath.Dir(directory) {
		if data, err := os.ReadFile(filepath.Join(directory, "go.mod")); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				fields := strings.Fields(line)
				if len(fields) >= 2 && fields[0] == "module" && strings.Trim(fields[1], `"`) == "nistro-ctl" {
					return directory
				}
			}
		}
		if filepath.Dir(directory) == directory {
			return ""
		}
	}
}

// PrepareBinary selects NISTRO_TEST_BINARY or builds the repository's CLI once.
// TestMain should invoke the returned cleanup after m.Run. Live smoke tests
// should require an installed binary rather than calling this build fallback.
func PrepareBinary() (path string, cleanup func(), err error) {
	noop := func() {}
	if binary := strings.TrimSpace(os.Getenv("NISTRO_TEST_BINARY")); binary != "" {
		path, err = resolveBinary(binary)
		return path, noop, err
	}
	repository, err := RepositoryRoot()
	if err != nil {
		return "", noop, err
	}
	directory, err := os.MkdirTemp("", "nistro-test-binary-*")
	if err != nil {
		return "", noop, err
	}
	cleanup = func() { _ = os.RemoveAll(directory) }
	path = filepath.Join(directory, "nistro")
	if runtime.GOOS == "windows" {
		path += ".exe"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "build", "-o", path, ".")
	command.Dir = repository
	output, err := command.CombinedOutput()
	if err != nil {
		cleanup()
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return "", noop, fmt.Errorf("build CLI binary: %w\n%s", err, output)
	}
	return path, cleanup, nil
}
