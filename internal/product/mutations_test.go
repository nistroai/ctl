package product_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nistro-ctl/internal/api"
	"nistro-ctl/internal/product"
	"nistro-ctl/internal/products/postgres"
	"nistro-ctl/internal/products/vllm"
)

func TestOnDemandCommands(t *testing.T) {
	for _, tc := range []struct {
		prod   product.CLIProduct
		groups []string
	}{
		{vllm.New(), []string{"create"}},
		{postgres.New(), []string{"create", "update", "offers"}},
	} {
		for _, group := range tc.groups {
			t.Run(tc.prod.Name()+"/"+group, func(t *testing.T) {
				command := tc.prod.Command(product.Deps{})
				found, _, err := command.Find([]string{group})
				if err != nil || found.Name() != group {
					t.Fatalf("missing command group %s: %v", group, err)
				}
				onDemand := false
				for _, child := range found.Commands() {
					if child.Name() == "base" || child.HasAlias("base") {
						t.Fatal("command still exposes the old tier name")
					}
					if child.Name() == "on-demand" {
						onDemand = true
						if !strings.Contains(child.Short, "On-Demand") {
							t.Fatalf("help is missing the tier label: %s", child.Short)
						}
					}
				}
				if !onDemand {
					t.Fatal("missing on-demand subcommand")
				}
			})
		}
	}
}

