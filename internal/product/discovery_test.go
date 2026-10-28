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

func TestPostgresOnDemandOfferVCPU(t *testing.T) {
	for _, value := range []string{"1", "2", "8", "0", "9", "-1", "0.5", "2.5"} {
		for _, inputMethod := range []string{"flags", "config"} {
			t.Run(value+"/"+inputMethod, func(t *testing.T) {
				valid := value == "1" || value == "2" || value == "8"
				requests := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests++
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if r.URL.Path != "/v1/postgres/offers" || r.Method != "POST" {
						t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					}
					cpu, _ := body["vcpu"].(float64)
					if fmt.Sprint(cpu) != value || body["memory_gib"] != cpu*4 {
						t.Errorf("unexpected offer capacity: %v", body)
					}
					if body["pricing_mode"] != "basic" {
						t.Errorf("unexpected On-Demand API pricing mode: %v", body["pricing_mode"])
					}
					fmt.Fprint(w, `{"deployment_options":[]}`)
				}))
				defer server.Close()
				command := postgres.New().Command(product.Deps{Client: func() *api.Client { return api.NewClient(server.URL, "") }})
				args := []string{"offers", "on-demand", "--vcpu", value, "--storage-gib", "50", "--location", "US"}
				if inputMethod == "config" {
					args = []string{"offers", "on-demand", "--config-file", "-"}
					command.SetIn(strings.NewReader(fmt.Sprintf(`{"vcpu":%s,"storage_gib":50,"location":"US"}`, value)))
				}
				command.SetArgs(args)
				command.SetOut(&bytes.Buffer{})
				command.SetErr(&bytes.Buffer{})
				err := command.ExecuteContext(t.Context())
				if (err == nil) != valid {
					t.Fatalf("valid=%v, error=%v", valid, err)
				}
				if (!valid && requests != 0) || (valid && requests != 1) {
					t.Fatalf("unexpected API request count: %d", requests)
				}
			})
		}
	}
}

func TestDiscoveryRejectsFieldsOutsideWebsiteForms(t *testing.T) {
	for _, tc := range []struct {
		prod  product.CLIProduct
		args  []string
		input string
	}{
		{vllm.New(), []string{"quantizations", "--quantization", "awq"}, ""},
		{vllm.New(), []string{"quantizations", "--gpu-type", "H100"}, ""},
		{vllm.New(), []string{"recommend-hardware", "scale", "--cloud", "aws"}, ""},
		{vllm.New(), []string{"recommend-hardware", "scale", "--replicas", "2"}, ""},
		{vllm.New(), []string{"recommend-hardware", "scale", "--config-file", "-"}, `{"model_url":"org/model","max_model_len":32768,"region":"us-east-1"}`},
		{postgres.New(), []string{"offers", "on-demand", "--cloud", "aws"}, ""},
		{postgres.New(), []string{"offers", "on-demand", "--memory-gib", "8"}, ""},
		{postgres.New(), []string{"offers", "scale", "--vcpu", "2"}, ""},
		{postgres.New(), []string{"offers", "scale", "--location", "US"}, ""},
		{postgres.New(), []string{"offers", "scale", "--storage-gib", "50"}, ""},
		{postgres.New(), []string{"offers", "scale", "--storage-gib", "50", "--cloud", "aws"}, ""},
		{postgres.New(), []string{"offers", "scale", "--storage-gib", "50", "--region", "us-east-1"}, ""},
		{postgres.New(), []string{"offers", "scale", "--config-file", "-"}, `{"storage_gib":50,"cloud":"aws","region":"us-east-1","replicas":9}`},
	} {
		cmd := tc.prod.Command(product.Deps{Client: func() *api.Client { t.Fatal("invalid field called API"); return nil }})
		cmd.SetArgs(tc.args)
		cmd.SetIn(strings.NewReader(tc.input))
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		if err := cmd.ExecuteContext(t.Context()); err == nil {
			t.Fatalf("unsupported field accepted: %v", tc.args)
		}
	}
}

