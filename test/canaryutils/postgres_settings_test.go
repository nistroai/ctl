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
		{"real rounding tolerance", PostgresSetting{"1.0000000001", "", "real"}, "1", true},
		{"real mismatch", PostgresSetting{"1.01", "", "real"}, "1", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := PostgresSettingMatches(test.setting, test.requested); got != test.matches {
				t.Fatalf("snapshot match = %v, want %v", got, test.matches)
			}
		})
	}
}

func TestPostgresIntegerSettingsRequireExactValues(t *testing.T) {
	for _, test := range []struct {
		name, actual, unit, requested string
		matches                       bool
	}{
		{"adjacent vacuum failsafe ages", "1600000000", "", "1600000001", false},
		{"adjacent large millisecond values", "1600000000", "ms", "1600000001ms", false},
		{"integers beyond float precision", "9007199254740992", "", "9007199254740993", false},
		{"large equivalent byte units", "17179869184GB", "GB", "18446744073709551616B", true},
		{"large normalized bytes differ by one", "17179869184GB", "GB", "18446744073709551617B", false},
		{"large equivalent time units", "1600000000", "ms", "1600000s", true},
		{"fractional unit normalization", "1500", "ms", "1.5s", true},
		{"fractional memory units", "1572864kB", "kB", "1.5GB", true},
		{"subsecond unit normalization", "1", "ms", "1000us", true},
		{"equivalent block units", "2147483647", "8kB", "17179869176kB", true},
		{"adjacent block values", "2147483647", "8kB", "17179869177kB", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			setting := PostgresSetting{Value: test.actual, Unit: test.unit, Kind: "integer"}
			if got := PostgresSettingMatches(setting, test.requested); got != test.matches {
				t.Fatalf("integer snapshot match = %v, want %v", got, test.matches)
			}
		})
	}
}

func TestPostgresSettingsScientificNotation(t *testing.T) {
	for _, test := range []struct {
		name, actual, unit, kind, requested string
		matches                             bool
	}{
		{"reported negative exponent", "1e-06", "", "real", "0.000001", true},
		{"requested negative exponent", "0.000001", "", "real", "1e-06", true},
		{"uppercase exponent", "1E-06", "", "real", "0.000001", true},
		{"positive exponent", "1.25e+2", "", "real", "125", true},
		{"unsigned exponent", "1.25e2", "", "real", "125", true},
		{"signed fractional mantissa", "-.5e-2", "", "real", "-0.005", true},
		{"trailing decimal point", "+1.e-6", "", "real", "0.000001", true},
		{"explicit time unit", "1e-3s", "ms", "real", "1ms", true},
		{"scientific real mismatch", "1e-06", "", "real", "0.000002", false},
		{"exact integer beyond float precision", "9.007199254740993e15", "", "integer", "9007199254740993", true},
		{"adjacent integer remains different", "9.007199254740993e15", "", "integer", "9007199254740992", false},
		{"missing exponent digits", "1e", "", "real", "1", false},
		{"missing signed exponent digits", "1e-", "", "real", "1", false},
		{"repeated exponent", "1e-6e2", "", "real", "0.0001", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			setting := PostgresSetting{Value: test.actual, Unit: test.unit, Kind: test.kind}
			if got := PostgresSettingMatches(setting, test.requested); got != test.matches {
				t.Fatalf("scientific snapshot match = %v, want %v", got, test.matches)
			}
		})
	}
}

func TestPostgresRealSettingsRelativeTolerance(t *testing.T) {
	for _, test := range []struct {
		name, actual, unit, requested string
		matches                       bool
	}{
		{"distinct tiny values", "1e-10", "", "2e-10", false},
		{"zero differs from tiny value", "0", "", "1e-10", false},
		{"tiny value differs from zero", "1e-10", "", "0", false},
		{"equivalent zero representations", "0.000", "", "0e-10", true},
		{"tiny relative rounding", "1.0000000001e-10", "", "1e-10", true},
		{"tiny rounding exceeds tolerance", "1.000000002e-10", "", "1e-10", false},
		{"negative tiny relative rounding", "-1.0000000001e-10", "", "-1e-10", true},
		{"distinct negative tiny values", "-1e-10", "", "-2e-10", false},
		{"equivalent tiny time units", "1e-7", "ms", "1e-10s", true},
		{"distinct normalized tiny values", "1e-7", "ms", "2e-10s", false},
		{"zero differs below float range", "0", "", "1e-400", false},
		{"distinct values below float range", "1e-400", "", "2e-400", false},
		{"relative rounding below float range", "1.0000000001e-400", "", "1e-400", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			setting := PostgresSetting{Value: test.actual, Unit: test.unit, Kind: "real"}
			if got := PostgresSettingMatches(setting, test.requested); got != test.matches {
				t.Fatalf("relative snapshot match = %v, want %v", got, test.matches)
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
