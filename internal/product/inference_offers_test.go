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
	"nistro-ctl/internal/products/vllm"
)

func TestInferenceOffers(t *testing.T) {
	for _, tc := range []struct {
		name, tier, input string
		args              []string
		want              map[string]any
	}{
		{"on-demand flags", "on-demand", "", []string{"--model-url", "org/model", "--max-model-len", "32000", "--location", " any "}, map[string]any{"model": "org/model", "location": "ANY", "pricing_mode": "basic", "dedicated": false}},
		{"on-demand config override", "on-demand", `{"model_url":"https://huggingface.co/org/model","max_model_len":32000,"location":"EU","quantization":"awq"}`, []string{"--config-file", "-", "--location", "us"}, map[string]any{"url": "https://huggingface.co/org/model", "location": "US", "quantization": "awq", "pricing_mode": "basic", "dedicated": false}},
		{"scale default replicas", "scale", "", []string{"--model-url", "org/model", "--max-model-len", "32000", "--cloud", "aws", "--region", "us-east-1", "--instance-type", "g5.xlarge"}, map[string]any{"model": "org/model", "cloud": "aws", "region": "us-east-1", "instance_type": "g5.xlarge", "node_count": float64(1), "pricing_mode": "dedicated", "dedicated": true}},
		{"scale replica override", "scale", `{"model_url":"org/model","max_model_len":32000,"cloud":"aws","region":"us-east-1","instance_type":"g5.xlarge","replicas":2,"gpu_type":"A10G-24GB"}`, []string{"--config-file", "-", "--replicas", "8"}, map[string]any{"model": "org/model", "cloud": "aws", "region": "us-east-1", "instance_type": "g5.xlarge", "node_count": float64(8), "gpu_type": "A10G-24GB", "pricing_mode": "dedicated", "dedicated": true}},
	} {
		for _, format := range []string{"table", "json"} {
			t.Run(tc.name+"/"+format, func(t *testing.T) {
				requests := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests++
					if r.Method != "POST" || r.URL.Path != "/v1/vllm/price-estimates" {
						t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					}
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					for key, want := range tc.want {
						if body[key] != want {
							t.Errorf("%s: got %v, want %v", key, body[key], want)
						}
					}
					if body["context_length"] != float64(32000) {
						t.Errorf("unexpected context length: %v", body)
					}
					for _, key := range []string{"model_url", "max_model_len", "replicas", "min_replicas", "max_replicas"} {
						if _, exists := body[key]; exists {
							t.Errorf("unexpected request field %s", key)
						}
					}
					if tc.tier == "on-demand" && (body["cloud"] != nil || body["region"] != nil || body["instance_type"] != nil || body["gpu_type"] != nil || body["node_count"] != nil) {
						t.Errorf("On-Demand must use automatic hardware: %v", body)
					}
					if tc.tier == "scale" && body["location"] != nil {
						t.Errorf("Scale location must come from placement: %v", body)
					}
					row := map[string]any{"gpu_type": "A10G-24GB", "required_vram_gb": 20, "actual_vram_gb": 24, "tensor_parallel_size": 1, "gpu_memory_utilization": 0.8, "price_per_hour_usd": 0.1727, "billing_model": "hourly"}
					if tc.tier == "scale" {
						row["cloud"], row["region"], row["sku"], row["node_count"] = "aws", "us-east-1", "g5.xlarge", body["node_count"]
					}
					if err := json.NewEncoder(w).Encode(map[string]any{"model_id": "org/model", "deployment_options": []any{row}, "quantization_options": []any{map[string]any{"name": "fp16"}}}); err != nil {
						t.Error(err)
					}
				}))
				defer server.Close()
				command := vllm.New().Command(product.Deps{Client: func() *api.Client { return api.NewClient(server.URL, "") }})
				var out, progress bytes.Buffer
				command.SetArgs(append([]string{"offers", tc.tier, "-o", format}, tc.args...))
				command.SetIn(strings.NewReader(tc.input))
				command.SetOut(&out)
				command.SetErr(&progress)
				if err := command.ExecuteContext(t.Context()); err != nil {
					t.Fatal(err)
				}
				if requests != 1 || !strings.Contains(out.String(), "0.1727") {
					t.Fatalf("missing price or unexpected requests: %d, %s", requests, out.String())
				}
				if format == "json" {
					var response map[string]any
					if err := json.Unmarshal(out.Bytes(), &response); err != nil {
						t.Fatal(err)
					}
					row := response["deployment_options"].([]any)[0].(map[string]any)
					if response["quantization_options"] == nil || response["model_id"] != "org/model" || row["billing_model"] != "hourly" || row["instance_type"] != nil || progress.Len() != 0 {
						t.Fatalf("API response or JSON progress changed: %s", out.String())
					}
				} else {
					lines := strings.Split(strings.TrimSpace(out.String()), "\n")
					want := "GPU_TYPE REQUIRED_VRAM_GB ACTUAL_VRAM_GB TENSOR_PARALLEL_SIZE GPU_MEMORY_UTILIZATION PRICE_PER_HOUR_USD"
					if tc.tier == "scale" {
						want = "CLOUD REGION INSTANCE_TYPE NODE_COUNT " + want
						if !strings.Contains(out.String(), "g5.xlarge") {
							t.Fatal("hardware SKU missing from table")
						}
					}
					if len(lines) != 2 || strings.Join(strings.Fields(lines[0]), " ") != want {
						t.Fatalf("unexpected offer table: %s", out.String())
					}
				}
			})
		}
	}
}

func TestInferenceOffersRejectInvalidInputs(t *testing.T) {
	base := []string{"offers", "on-demand", "--model-url", "org/model", "--max-model-len", "32000", "--location", "US"}
	scale := []string{"offers", "scale", "--model-url", "org/model", "--max-model-len", "32000", "--cloud", "aws", "--region", "us-east-1", "--instance-type", "g5.xlarge"}
	for _, tc := range []struct {
		args  []string
		input string
	}{
		{base[:len(base)-2], ""},
		{append(append([]string{}, base...), "--cloud", "aws"), ""},
		{append(append([]string{}, base...), "--gpu-type", "H100"), ""},
		{[]string{"offers", "on-demand", "--config-file", "-"}, `{"model_url":"org/model","max_model_len":32000,"location":"US","instance_type":"g5.xlarge"}`},
		{scale[:len(scale)-2], ""},
		{append(append([]string{}, scale...), "--location", "US"), ""},
		{append(append([]string{}, scale...), "--replicas", "0"), ""},
		{append(append([]string{}, scale...), "--replicas", "9"), ""},
		{[]string{"offers", "scale", "--config-file", "-"}, `{"model_url":"org/model","max_model_len":32000,"cloud":"aws","region":"us-east-1","instance_type":"g5.xlarge","replicas":1.5}`},
	} {
		t.Run(fmt.Sprint(tc.args)+tc.input, func(t *testing.T) {
			command := vllm.New().Command(product.Deps{Client: func() *api.Client { t.Fatal("invalid input called API"); return nil }})
			command.SetArgs(tc.args)
			command.SetIn(strings.NewReader(tc.input))
			command.SetOut(&bytes.Buffer{})
			command.SetErr(&bytes.Buffer{})
			if err := command.ExecuteContext(t.Context()); err == nil {
				t.Fatal("invalid input accepted")
			}
		})
	}
}