func TestPostgresPlacements(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/v1/postgres/scale/catalog" {
			t.Errorf("request: %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{"clouds":[{"cloud":"aws","regions":[{"region":"us-east-1","location":"US","instance_types":["m7i.large"]}]}]}`)
	}))
	defer server.Close()
	for _, format := range []string{"table", "json"} {
		cmd := postgres.New().Command(product.Deps{Client: func() *api.Client { return api.NewClient(server.URL, "") }})
		var out, errOut bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&errOut)
		cmd.SetArgs([]string{"placements", "scale", "-o", format})
		if err := cmd.ExecuteContext(t.Context()); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "m7i.large") {
			t.Fatal("placement instance missing")
		}
		if format == "table" && !strings.Contains(out.String(), "INSTANCE_TYPE") {
			t.Fatal("placement columns missing")
		}
		if format == "json" && errOut.Len() != 0 {
			t.Fatal("JSON included progress")
		}
	}
}

func TestInferenceHardwareRecommendationCommands(t *testing.T) {
	command := vllm.New().Command(product.Deps{})
	found := false
	for _, child := range command.Commands() {
		if child.Name() == "recommend-hardware" {
			found = true
			tiers := child.Commands()
			if len(tiers) != 1 || tiers[0].Name() != "scale" {
				t.Fatal("hardware recommendations must be Scale-only")
			}
		}
	}
	if !found {
		t.Fatal("inference is missing recommend-hardware")
	}
}

func TestInferenceHardwareRecommendations(t *testing.T) {
	for _, model := range []string{"org/model", "https://huggingface.co/org/model"} {
		for _, format := range []string{"table", "json"} {
			t.Run(model+"/"+format, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != "POST" || r.URL.Path != "/v1/vllm/hardware-recommendations" {
						t.Errorf("unexpected recommendation request: %s %s", r.Method, r.URL.Path)
					}
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					key := "model"
					if strings.HasPrefix(model, "https://") {
						key = "url"
					}
					if body[key] != model || body["context_length"] != float64(32768) || body["pricing_mode"] != "dedicated" || body["dedicated"] != true || body["location"] != "US" || body["cloud"] != nil || body["region"] != nil {
						t.Errorf("unexpected recommendation body: %v", body)
					}
					if _, exists := body["model_url"]; exists {
						t.Error("model_url must be translated for the API")
					}
					if _, exists := body["max_model_len"]; exists {
						t.Error("max_model_len must be translated for the API")
					}
					fmt.Fprint(w, `{"model_id":"org/model","deployment_options":[{"cloud":"nebius","region":"eu-north1","sku":"gpu-h100-sxm","gpu_type":"H100-80GB"}],"quantization_options":[{"name":"fp16","selected":true}]}`)
				}))
				defer server.Close()
				command := vllm.New().Command(product.Deps{Client: func() *api.Client { return api.NewClient(server.URL, "") }})
				var out, progress bytes.Buffer
				command.SetOut(&out)
				command.SetErr(&progress)
				command.SetArgs([]string{"recommend-hardware", "scale", "--model-url", model, "--max-model-len", "32768", "--location", "us", "-o", format})
				if err := command.ExecuteContext(t.Context()); err != nil {
					t.Fatal(err)
				}
				if format == "json" {
					var response map[string]any
					if err := json.Unmarshal(out.Bytes(), &response); err != nil {
						t.Fatal(err)
					}
					options := response["deployment_options"].([]any)
					row := options[0].(map[string]any)
					if row["sku"] != "gpu-h100-sxm" || row["gpu_type"] != "H100-80GB" || len(row) != 4 || response["model_id"] != "org/model" || response["quantization_options"] == nil || progress.Len() != 0 {
						t.Fatalf("JSON response fields or progress changed: %s", out.String())
					}
					return
				}
				lines := strings.Split(strings.TrimSpace(out.String()), "\n")
				if len(lines) != 2 || strings.Join(strings.Fields(lines[0]), ",") != "CLOUD,REGION,INSTANCE_TYPE,GPU_TYPE" || strings.Join(strings.Fields(lines[1]), ",") != "nebius,eu-north1,gpu-h100-sxm,H100-80GB" {
					t.Fatalf("unexpected hardware table: %s", out.String())
				}
			})
		}
	}
}

