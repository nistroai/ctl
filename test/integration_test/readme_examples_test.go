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

type readmeRequest struct {
	method, path string
	body         map[string]any
	response     map[string]any
}

type readmeExampleScenario struct {
	requests       []readmeRequest
	config         string
	outputContains []string
	outputOmits    []string
	jsonOption     map[string]any
}

// args comes from the README itself. Fixtures provide the documented deployment
// ID and inference.json file; command flags are never substituted or generated.
func testREADMEExample(t *testing.T, args []string) {
	t.Helper()
	if len(args) < 3 {
		t.Fatalf("README example has no supported product/action/tier: %v", args)
	}
	scenario := readmeBehaviorFixture(t, strings.Join(args[:3], " "))
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index := requests.Add(1) - 1
		if index >= int32(len(scenario.requests)) {
			t.Errorf("README example made an unexpected request: %s %s", r.Method, r.URL.RequestURI())
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		want := scenario.requests[index]
		if r.Method != want.method || r.URL.Path != want.path || r.URL.RawQuery != "" {
			t.Errorf("README request %d = %s %s, want %s %s", index+1, r.Method, r.URL.RequestURI(), want.method, want.path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.Header.Get("Authorization") != "Bearer readme-example-token" || r.Header.Get("Accept") != "application/json" {
			t.Error("README request omitted selected authentication or JSON accept header")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if want.body != nil {
			var body map[string]any
			decoder := json.NewDecoder(r.Body)
			if err := decoder.Decode(&body); err != nil {
				t.Errorf("decode README request: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if !reflect.DeepEqual(body, want.body) || decoder.Decode(new(any)) != io.EOF || r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("README request body = %#v, want %#v as a single JSON object", body, want.body)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		} else if r.ContentLength != 0 {
			t.Error("README tier/catalog GET unexpectedly included a body")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		writeJSON(w, want.response)
	}))
	defer server.Close()
	cli := newCLI(t, server.URL)
	cli.SetToken("readme-example-token")
	if scenario.config != "" {
		if err := os.WriteFile(filepath.Join(cli.Home(), "inference.json"), []byte(scenario.config), 0600); err != nil {
			t.Fatal(err)
		}
	}
	result := cli.MustRun(t, "", args...)
	if requests.Load() != int32(len(scenario.requests)) {
		t.Fatalf("README example made %d requests, want %d", requests.Load(), len(scenario.requests))
	}
	for _, value := range scenario.outputContains {
		if !strings.Contains(result.Stdout, value) {
			t.Fatalf("README example output omitted %q: %s", value, cli.Redact(result.Stdout))
		}
	}
	for _, value := range scenario.outputOmits {
		if strings.Contains(result.Stdout, value) {
			t.Fatalf("README example output exposed %q", value)
		}
	}
	if invocationFormat(args) == "json" {
		object := decodeObject(t, result.Stdout)
		if result.Stderr != "" {
			t.Fatal("README JSON example included progress output")
		}
		options, ok := object["deployment_options"].([]any)
		if !ok || len(options) != 1 {
			t.Fatal("README JSON example did not preserve one deployment option")
		}
		option, ok := options[0].(map[string]any)
		if !ok {
			t.Fatal("README JSON deployment option is not an object")
		}
		for key, value := range scenario.jsonOption {
			if !reflect.DeepEqual(option[key], value) {
				t.Errorf("README JSON option %s = %#v, want %#v", key, option[key], value)
			}
		}
	}
}

func readmeBehaviorFixture(t *testing.T, command string) readmeExampleScenario {
	t.Helper()
	const inferencePath = "/v1/vllm-kubeai/deployments"
	const postgresPath = "/v1/postgres/deployments"
	const id = "fixture-deployment"
	const model = "Qwen/Qwen2.5-7B-Instruct"
	deployment := func(mode string) map[string]any {
		return map[string]any{"deployment": map[string]any{
			"id": id, "status": "running", "pricing_mode": mode,
			"database_password": "readme-db-password", "vllm_api_key": "readme-api-key",
		}}
	}
	mutation := func(path, mode string, body map[string]any, update bool) readmeExampleScenario {
		response := deployment(mode)
		requests := []readmeRequest{{http.MethodPost, path, body, response}}
		if update {
			requests = []readmeRequest{
				{http.MethodGet, path + "/" + id, nil, response},
				{http.MethodPatch, path + "/" + id, body, response},
			}
		}
		return readmeExampleScenario{
			requests:       requests,
			outputContains: []string{id, "RUNNING"},
			outputOmits:    []string{"readme-db-password", "readme-api-key"},
		}
	}
	inferenceOption := map[string]any{
		"cloud": "aws", "region": "us-east-1", "sku": "g5.xlarge", "gpu_type": "A10G", "node_count": 2,
		"required_vram_gb": 14, "actual_vram_gb": 24, "tensor_parallel_size": 1, "gpu_memory_utilization": 0.8,
		"price_per_hour_usd": 1.25,
	}
	inferenceResponse := map[string]any{"deployment_options": []any{inferenceOption}, "warnings": []string{}}
	postgresOption := map[string]any{
		"cloud": "aws", "region": "us-east-1", "instance_type": "m7i.large", "vcpu": 2, "ram_gib": 8, "storage_gib": 100,
		"replicas": 2, "requested_vcpu": 1, "requested_memory_gib": 1, "source": "readme-offer-source",
		"estimated_price_per_hour_usd": 0.25, "estimated_price_per_month_usd": 180,
	}
	postgresResponse := map[string]any{"deployment_options": []any{postgresOption}, "warnings": []string{}}
	baseInferenceDiscovery := map[string]any{
		"model": model, "context_length": float64(32768), "location": "ANY", "pricing_mode": "basic", "dedicated": false,
	}

	switch command {
	case "inference create on-demand":
		return mutation(inferencePath, "basic", map[string]any{
			"model_url": model, "max_model_len": float64(32768), "location": "ANY", "pricing_mode": "basic",
			"enable_prefix_caching": true, "enable_chunked_prefill": true, "min_replicas": float64(0), "max_replicas": float64(1),
		}, false)
	case "inference create scale":
		scenario := mutation(inferencePath, "dedicated", map[string]any{
			"model_url": model, "max_model_len": float64(32768), "cloud": "aws", "region": "us-east-1", "instance_type": "g5.xlarge",
			"pricing_mode": "dedicated", "min_replicas": float64(2), "max_replicas": float64(2), "enable_prefix_caching": true, "enable_chunked_prefill": true,
		}, false)
		scenario.config = `{"model_url":"Qwen/Qwen2.5-7B-Instruct","max_model_len":32768,"cloud":"aws","region":"us-east-1","instance_type":"g5.xlarge","replicas":2}`
		return scenario
	case "inference update scale":
		return mutation(inferencePath, "dedicated", map[string]any{"min_replicas": float64(2), "max_replicas": float64(2)}, true)
	case "postgres create on-demand":
		return mutation(postgresPath, "basic", map[string]any{
			"vcpu": float64(2), "memory_gib": float64(8), "storage_gib": float64(50), "location": "US",
			"pricing_mode": "basic", "postgres_version": "ghcr.io/cloudnative-pg/postgresql:18",
		}, false)
	case "postgres create scale":
		scenario := mutation(postgresPath, "dedicated", map[string]any{
			"storage_gib": float64(100), "cloud": "aws", "region": "us-east-1", "instance_type": "m7i.large", "replicas": float64(1),
			"vcpu": float64(2), "memory_gib": float64(8), "pricing_mode": "dedicated", "postgres_version": "ghcr.io/cloudnative-pg/postgresql:18",
		}, false)
		scenario.requests = append([]readmeRequest{{http.MethodPost, "/v1/postgres/offers", map[string]any{
			"storage_gib": float64(100), "cloud": "aws", "region": "us-east-1", "instance_type": "m7i.large", "replicas": float64(1),
			"vcpu": 0.5, "memory_gib": float64(2), "pricing_mode": "dedicated", "postgres_version": "ghcr.io/cloudnative-pg/postgresql:18",
		}, postgresResponse}}, scenario.requests...)
		return scenario
	case "postgres update on-demand":
		return mutation(postgresPath, "basic", map[string]any{"vcpu": float64(4), "memory_gib": float64(16), "storage_gib": float64(100)}, true)
	case "postgres update scale":
		return mutation(postgresPath, "dedicated", map[string]any{"replicas": float64(2), "parameters": map[string]any{"work_mem": "32MB"}}, true)
	case "inference recommend-hardware scale":
		return readmeExampleScenario{
			requests: []readmeRequest{{http.MethodPost, "/v1/vllm/hardware-recommendations", map[string]any{
				"model": model, "context_length": float64(32768), "location": "US", "pricing_mode": "dedicated", "dedicated": true,
			}, inferenceResponse}},
			outputContains: []string{"aws", "us-east-1", "g5.xlarge", "A10G"},
			jsonOption:     map[string]any{"cloud": "aws", "region": "us-east-1", "sku": "g5.xlarge", "gpu_type": "A10G"},
		}
	case "inference offers on-demand":
		return readmeExampleScenario{
			requests:       []readmeRequest{{http.MethodPost, "/v1/vllm/price-estimates", baseInferenceDiscovery, inferenceResponse}},
			outputContains: []string{"GPU_TYPE", "PRICE_PER_HOUR_USD", "A10G", "1.25"},
			outputOmits:    []string{"CLOUD", "REGION", "g5.xlarge"},
		}
	case "inference offers scale":
		return readmeExampleScenario{
			requests: []readmeRequest{{http.MethodPost, "/v1/vllm/price-estimates", map[string]any{
				"model": model, "context_length": float64(32768), "cloud": "aws", "region": "us-east-1", "instance_type": "g5.xlarge",
				"pricing_mode": "dedicated", "dedicated": true, "node_count": float64(2),
			}, inferenceResponse}},
			outputContains: []string{"g5.xlarge", "1.25"},
			jsonOption:     map[string]any{"sku": "g5.xlarge", "node_count": json.Number("2"), "price_per_hour_usd": json.Number("1.25")},
		}
	case "postgres placements scale":
		return readmeExampleScenario{
			requests: []readmeRequest{{http.MethodGet, "/v1/postgres/scale/catalog", nil, map[string]any{"clouds": []any{map[string]any{
				"cloud": "aws", "regions": []any{map[string]any{"region": "us-east-1", "location": "US", "instance_types": []string{"m7i.large"}}},
			}}}}},
			outputContains: []string{"CLOUD", "REGION", "LOCATION", "INSTANCE_TYPE", "aws", "us-east-1", "US", "m7i.large"},
		}
	case "postgres offers on-demand":
		// The Base offer's location and requested compute come from this README example.
		response := map[string]any{"deployment_options": []any{map[string]any{
			"instance_type": "managed-2", "location": "ANY", "vcpu": 2, "ram_gib": 8, "storage_gib": 50,
			"requested_vcpu": 2, "requested_memory_gib": 8, "source": "readme-offer-source",
			"estimated_price_per_hour_usd": 0.125, "estimated_price_per_month_usd": 90,
		}}}
		return readmeExampleScenario{
			requests: []readmeRequest{{http.MethodPost, "/v1/postgres/offers", map[string]any{
				"vcpu": float64(2), "memory_gib": float64(8), "storage_gib": float64(50), "location": "ANY", "pricing_mode": "basic",
			}, response}},
			outputContains: []string{"INSTANCE_TYPE", "LOCATION", "VCPU", "STORAGE_GIB", "managed-2", "ANY", "50"},
			outputOmits:    []string{"PRICE", "SOURCE", "readme-offer-source", "0.125"},
		}
	case "postgres offers scale":
		return readmeExampleScenario{
			requests: []readmeRequest{{http.MethodPost, "/v1/postgres/offers", map[string]any{
				"storage_gib": float64(100), "cloud": "aws", "region": "us-east-1", "replicas": float64(2),
				"pricing_mode": "dedicated", "vcpu": float64(1), "memory_gib": float64(1),
			}, postgresResponse}},
			outputContains: []string{"m7i.large", "us-east-1", "0.25"},
			outputOmits:    []string{"estimated_price_per_month_usd"},
			jsonOption: map[string]any{
				"cloud": "aws", "region": "us-east-1", "instance_type": "m7i.large", "replicas": json.Number("2"),
				"estimated_price_per_hour_usd": json.Number("0.25"), "vcpu": json.Number("2"), "ram_gib": json.Number("8"),
			},
		}
	default:
		t.Fatalf("README command %q needs a behavior fixture", command)
		return readmeExampleScenario{}
	}
}
