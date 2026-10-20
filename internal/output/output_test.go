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
		{`{"deployment":{"id":"dep-1","database_password":"secret"}}`, "table", "dep-1", "DATABASE_PASSWORD"},
		{`{"id":"dep-1","api_key":"secret"}`, "json", "secret", ""},
		{`{"connection":{"password":"secret","apiKey":"secret"}}`, "table", "redacted", "secret"},
		{`{"value":9007199254740993}`, "json", "9007199254740993", ""},
		{`{"id":"dep-1","can_write":true,"min_replicas":1,"max_replicas":1}`, "table", "REPLICAS", "can_write"},
		{`{"min_replicas":1,"max_replicas":1}`, "table", "REPLICAS", "min_replicas"},
		{`{"min_replicas":0,"max_replicas":1}`, "table", "0–1", "max_replicas"},
		{`{"can_write":true,"min_replicas":1,"max_replicas":1}`, "json", "min_replicas", ""},
		{`{"pricing_mode":"basic","enable_superuser":false,"nodes":[{"id":"node-1"}]}`, "table", "BASE", "nodes"},
		{`{"pricing_mode":"basic","enable_superuser":false}`, "table", "BASE", "enable_superuser"},
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

func TestHiddenTableFields(t *testing.T) {
	record := `{"id":"dep-1","status_message":"starting deployment","cluster_status":"RUNNING","ready_instances":2,"current_primary":"node-1","dedicated":true,"database_password":"secret","vllm_api_key":"vllm-secret","omnistrate_instance_id":"instance-1","endpoint_url":"https://inference.example","reader_endpoint_url":"https://reader.example","writer_endpoint_url":"https://writer.example","estimated_price_per_hour_usd":0.31,"estimated_price_per_month_usd":226.30,"estimated_price_per_year_usd":2715.60}`
	for _, raw := range []string{record, `{"deployment":` + record + `}`, `[` + record + `]`, `{"details":` + record + `}`} {
		var b bytes.Buffer
		if err := Print(&b, []byte(raw), "table"); err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"status_message", "cluster_status", "ready_instances", "current_primary", "dedicated", "database_password", "vllm_api_key", "omnistrate_instance_id", "endpoint_url", "reader_endpoint_url", "writer_endpoint_url", "estimated_price_per_month_usd", "estimated_price_per_year_usd", "starting deployment", "RUNNING", "node-1", "secret", "instance-1", "https://inference.example", "https://reader.example", "https://writer.example", "226.3", "2715.6"} {
			if strings.Contains(strings.ToLower(b.String()), field) {
				t.Fatalf("table includes hidden field or value %q: %s", field, b.String())
			}
		}
		if !strings.Contains(b.String(), "dep-1") {
			t.Fatalf("table lost deployment ID: %s", b.String())
		}
		b.Reset()
		if err := Print(&b, []byte(raw), "json"); err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"status_message", "cluster_status", "ready_instances", "current_primary", "dedicated", "database_password", "vllm_api_key", "omnistrate_instance_id", "endpoint_url", "reader_endpoint_url", "writer_endpoint_url", "estimated_price_per_hour_usd", "estimated_price_per_month_usd", "estimated_price_per_year_usd"} {
			if !strings.Contains(b.String(), field) {
				t.Fatalf("JSON lost field %q: %s", field, b.String())
			}
		}
	}

	rows := []map[string]any{{"id": "dep-1", "status_message": "starting deployment", "cluster_status": "RUNNING", "ready_instances": 2, "current_primary": "node-1", "dedicated": true, "database_password": "secret", "vllm_api_key": "vllm-secret", "omnistrate_instance_id": "instance-1", "endpoint_url": "https://inference.example", "reader_endpoint_url": "https://reader.example", "writer_endpoint_url": "https://writer.example", "estimated_price_per_hour_usd": 0.31, "estimated_price_per_month_usd": 226.30}}
	for _, columns := range [][]string{nil, {"id", "status_message", "cluster_status", "ready_instances", "current_primary", "dedicated", "database_password", "vllm_api_key", "omnistrate_instance_id", "endpoint_url", "reader_endpoint_url", "writer_endpoint_url", "estimated_price_per_hour_usd", "estimated_price_per_month_usd"}} {
		var b bytes.Buffer
		if err := Table(&b, rows, columns); err != nil {
			t.Fatal(err)
		}
		got := strings.Fields(b.String())
		if len(got) != 4 || !strings.Contains(b.String(), "ESTIMATED_PRICE_PER_HOUR_USD") || !strings.Contains(b.String(), "ID") || !strings.Contains(b.String(), "0.31") || !strings.Contains(b.String(), "dep-1") {
			t.Fatalf("unexpected direct table output: %s", b.String())
		}
	}
}