func TestMutationsAndDiscovery(t *testing.T) {
	for _, tc := range []struct {
		name                string
		prod                product.CLIProduct
		args                []string
		input, path, method string
		want                map[string]any
	}{
		{"inference on-demand", vllm.New(), []string{"create", "on-demand", "--model-url", "org/model", "--max-model-len", "32768", "--location", "US"}, "", "/v1/vllm-kubeai/deployments", "POST", map[string]any{"pricing_mode": "basic", "max_replicas": float64(1)}},
		{"inference on-demand ANY", vllm.New(), []string{"create", "on-demand", "--model-url", "org/model", "--max-model-len", "32768", "--location", " any "}, "", "/v1/vllm-kubeai/deployments", "POST", map[string]any{"location": "ANY", "pricing_mode": "basic"}},
		{"inference scale omitted location", vllm.New(), []string{"create", "scale", "--model-url", "org/model", "--max-model-len", "32768", "--cloud", "nebius", "--region", "eu-north1", "--instance-type", "gpu", "--replicas", "1"}, "", "/v1/vllm-kubeai/deployments", "POST", map[string]any{"location": nil, "region": "eu-north1", "pricing_mode": "dedicated"}},
		{"inference scale GPU type", vllm.New(), []string{"create", "scale", "--model-url", "org/model", "--max-model-len", "32768", "--cloud", "nebius", "--region", "eu-north1", "--instance-type", "gpu", "--gpu-type", "H100-80GB", "--replicas", "1"}, "", "/v1/vllm-kubeai/deployments", "POST", map[string]any{"gpu_type": "H100-80GB", "pricing_mode": "dedicated"}},
		{"inference scale file override", vllm.New(), []string{"create", "scale", "--config-file", "-", "--replicas", "2"}, `{"model_url":"org/model","max_model_len":32768,"cloud":"aws","region":"us-east-1","instance_type":"gpu","replicas":1}`, "/v1/vllm-kubeai/deployments", "POST", map[string]any{"min_replicas": float64(2), "max_replicas": float64(2)}},
		{"inference update", vllm.New(), []string{"update", "scale", "dep-1", "--replicas", "3"}, "", "/v1/vllm-kubeai/deployments/dep-1", "PATCH", map[string]any{"min_replicas": float64(3), "max_replicas": float64(3)}},
		{"postgres on-demand", postgres.New(), []string{"create", "on-demand", "--vcpu", "2", "--storage-gib", "50", "--location", "US"}, "", "/v1/postgres/deployments", "POST", map[string]any{"memory_gib": float64(8), "pricing_mode": "basic"}},
		{"postgres on-demand config ANY", postgres.New(), []string{"create", "on-demand", "--config-file", "-"}, `{"vcpu":2,"storage_gib":50,"location":" Any "}`, "/v1/postgres/deployments", "POST", map[string]any{"location": "ANY", "pricing_mode": "basic"}},
		{"postgres scale omitted location", postgres.New(), []string{"create", "scale", "--cloud", "aws", "--region", "eu-west-1", "--instance-type", "m7i.large", "--storage-gib", "50"}, "", "/v1/postgres/deployments", "POST", map[string]any{"location": nil, "region": "eu-west-1", "pricing_mode": "dedicated"}},
		{"postgres scale default standby", postgres.New(), []string{"create", "scale", "--cloud", "aws", "--region", "us-east-1", "--instance-type", "m7i.large", "--storage-gib", "50"}, "", "/v1/postgres/deployments", "POST", map[string]any{"replicas": float64(1)}},
		{"postgres scale config default standby", postgres.New(), []string{"create", "scale", "--config-file", "-"}, `{"cloud":"aws","region":"us-east-1","instance_type":"m7i.large","storage_gib":50}`, "/v1/postgres/deployments", "POST", map[string]any{"replicas": float64(1)}},
		{"postgres scale config zero standby", postgres.New(), []string{"create", "scale", "--config-file", "-"}, `{"cloud":"aws","region":"us-east-1","instance_type":"m7i.large","storage_gib":50,"replicas":0}`, "/v1/postgres/deployments", "POST", map[string]any{"replicas": float64(0)}},
		{"postgres scale zero standby", postgres.New(), []string{"create", "scale", "--cloud", "aws", "--region", "us-east-1", "--instance-type", "m7i.large", "--storage-gib", "50", "--replicas", "0"}, "", "/v1/postgres/deployments", "POST", map[string]any{"memory_gib": float64(8), "vcpu": float64(2), "replicas": float64(0)}},
		{"postgres on-demand resize", postgres.New(), []string{"update", "on-demand", "dep-1", "--vcpu", "0.5"}, "", "/v1/postgres/deployments/dep-1", "PATCH", map[string]any{"memory_gib": float64(2)}},
		{"postgres parameters file", postgres.New(), []string{"update", "scale", "dep-1", "--config-file", "-", "--parameters", "work_mem=32MB", "--enable-superuser=false"}, `{"parameters":{"work_mem":"16MB"}}`, "/v1/postgres/deployments/dep-1", "PATCH", map[string]any{"enable_superuser": false}},
		{"recommendation", vllm.New(), []string{"recommend-hardware", "scale", "--model-url", "org/model", "--max-model-len", "32768", "--cloud", "nebius", "--region", "eu-north1"}, "", "/v1/recommendations", "POST", map[string]any{"context_length": float64(32768), "dedicated": true}},
		{"recommendation omitted location", vllm.New(), []string{"recommend-hardware", "scale", "--model-url", "org/model", "--max-model-len", "32768", "--cloud", "nebius", "--region", "eu-north1"}, "", "/v1/recommendations", "POST", map[string]any{"location": nil, "cloud": "nebius", "region": "eu-north1", "dedicated": true}},
		{"postgres offers ANY", postgres.New(), []string{"offers", "on-demand", "--vcpu", "2", "--storage-gib", "50", "--location", " any "}, "", "/v1/postgres/offers", "POST", map[string]any{"location": "ANY", "pricing_mode": "basic"}},
		{"postgres offers", postgres.New(), []string{"offers", "scale", "--storage-gib", "50", "--cloud", "aws", "--region", "us-east-1", "--replicas", "2"}, "", "/v1/postgres/offers", "POST", map[string]any{"vcpu": float64(1), "memory_gib": float64(1), "pricing_mode": "dedicated", "cloud": "aws", "region": "us-east-1", "replicas": float64(2)}},
		{"postgres offers zero standby", postgres.New(), []string{"offers", "scale", "--storage-gib", "50", "--cloud", "aws", "--region", "us-east-1", "--replicas", "0"}, "", "/v1/postgres/offers", "POST", map[string]any{"vcpu": float64(1), "memory_gib": float64(1), "pricing_mode": "dedicated", "cloud": "aws", "region": "us-east-1", "replicas": float64(0)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/dep-1") {
					mode := "dedicated"
					if tc.name == "postgres on-demand resize" {
						mode = "basic"
					}
					fmt.Fprintf(w, `{"id":"dep-1","pricing_mode":%q}`, mode)
					return
				}
				if strings.HasPrefix(tc.name, "postgres scale ") && r.URL.Path == "/v1/postgres/offers" {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if expected, ok := tc.want["location"]; ok && body["location"] != expected {
						t.Errorf("offer location: got %v, want %v", body["location"], expected)
					}
					json.NewEncoder(w).Encode(map[string]any{"deployment_options": []any{map[string]any{"cloud": body["cloud"], "region": body["region"], "instance_type": body["instance_type"], "vcpu": 2, "ram_gib": 8}}})
					return
				}
				calls++
				if r.Method != tc.method || r.URL.Path != tc.path {
					t.Errorf("request: %s %s", r.Method, r.URL.Path)
				}
				if tc.method == "GET" {
					if r.URL.Query().Get("url") != "https://huggingface.co/org/model" || r.URL.Query().Get("context_length") != "32768" {
						t.Error("quantization query mismatch")
					}
				} else {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					for key, value := range tc.want {
						if body[key] != value {
							t.Errorf("%s: got %v, want %v", key, body[key], value)
						}
					}
					if params, ok := body["parameters"].(map[string]any); ok && params["work_mem"] != "32MB" {
						t.Error("parameter override mismatch")
					}
				}
				fmt.Fprint(w, `{"ok":true,"estimated_price_per_hour_usd":0.3054,"estimated_price_per_month_usd":222.94}`)
			}))
			defer server.Close()
			command := tc.prod.Command(product.Deps{Client: func() *api.Client { return api.NewClient(server.URL, "") }})
			var out, progress bytes.Buffer
			command.SetOut(&out)
			command.SetErr(&progress)
			command.SetIn(strings.NewReader(tc.input))
			command.SetArgs(append(tc.args, "-o", "json"))
			if err := command.ExecuteContext(t.Context()); err != nil {
				t.Fatal(err)
			}
			if command.Name() == "postgres" && len(tc.args) > 0 && (tc.args[0] == "create" || tc.args[0] == "update" || tc.args[0] == "offers") {
				if strings.Contains(out.String(), "estimated_price_per_month_usd") || !strings.Contains(out.String(), "0.3054") {
					t.Fatalf("unexpected Postgres pricing output: %s", out.String())
				}
			}
			if calls != 1 {
				t.Fatalf("mutation/discovery calls: %d", calls)
			}
			if progress.Len() != 0 || !json.Valid(out.Bytes()) {
				t.Fatalf("unclean JSON response: %s %s", out.String(), progress.String())
			}
		})
	}
}

