package integration_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func testDiscoveryTableOutput(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, progress string
		args                         []string
		body                         map[string]any
		response                     map[string]any
		headers, row, hidden         []string
	}{
		{
			name: "inference recommend-hardware scale", method: http.MethodPost, path: "/v1/vllm/hardware-recommendations", progress: "hardware recommendations",
			args: []string{"inference", "recommend-hardware", "scale", "--model-url", "Qwen/Qwen2.5-7B-Instruct", "--max-model-len", "8192", "--location", "EU", "--gpu-type", "A10G"},
			body: map[string]any{"model": "Qwen/Qwen2.5-7B-Instruct", "context_length": float64(8192), "location": "EU", "gpu_type": "A10G", "pricing_mode": "dedicated", "dedicated": true},
			response: map[string]any{"deployment_options": []any{map[string]any{
				"cloud": "aws", "region": "eu-west-1", "sku": "g5.2xlarge", "instance_type": "ignored-instance-type", "gpu_type": "A10G", "price_per_hour_usd": 19.875,
			}}},
			headers: []string{"CLOUD", "REGION", "INSTANCE_TYPE", "GPU_TYPE"},
			row:     []string{"aws", "eu-west-1", "g5.2xlarge", "A10G"},
			hidden:  []string{"SKU", "ignored-instance-type", "PRICE", "19.875"},
		},
		{
			name: "inference offers on-demand", method: http.MethodPost, path: "/v1/vllm/price-estimates", progress: "offers",
			args: []string{"inference", "offers", "on-demand", "--model-url", "Qwen/Qwen2.5-7B-Instruct", "--max-model-len", "8192", "--location", "any", "--quantization", "Auto", "--output", "table"},
			body: map[string]any{"model": "Qwen/Qwen2.5-7B-Instruct", "context_length": float64(8192), "location": "ANY", "pricing_mode": "basic", "dedicated": false},
			response: map[string]any{"deployment_options": []any{map[string]any{
				"gpu_type": "L4", "required_vram_gb": 14, "actual_vram_gb": 24, "tensor_parallel_size": 1, "gpu_memory_utilization": 0.75, "price_per_hour_usd": 0.625,
				"cloud": "hidden-cloud", "region": "hidden-region", "sku": "hidden-sku", "estimated_price_per_month_usd": 999.875,
			}}},
			headers: []string{"GPU_TYPE", "REQUIRED_VRAM_GB", "ACTUAL_VRAM_GB", "TENSOR_PARALLEL_SIZE", "GPU_MEMORY_UTILIZATION", "PRICE_PER_HOUR_USD"},
			row:     []string{"L4", "14", "24", "1", "0.75", "0.625"},
			hidden:  []string{"hidden-cloud", "hidden-region", "hidden-sku", "MONTH", "999.875"},
		},
		{
			name: "inference offers scale", method: http.MethodPost, path: "/v1/vllm/price-estimates", progress: "offers",
			args: []string{"inference", "offers", "scale", "--model-url", "https://huggingface.co/Qwen/Qwen2.5-7B-Instruct", "--max-model-len", "8192", "--cloud", "aws", "--region", "us-east-1", "--instance-type", "g5.2xlarge", "--replicas", "3"},
			body: map[string]any{"url": "https://huggingface.co/Qwen/Qwen2.5-7B-Instruct", "context_length": float64(8192), "cloud": "aws", "region": "us-east-1", "instance_type": "g5.2xlarge", "node_count": float64(3), "pricing_mode": "dedicated", "dedicated": true},
			response: map[string]any{"deployment_options": []any{map[string]any{
				"cloud": "aws", "region": "us-east-1", "sku": "g5.2xlarge", "instance_type": "ignored-instance-type", "node_count": 3,
				"gpu_type": "A10G", "required_vram_gb": 14, "actual_vram_gb": 24, "tensor_parallel_size": 1, "gpu_memory_utilization": 0.8, "price_per_hour_usd": 3.375,
			}}},
			headers: []string{"CLOUD", "REGION", "INSTANCE_TYPE", "NODE_COUNT", "GPU_TYPE", "REQUIRED_VRAM_GB", "ACTUAL_VRAM_GB", "TENSOR_PARALLEL_SIZE", "GPU_MEMORY_UTILIZATION", "PRICE_PER_HOUR_USD"},
			row:     []string{"aws", "us-east-1", "g5.2xlarge", "3", "A10G", "14", "24", "1", "0.8", "3.375"},
			hidden:  []string{"SKU", "ignored-instance-type"},
		},
		{
			name: "postgres placements scale", method: http.MethodGet, path: "/v1/postgres/scale/catalog", progress: "placements",
			args: []string{"postgres", "placements", "scale"},
			response: map[string]any{"clouds": []any{map[string]any{"cloud": "aws", "regions": []any{map[string]any{
				"region": "eu-west-1", "location": "EU", "instance_types": []string{"m7i.large"},
			}}}}},
			headers: []string{"CLOUD", "REGION", "LOCATION", "INSTANCE_TYPE"},
			row:     []string{"aws", "eu-west-1", "EU", "m7i.large"},
		},
		{
			name: "postgres offers on-demand", method: http.MethodPost, path: "/v1/postgres/offers", progress: "offers",
			args: []string{"postgres", "offers", "on-demand", "--vcpu", "2", "--storage-gib", "50", "--location", " eu "},
			body: map[string]any{"vcpu": float64(2), "memory_gib": float64(8), "storage_gib": float64(50), "location": "EU", "pricing_mode": "basic"},
			response: map[string]any{"deployment_options": []any{map[string]any{
				"instance_type": "managed-2", "location": "EU", "vcpu": 2, "memory_gib": 8, "storage_gib": 50, "requested_vcpu": 2, "requested_memory_gib": 8,
				"estimated_price_per_hour_usd": 12.375, "estimated_price_per_month_usd": 991.375, "source": "hidden-offer-source", "database_password": "hidden-database-password", "vllm_api_key": "hidden-api-key",
			}}},
			headers: []string{"INSTANCE_TYPE", "LOCATION", "MEMORY_GIB", "REQUESTED_MEMORY_GIB", "REQUESTED_VCPU", "STORAGE_GIB", "VCPU"},
			row:     []string{"managed-2", "EU", "8", "8", "2", "50", "2"},
			hidden:  []string{"PRICE", "12.375", "991.375", "SOURCE", "hidden-offer-source", "hidden-database-password", "hidden-api-key"},
		},
		{
			name: "postgres offers scale", method: http.MethodPost, path: "/v1/postgres/offers", progress: "offers",
			args: []string{"postgres", "offers", "scale", "--storage-gib", "100", "--cloud", "aws", "--region", "us-east-1", "--instance-type", "m7i.large", "--replicas", "0", "-o", "table"},
			body: map[string]any{"storage_gib": float64(100), "cloud": "aws", "region": "us-east-1", "instance_type": "m7i.large", "replicas": float64(0), "vcpu": float64(1), "memory_gib": float64(1), "pricing_mode": "dedicated"},
			response: map[string]any{"deployment_options": []any{map[string]any{
				"cloud": "aws", "region": "us-east-1", "instance_type": "m7i.large", "replicas": 0, "vcpu": 2, "memory_gib": 8, "storage_gib": 100,
				"requested_vcpu": 99, "requested_memory_gib": 199, "estimated_price_per_hour_usd": 12.375, "estimated_price_per_month_usd": 991.375, "price_per_hour_usd": 89.375,
				"source": "hidden-offer-source", "database_password": "hidden-database-password", "vllm_api_key": "hidden-api-key",
			}}},
			headers: []string{"CLOUD", "ESTIMATED_PRICE_PER_HOUR_USD", "INSTANCE_TYPE", "MEMORY_GIB", "REGION", "REPLICAS", "STORAGE_GIB", "VCPU"},
			row:     []string{"aws", "12.375", "m7i.large", "8", "us-east-1", "0", "100", "2"},
			hidden:  []string{"MONTH", "991.375", "89.375", "REQUESTED_", "SOURCE", "hidden-offer-source", "hidden-database-password", "hidden-api-key"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != tc.method || r.URL.Path != tc.path || r.URL.RawQuery != "" {
					t.Errorf("request = %s %s, want %s %s", r.Method, r.URL.RequestURI(), tc.method, tc.path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if r.Header.Get("Authorization") != "Bearer discovery-table-token" {
					t.Error("discovery request did not include selected authentication")
				}
				if tc.body != nil {
					var body map[string]any
					decoder := json.NewDecoder(r.Body)
					if err := decoder.Decode(&body); err != nil {
						t.Errorf("decode request body: %v", err)
					}
					if !reflect.DeepEqual(body, tc.body) {
						t.Errorf("request body = %#v, want %#v", body, tc.body)
					}
					if err := decoder.Decode(new(any)); err != io.EOF || r.Header.Get("Content-Type") != "application/json" {
						t.Error("request must contain exactly one JSON object with JSON content type")
					}
				} else if r.ContentLength != 0 {
					t.Error("placement GET unexpectedly sent a body")
				}
				writeJSON(w, tc.response)
			}))
			defer server.Close()
			cli := newCLI(t, server.URL)
			cli.SetToken("discovery-table-token")
			result := cli.MustRun(t, "", tc.args...)
			if requests.Load() != 1 {
				t.Fatalf("discovery made %d API requests, want 1", requests.Load())
			}
			lines := strings.Split(strings.TrimSpace(result.Stdout), "\n")
			if len(lines) != 2 || !reflect.DeepEqual(strings.Fields(lines[0]), tc.headers) || !reflect.DeepEqual(strings.Fields(lines[1]), tc.row) {
				t.Fatalf("table output did not contain expected headers and values: %q", result.Stdout)
			}
			for _, value := range tc.hidden {
				if strings.Contains(result.Stdout, value) {
					t.Fatalf("table unexpectedly exposed %q", value)
				}
			}
			if result.Stderr != "Loading "+tc.progress+"...\n✓ Loaded "+tc.progress+"\n" {
				t.Fatalf("progress was not confined to stderr: %q", result.Stderr)
			}
		})
	}
}

