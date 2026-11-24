package integration_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

// README discovery commands accept JSON files and stdin. These cases exercise
// both input routes while proving that explicit flags override file values.
func testREADMEDiscoveryConfig(t *testing.T) {
	for _, tc := range []struct {
		command, path, config string
		overrides             []string
		body                  map[string]any
	}{
		{
			command: "inference recommend-hardware scale", path: "/v1/vllm/hardware-recommendations",
			config:    `{"model_url":"Qwen/Qwen2.5-7B-Instruct","max_model_len":4096,"location":" eu ","quantization":"Auto","gpu_type":"Any"}`,
			overrides: []string{"--max-model-len", "32768", "--location", "us"},
			body: map[string]any{
				"model": "Qwen/Qwen2.5-7B-Instruct", "context_length": float64(32768),
				"location": "US", "pricing_mode": "dedicated", "dedicated": true,
			},
		},
		{
			command: "inference offers on-demand", path: "/v1/vllm/price-estimates",
			config:    `{"model_url":"https://huggingface.co/Qwen/Qwen2.5-7B-Instruct","max_model_len":4096,"location":"EU","quantization":"awq"}`,
			overrides: []string{"--max-model-len", "32768", "--location", " any ", "--quantization", "Auto"},
			body: map[string]any{
				"url": "https://huggingface.co/Qwen/Qwen2.5-7B-Instruct", "context_length": float64(32768),
				"location": "ANY", "pricing_mode": "basic", "dedicated": false,
			},
		},
		{
			command: "inference offers scale", path: "/v1/vllm/price-estimates",
			config:    `{"model_url":"Qwen/Qwen2.5-7B-Instruct","max_model_len":4096,"cloud":"aws","region":"us-east-1","instance_type":"g5.xlarge","replicas":1,"quantization":"Auto","gpu_type":"Any"}`,
			overrides: []string{"--max-model-len", "32768", "--replicas", "2"},
			body: map[string]any{
				"model": "Qwen/Qwen2.5-7B-Instruct", "context_length": float64(32768), "cloud": "aws",
				"region": "us-east-1", "instance_type": "g5.xlarge", "node_count": float64(2),
				"pricing_mode": "dedicated", "dedicated": true,
			},
		},
		{
			command: "postgres offers on-demand", path: "/v1/postgres/offers",
			config:    `{"vcpu":1,"storage_gib":50,"location":"EU"}`,
			overrides: []string{"--vcpu", "2", "--location", " any "},
			body: map[string]any{
				"vcpu": float64(2), "memory_gib": float64(8), "storage_gib": float64(50),
				"location": "ANY", "pricing_mode": "basic",
			},
		},
		{
			command: "postgres offers scale", path: "/v1/postgres/offers",
			config:    `{"storage_gib":50,"cloud":"aws","region":"us-east-1","replicas":1}`,
			overrides: []string{"--storage-gib", "100", "--replicas", "2"},
			body: map[string]any{
				"storage_gib": float64(100), "cloud": "aws", "region": "us-east-1", "replicas": float64(2),
				"vcpu": float64(1), "memory_gib": float64(1), "pricing_mode": "dedicated",
			},
		},
	} {
		for _, inputRoute := range []string{"file", "stdin"} {
			t.Run(tc.command+"/"+inputRoute, func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if r.Method != http.MethodPost || r.URL.Path != tc.path || r.URL.RawQuery != "" {
						t.Errorf("request = %s %s, want POST %s", r.Method, r.URL.RequestURI(), tc.path)
						w.WriteHeader(http.StatusNotFound)
						return
					}
					if r.Header.Get("Authorization") != "Bearer readme-config-token" || r.Header.Get("Accept") != "application/json" || r.Header.Get("Content-Type") != "application/json" {
						t.Error("discovery request must use the selected token and JSON headers")
					}
					var body map[string]any
					decoder := json.NewDecoder(r.Body)
					if err := decoder.Decode(&body); err != nil {
						t.Errorf("decode request body: %v", err)
					}
					if !reflect.DeepEqual(body, tc.body) {
						t.Errorf("request body = %#v, want %#v", body, tc.body)
					}
					if err := decoder.Decode(new(any)); err != io.EOF {
						t.Error("request must contain exactly one JSON object")
					}
					writeJSON(w, map[string]any{
						"deployment_options": []any{map[string]any{"sku": "fixture-instance", "price_per_hour_usd": 0.5, "estimated_price_per_hour_usd": 0.5, "estimated_price_per_month_usd": 365}},
						"metadata":           map[string]any{"source": "readme-config-fixture"},
					})
				}))
				defer server.Close()
				cli := newCLI(t, server.URL)
				cli.SetToken("readme-config-token")
				configPath, input := "-", tc.config
				if inputRoute == "file" {
					configPath = filepath.Join(t.TempDir(), "discovery.json")
					if err := os.WriteFile(configPath, []byte(tc.config), 0600); err != nil {
						t.Fatal(err)
					}
					input = ""
				}
				args := append(strings.Fields(tc.command), "--config-file", configPath)
				args = append(args, tc.overrides...)
				args = append(args, "-o", "json")
				result := cli.MustRun(t, input, args...)
				response := decodeObject(t, result.Stdout)
				metadata, ok := response["metadata"].(map[string]any)
				if !ok || metadata["source"] != "readme-config-fixture" {
					t.Fatal("discovery JSON did not preserve API metadata")
				}
				if result.Stderr != "" || requests.Load() != 1 {
					t.Fatal("discovery must return clean JSON after exactly one API request")
				}
				if strings.HasPrefix(tc.command, "postgres ") && strings.Contains(result.Stdout, "estimated_price_per_month_usd") {
					t.Fatal("Postgres offer JSON must omit the monthly estimate")
				}
			})
		}
	}
}
