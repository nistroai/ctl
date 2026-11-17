package cmd

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nistro-ctl/internal/api"
)

func TestHTTPTimeoutConfiguration(t *testing.T) {
	previous := rootCfg
	t.Cleanup(func() { rootCfg = previous })
	for _, test := range []struct {
		value string
		want  time.Duration
	}{
		{value: "", want: api.DefaultHTTPTimeout},
		{value: " 2m ", want: 2 * time.Minute},
		{value: "50ms", want: 50 * time.Millisecond},
	} {
		t.Run(test.value, func(t *testing.T) {
			t.Setenv("NISTRO_HTTP_TIMEOUT", test.value)
			if err := rootCmd.PersistentPreRunE(loginCmd, nil); err != nil {
				t.Fatal(err)
			}
			if got := client().RequestTimeout(); got != test.want {
				t.Fatalf("HTTP timeout=%s, want %s", got, test.want)
			}
		})
	}
	for _, value := range []string{"0", "0s", "-1m", "120", "invalid", "999999999999999h"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("NISTRO_HTTP_TIMEOUT", value)
			if err := rootCmd.PersistentPreRunE(loginCmd, nil); err == nil || !strings.Contains(err.Error(), "NISTRO_HTTP_TIMEOUT") {
				t.Fatalf("invalid timeout was not rejected before login: %v", err)
			}
		})
	}
}

func TestConfiguredHTTPTimeoutReachesClient(t *testing.T) {
	previous := rootCfg
	t.Cleanup(func() { rootCfg = previous })
	t.Setenv("NISTRO_HTTP_TIMEOUT", "20ms")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	rootCfg.server, rootCfg.token = server.URL, "private-token"
	if err := rootCmd.PersistentPreRunE(loginCmd, nil); err != nil {
		t.Fatal(err)
	}
	_, err := client().Deployments(t.Context())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("configured timeout did not bound the request: %v", err)
	}
}