func TestInvalidMutationInputsDoNotCallAPI(t *testing.T) {
	for _, tc := range []struct {
		prod  product.CLIProduct
		args  []string
		input string
	}{
		{vllm.New(), []string{"create", "on-demand"}, ""},
		{vllm.New(), []string{"create", "on-demand", "--gpu-type", "H100"}, ""},
		{vllm.New(), []string{"create", "on-demand", "--config-file", "-"}, `{"model_url":"org/model","max_model_len":32768,"location":"ANY","gpu_type":"Any"}`},
		{vllm.New(), []string{"create", "on-demand", "--config-file", "-"}, `{"model_url":"org/model","max_model_len":32768,"location":"US","gpu_type":"H100"}`},
		{vllm.New(), []string{"create", "on-demand", "--config-file", "-"}, `{"model_url":"org/model","max_model_len":32768,"location":"ANY","instance_type":"gpu"}`},
		{postgres.New(), []string{"create", "on-demand", "--config-file", "-"}, `{"vcpu":2,"storage_gib":50,"location":"ANY","instance_type":"t4g.2xlarge"}`},
		{postgres.New(), []string{"offers", "on-demand", "--config-file", "-"}, `{"vcpu":2,"storage_gib":50,"location":"US","instance_type":"t4g.2xlarge"}`},
		{vllm.New(), []string{"create", "scale", "--location", "US"}, ""},
		{postgres.New(), []string{"create", "scale", "--location", "EU"}, ""},
		{vllm.New(), []string{"create", "scale", "--config-file", "-"}, `{"model_url":"org/model","max_model_len":32768,"location":"","cloud":"nebius","region":"eu-north1","instance_type":"gpu","replicas":1}`},
		{postgres.New(), []string{"create", "scale", "--config-file", "-"}, `{"storage_gib":50,"location":"US","cloud":"aws","region":"us-east-1","instance_type":"m7i.large"}`},
		{vllm.New(), []string{"recommend-hardware", "scale", "--config-file", "-"}, `{"model_url":"org/model","max_model_len":32768,"location":"US","cloud":"nebius","region":"eu-north1"}`},
		{vllm.New(), []string{"recommend-hardware", "scale", "--model-url", "org/model", "--max-model-len", "32768", "--cloud", "nebius"}, ""},
		{vllm.New(), []string{"recommend-hardware", "scale", "--model-url", "org/model", "--max-model-len", "32768", "--region", "eu-north1"}, ""},
		{vllm.New(), []string{"create", "scale", "--config-file", "-"}, `{"model_url":"org/model","max_model_len":32768,"location":" any ","cloud":"aws","region":"us-east-1","instance_type":"gpu","replicas":1}`},
		{vllm.New(), []string{"recommend-hardware", "scale", "--model-url", "org/model", "--max-model-len", "32768"}, ""},
		{vllm.New(), []string{"recommend-hardware", "scale", "--model-url", "org/model", "--max-model-len", "32768", "--location", "ANY"}, ""},
		{postgres.New(), []string{"create", "scale", "--config-file", "-"}, `{"storage_gib":50,"location":"ANY","cloud":"aws","region":"us-east-1","instance_type":"m7i.large"}`},
		{postgres.New(), []string{"offers", "on-demand", "--vcpu", "2", "--storage-gib", "50", "--location", ""}, ""},
		{postgres.New(), []string{"offers", "on-demand", "--config-file", "-"}, `{"vcpu":2,"storage_gib":50,"location":"ANY","cloud":"aws"}`},
		{vllm.New(), []string{"create", "scale", "--config-file", "-"}, `{"model_url":"org/model","max_model_len":32768,"cloud":"aws","region":"us-east-1","instance_type":"gpu","replicas":0}`},
		{postgres.New(), []string{"update", "scale", "dep-1", "--config-file", "-"}, `{"parameters":{"work_mem":42}}`},
		{postgres.New(), []string{"create", "on-demand", "--config-file", "-"}, `{"vcpu":"2","storage_gib":50,"location":"US"}`},
		{postgres.New(), []string{"create", "on-demand", "--config-file", "-"}, `{"vcpu":2,"storage_gib":50,"location":"US","unsupported":true}`},
		{postgres.New(), []string{"create", "on-demand", "--config-file", "-"}, `{} {}`},
		{postgres.New(), []string{"update", "on-demand", "dep-1"}, ""},
	} {
		command := tc.prod.Command(product.Deps{Client: func() *api.Client { t.Fatal("invalid input called API"); return nil }})
		command.SetIn(strings.NewReader(tc.input))
		command.SetOut(&bytes.Buffer{})
		command.SetErr(&bytes.Buffer{})
		command.SetArgs(tc.args)
		if err := command.ExecuteContext(t.Context()); err == nil {
			t.Fatalf("invalid inputs accepted: %v", tc.args)
		}
	}
}