func TestTableStatusUsesUppercase(t *testing.T) {
	var b bytes.Buffer
	if err := Print(&b, []byte(`{"deployments":[{"id":"creating","status":"creating"},{"id":"running","status":"RUNNING"}]}`), "table"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), " creating") || !strings.Contains(b.String(), "CREATING") || !strings.Contains(b.String(), "RUNNING") {
		t.Fatalf("unexpected status casing: %s", b.String())
	}

	b.Reset()
	if err := Print(&b, []byte(`{"status":"creating"}`), "json"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), `"creating"`) {
		t.Fatalf("JSON status must be preserved: %s", b.String())
	}
}

func TestDeploymentListOmitsPriceInformation(t *testing.T) {
	raw := []byte(`{"deployments":[{"id":"dep-1","status":"RUNNING","estimated_price_per_hour_usd":0.31,"estimated_price_per_month_usd":226.30}]}`)
	var b bytes.Buffer
	if err := Print(&b, raw, "table"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "PRICE") || strings.Contains(b.String(), "0.31") || strings.Contains(b.String(), "226.3") {
		t.Fatalf("deployment list includes price information: %s", b.String())
	}
	if !strings.Contains(b.String(), "dep-1") || !strings.Contains(b.String(), "RUNNING") {
		t.Fatalf("deployment list lost summary fields: %s", b.String())
	}

	b.Reset()
	if err := Print(&b, raw, "json"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "estimated_price_per_hour_usd") || !strings.Contains(b.String(), "estimated_price_per_month_usd") {
		t.Fatalf("JSON price information must be preserved: %s", b.String())
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
		for i, tier := range []string{"BASE", "SCALE", "ENTERPRISE"} {
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

func TestPostgresBaseListPreservesReplicas(t *testing.T) {
	raw := []byte(`{"deployments":[{"id":"pg-base-zero","pricing_mode":"basic","location":"US","storage_gib":50,"replicas":0},{"id":"pg-base-two","pricing_mode":"basic","location":"US","storage_gib":50,"replicas":2}]}`)
	var b bytes.Buffer
	if err := Print(&b, raw, "table"); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	if len(lines) != 3 || strings.Join(strings.Fields(lines[0]), " ") != "ID CLOUD REGION TIER STORAGE_GIB REPLICAS" || strings.Join(strings.Fields(lines[1]), " ") != "pg-base-zero Nistro Managed US BASE 50 0" || strings.Join(strings.Fields(lines[2]), " ") != "pg-base-two Nistro Managed US BASE 50 2" {
		t.Fatalf("unexpected Postgres Base list output: %s", b.String())
	}
}

func TestInferenceBaseListShowsManagedPlacement(t *testing.T) {
	raw := []byte(`{"deployments":[{"id":"inference-base","model_name":"model","dedicated":false,"location":"Europe"}]}`)
	var b bytes.Buffer
	if err := Print(&b, raw, "table"); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	if len(lines) != 2 || strings.Join(strings.Fields(lines[0]), " ") != "ID MODEL_NAME CLOUD REGION TIER" || strings.Join(strings.Fields(lines[1]), " ") != "inference-base model Nistro Managed Europe BASE" {
		t.Fatalf("unexpected Inference Base list output: %s", b.String())
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
