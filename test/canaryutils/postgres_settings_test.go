package canaryutils

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func TestPostgresSettingMatches(t *testing.T) {
	for _, test := range []struct {
		name      string
		setting   PostgresSetting
		requested string
		matches   bool
	}{
		{"equivalent memory units", PostgresSetting{"32MB", "kB", "integer"}, "32768kB", true},
		{"bare value uses server unit", PostgresSetting{"32768", "kB", "integer"}, "32MB", true},
		{"block units", PostgresSetting{"16", "8kB", "integer"}, "128kB", true},
		{"equivalent time units", PostgresSetting{"1s", "ms", "integer"}, "1000ms", true},
		{"boolean alias", PostgresSetting{"on", "", "bool"}, "true", true},
		{"enum boolean alias", PostgresSetting{"on", "", "enum"}, "yes", true},
		{"enum case", PostgresSetting{"local", "", "enum"}, "LOCAL", true},
		{"changed value", PostgresSetting{"32MB", "kB", "integer"}, "16MB", false},
		{"different quantity categories", PostgresSetting{"8kB", "kB", "integer"}, "8ms", false},
		{"invalid quantity", PostgresSetting{"32MB", "kB", "integer"}, "invalid", false},
		{"string is not a boolean", PostgresSetting{"on", "", "string"}, "true", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := PostgresSettingMatches(test.setting, test.requested); got != test.matches {
				t.Fatalf("snapshot match = %v, want %v", got, test.matches)
			}
		})
	}
}

func TestReadPostgresSettingsEmptyDoesNotConnect(t *testing.T) {
	settings, err := ReadPostgresSettings(t.Context(), "invalid connection", nil)
	if err != nil || settings == nil || len(settings) != 0 {
		t.Fatal("an empty snapshot request should return an empty map without connecting")
	}
	if err := CheckPostgresSettings(t.Context(), "invalid connection", nil); err != nil {
		t.Fatal("checking no settings should not require a connection")
	}
}

func TestReadPostgresSettingsDeadlineAndSafeErrors(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		connection, err := listener.Accept()
		if err == nil {
			defer connection.Close()
			<-done
		}
	}()
	uri := "postgres://test:fake-db-secret@" + listener.Addr().String() + "/test?sslmode=verify-full"
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	settings, err := ReadPostgresSettings(ctx, uri, []string{"private-setting-name"})
	if settings != nil || !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatalf("settings snapshot ignored its deadline or returned partial results: %v", err)
	}
	for _, secret := range []string{uri, "fake-db-secret", "private-setting-name"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatal("settings snapshot diagnostic exposed credentials or the setting name")
		}
	}
}
