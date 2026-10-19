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

func TestPostgresBaseOfferVCPU(t *testing.T) {
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
					fmt.Fprint(w, `{"deployment_options":[]}`)
				}))
				defer server.Close()
				command := postgres.New().Command(product.Deps{Client: func() *api.Client { return api.NewClient(server.URL, "") }})
				args := []string{"offers", "base", "--vcpu", value, "--storage-gib", "50", "--location", "US"}
				if inputMethod == "config" {
					args = []string{"offers", "base", "--config-file", "-"}
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
		{vllm.New(), []string{"offers", "base", "--cloud", "aws"}, ""},
		{vllm.New(), []string{"offers", "scale", "--replicas", "2"}, ""},
		{vllm.New(), []string{"offers", "scale", "--config-file", "-"}, `{"model_url":"org/model","max_model_len":32768,"location":"US","region":"us-east-1"}`},
		{postgres.New(), []string{"offers", "base", "--cloud", "aws"}, ""},
		{postgres.New(), []string{"offers", "base", "--memory-gib", "8"}, ""},
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

func TestRecommendationVRAMColumns(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"deployment_options":[{"gpu":"A100","actual_vram_gb":80,"gpu_vram_gb":40,"price_per_hour_usd":0.1727}]}`)
	}))
	defer server.Close()
	for _, tier := range []string{"base", "scale"} {
		for _, format := range []string{"table", "json"} {
			t.Run(tier+"/"+format, func(t *testing.T) {
				command := vllm.New().Command(product.Deps{Client: func() *api.Client { return api.NewClient(server.URL, "") }})
				var out bytes.Buffer
				command.SetOut(&out)
				command.SetErr(&bytes.Buffer{})
				command.SetArgs([]string{"offers", tier, "--model-url", "org/model", "--max-model-len", "32768", "--location", "US", "-o", format})
				if err := command.ExecuteContext(t.Context()); err != nil {
					t.Fatal(err)
				}
				if format == "json" {
					if !strings.Contains(out.String(), `"actual_vram_gb": 80`) || !strings.Contains(out.String(), `"gpu_vram_gb": 40`) || !strings.Contains(out.String(), `"price_per_hour_usd": 0.1727`) {
						t.Fatal("JSON response fields changed")
					}
					return
				}
				lines := strings.Split(strings.TrimSpace(out.String()), "\n")
				headers, cells := strings.Fields(lines[0]), strings.Fields(lines[1])
				if strings.Contains(out.String(), "PRICE") || strings.Contains(out.String(), "0.1727") {
					t.Fatalf("recommendation table includes price information: %s", out.String())
				}
				want := "80"
				for i, header := range headers {
					if header == "GPU_VRAM_GB" && cells[i] == want {
						return
					}
				}
				t.Fatalf("GPU_VRAM_GB should be %s: %s", want, out.String())
			})
		}
	}
}

func TestOfferTablesOmitPriceInformation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"estimated_price_per_hour_usd":0.1727,"estimated_price_per_month_usd":126.07,"deployment_options":[{"estimated_price_per_hour_usd":0.1727,"estimated_price_per_month_usd":126.07,"source":"catalog","requested_vcpu":2}]}`)
	}))
	defer server.Close()
	for _, tier := range []string{"base", "scale"} {
		for _, format := range []string{"table", "json"} {
			t.Run(tier+"/"+format, func(t *testing.T) {
				command := postgres.New().Command(product.Deps{Client: func() *api.Client { return api.NewClient(server.URL, "") }})
				args := []string{"offers", tier, "--storage-gib", "50", "-o", format}
				if tier == "base" {
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
				} else if strings.Contains(strings.ToLower(out.String()), "price") || strings.Contains(out.String(), "0.1727") || strings.Contains(out.String(), "126.07") {
					t.Fatalf("offer table includes price information: %s", out.String())
				}
			})
		}
	}
}