func TestDiscoveryArgumentValidation(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	for _, tc := range []struct {
		name, diagnostic string
		args             []string
	}{
		{"recommendation missing model", "model_url is required", []string{"inference", "recommend-hardware", "scale", "--max-model-len", "8192", "--location", "US"}},
		{"recommendation unsupported ANY", "location ANY is available on the On-Demand tier only", []string{"inference", "recommend-hardware", "scale", "--model-url", "model", "--max-model-len", "8192", "--location", "ANY"}},
		{"recommendation zero context", "max_model_len must be an integer from 1 to 2e+06", []string{"inference", "recommend-hardware", "scale", "--model-url", "model", "--max-model-len", "0", "--location", "US"}},
		{"inference on-demand missing location", "location is required", []string{"inference", "offers", "on-demand", "--model-url", "model", "--max-model-len", "8192"}},
		{"inference scale missing instance", "instance_type is required", []string{"inference", "offers", "scale", "--model-url", "model", "--max-model-len", "8192", "--cloud", "aws", "--region", "us-east-1"}},
		{"inference scale zero replicas", "replicas must be an integer from 1 to 8", []string{"inference", "offers", "scale", "--model-url", "model", "--max-model-len", "8192", "--cloud", "aws", "--region", "us-east-1", "--instance-type", "g5.xlarge", "--replicas", "0"}},
		{"postgres on-demand CPU limit", "vcpu must be an integer from 1 to 8", []string{"postgres", "offers", "on-demand", "--vcpu", "9", "--storage-gib", "50", "--location", "US"}},
		{"postgres on-demand fractional storage", "storage_gib must be an integer from 10 to 1024", []string{"postgres", "offers", "on-demand", "--vcpu", "2", "--storage-gib", "10.5", "--location", "US"}},
		{"postgres on-demand unsupported location", "location must be US, EU, ASIA, or ANY", []string{"postgres", "offers", "on-demand", "--vcpu", "2", "--storage-gib", "50", "--location", "AU"}},
		{"postgres scale negative replicas", "replicas must be an integer from 0 to 8", []string{"postgres", "offers", "scale", "--storage-gib", "100", "--cloud", "aws", "--region", "us-east-1", "--replicas", "-1"}},
		{"postgres scale unsupported location flag", "unknown flag: --location", []string{"postgres", "offers", "scale", "--storage-gib", "100", "--cloud", "aws", "--region", "us-east-1", "--location", "US"}},
		{"placements positional argument", `unknown command "unexpected"`, []string{"postgres", "placements", "scale", "unexpected"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := newCLI(t, server.URL).Run("", tc.args...)
			if result.ExitCode == 0 || result.Err == nil || result.Stdout != "" || !strings.Contains(result.Stderr, tc.diagnostic) {
				t.Fatalf("invalid discovery input did not fail clearly: exit=%d stderr=%q", result.ExitCode, result.Stderr)
			}
		})
	}
	if requests.Load() != 0 {
		t.Fatalf("invalid discovery inputs made %d API requests", requests.Load())
	}
}