func TestInferenceHardwareRecommendationPlacement(t *testing.T) {
	model := "https://huggingface.co/Qwen/Qwen2.5-0.5B"
	for _, tc := range []struct {
		name     string
		args     []string
		input    string
		location string
	}{
		{"location flag", []string{"--location", "us"}, "", "US"},
		{"location config", []string{"--config-file", "-"}, `{"location":" eu "}`, "EU"},
		{"location Asia", []string{"--location", "asia"}, "", "ASIA"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "POST" || r.URL.Path != "/v1/vllm/hardware-recommendations" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body["url"] != model || body["context_length"] != float64(32000) || body["pricing_mode"] != "dedicated" || body["dedicated"] != true {
					t.Errorf("unexpected recommendation input: %v", body)
				}
				if body["location"] != tc.location || body["cloud"] != nil || body["region"] != nil {
					t.Errorf("expected location %q without cloud or region: %v", tc.location, body)
				}
				fmt.Fprint(w, `{"deployment_options":[]}`)
			}))
			defer server.Close()
			command := vllm.New().Command(product.Deps{Client: func() *api.Client { return api.NewClient(server.URL, "") }})
			args := []string{"recommend-hardware", "scale", "--model-url", model, "--max-model-len", "32000", "-o", "json"}
			command.SetArgs(append(args, tc.args...))
			command.SetIn(strings.NewReader(tc.input))
			command.SetOut(&bytes.Buffer{})
			command.SetErr(&bytes.Buffer{})
			if err := command.ExecuteContext(t.Context()); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("expected one recommendation request, got %d", calls)
			}
		})
	}
}

func TestPostgresOfferPriceInformation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"estimated_price_per_hour_usd":0.1727,"estimated_price_per_month_usd":126.07,"deployment_options":[{"estimated_price_per_hour_usd":0.1727,"estimated_price_per_month_usd":126.07,"compute_price_per_hour_usd":0.15,"storage_price_per_month_usd":16.57,"source":"catalog","requested_vcpu":2}]}`)
	}))
	defer server.Close()
	for _, tier := range []string{"on-demand", "scale"} {
		for _, format := range []string{"table", "json"} {
			t.Run(tier+"/"+format, func(t *testing.T) {
				command := postgres.New().Command(product.Deps{Client: func() *api.Client { return api.NewClient(server.URL, "") }})
				args := []string{"offers", tier, "--storage-gib", "50", "-o", format}
				if tier == "on-demand" {
					args = append(args, "--vcpu", "2", "--location", "US")
				} else {
					args = append(args, "--cloud", "aws", "--region", "us-east-1")
				}
				var out, progress bytes.Buffer
				command.SetOut(&out)
				command.SetErr(&progress)
				command.SetArgs(args)
				if err := command.ExecuteContext(t.Context()); err != nil {
					t.Fatal(err)
				}
				if format == "json" {
					if !strings.Contains(out.String(), "0.1727") || strings.Contains(out.String(), "126.07") || !strings.Contains(out.String(), `"source": "catalog"`) || progress.Len() != 0 {
						t.Fatalf("JSON price or progress behavior changed: %s", out.String())
					}
				} else if tier == "scale" {
					if strings.Count(out.String(), "ESTIMATED_PRICE_PER_HOUR_USD") != 2 || strings.Count(out.String(), "0.1727") != 2 {
						t.Fatalf("Scale offer metadata or rows lost hourly price: %s", out.String())
					}
					for _, hidden := range []string{"ESTIMATED_PRICE_PER_MONTH_USD", "COMPUTE_PRICE", "STORAGE_PRICE", "126.07", "0.15", "16.57", "SOURCE", "REQUESTED_VCPU"} {
						if strings.Contains(out.String(), hidden) {
							t.Fatalf("Scale offer table includes hidden field or value %q: %s", hidden, out.String())
						}
					}
				} else if strings.Contains(strings.ToLower(out.String()), "price") || strings.Contains(out.String(), "0.1727") || strings.Contains(out.String(), "126.07") {
					t.Fatalf("offer table includes price information: %s", out.String())
				}
			})
		}
	}
}
