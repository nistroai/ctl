package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestOutput(t *testing.T) {
	for _, tc := range []struct{ raw, format, want, absent string }{
		{`{"deployments":[]}`, "table", "No results.", ""},
		{`{"deployments":[{"id":"dep-1","status":"running"}]}`, "table", "dep-1", ""},
		{`{"deployment":{"id":"dep-1","database_password":"secret"}}`, "table", "[redacted", "secret"},
		{`{"id":"dep-1","api_key":"secret"}`, "json", "secret", ""},
		{`{"connection":{"password":"secret","apiKey":"secret"}}`, "table", "redacted", "secret"},
		{`{"value":9007199254740993}`, "json", "9007199254740993", ""},
		{`{"id":"dep-1","can_write":true,"min_replicas":1,"max_replicas":1}`, "table", "REPLICAS", "can_write"},
		{`{"min_replicas":1,"max_replicas":1}`, "table", "REPLICAS", "min_replicas"},
		{`{"min_replicas":0,"max_replicas":1}`, "table", "0–1", "max_replicas"},
		{`{"can_write":true,"min_replicas":1,"max_replicas":1}`, "json", "min_replicas", ""},
		{`{"pricing_mode":"basic","enable_superuser":false,"nodes":[{"id":"node-1"}]}`, "table", "base", "nodes"},
		{`{"pricing_mode":"basic","enable_superuser":false}`, "table", "base", "enable_superuser"},
		{`{"pricing_mode":"dedicated","enable_superuser":true}`, "table", "ENABLE_SUPERUSER", ""},
		{`{"name":"malicious\u001b[31m\nvalue"}`, "table", "malicious", "\x1b"},
	} {
		var b bytes.Buffer
		if err := Print(&b, []byte(tc.raw), tc.format); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(b.String(), tc.want) || (tc.absent != "" && strings.Contains(b.String(), tc.absent)) {
			t.Errorf("unexpected output %s", b.String())
		}
	}
}

func TestDeploymentListTiers(t *testing.T) {
	for _, raw := range []string{
		`{"deployments":[{"id":"pg-base","pricing_mode":"basic"},{"id":"pg-scale","pricing_mode":"dedicated"},{"id":"pg-enterprise","pricing_mode":"enterprise"}]}`,
		`{"deployments":[{"id":"inference-base","dedicated":false},{"id":"inference-scale","dedicated":true},{"id":"inference-enterprise","pricing_mode":"enterprise","dedicated":true}]}`,
	} {
		var b bytes.Buffer
		if err := Print(&b, []byte(raw), "table"); err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(b.String()), "\n")
		if len(lines) != 4 || !strings.Contains(lines[0], "TIER") || strings.Contains(lines[0], "PRICING_MODE") {
			t.Fatalf("unexpected tier column: %s", b.String())
		}
		for i, tier := range []string{"base", "scale", "enterprise"} {
			if fields := strings.Fields(lines[i+1]); fields[len(fields)-1] != tier {
				t.Fatalf("expected tier %s: %s", tier, lines[i+1])
			}
		}
		b.Reset()
		if err := Print(&b, []byte(raw), "json"); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(b.String(), `"tier"`) {
			t.Fatalf("JSON must preserve API fields: %s", b.String())
		}
	}
}

func TestHorizontalDetailsAndGenericLists(t *testing.T) {
	for _, raw := range []string{
		`{"capacity":50,"cpu":2}`,
		`[{"capacity":50,"cpu":2},{"capacity":100,"cpu":4}]`,
	} {
		var b bytes.Buffer
		if err := Print(&b, []byte(raw), "table"); err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(b.String()), "\n")
		if strings.Join(strings.Fields(lines[0]), " ") != "CAPACITY CPU" {
			t.Fatalf("unexpected headers: %s", b.String())
		}
		if strings.Join(strings.Fields(lines[1]), " ") != "50 2" {
			t.Fatalf("expected horizontal record: %s", b.String())
		}
	}
}

func TestPostgresTierWithoutPricingMode(t *testing.T) {
	for _, tc := range []struct {
		raw, want string
	}{
		{`{"vcpu":2,"memory_gib":8,"storage_gib":50}`, "base"},
		{`{"vcpu":2,"memory_gib":8,"storage_gib":50,"cloud":"aws","region":"us-west-2","instance_type":"m7i.large"}`, ""},
		{`{"pricing_mode":"enterprise","vcpu":2,"memory_gib":8,"storage_gib":50}`, "enterprise"},
		{`{"pricing_mode":"unknown","vcpu":2,"memory_gib":8,"storage_gib":50}`, ""},
		{`{"pricing_mode":" Base "}`, "base"},
		{`{"id":"dep-1"}`, ""},
	} {
		var v map[string]any
		if err := json.Unmarshal([]byte(tc.raw), &v); err != nil {
			t.Fatal(err)
		}
		if got := DeploymentTier(v); got != tc.want {
			t.Errorf("tier for %s = %q, want %q", tc.raw, got, tc.want)
		}
	}
}
