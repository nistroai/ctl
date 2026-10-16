package product_test

import (
	"bytes"
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

func TestProductOperations(t *testing.T) {
	for _, tc := range []struct {
		prod       product.CLIProduct
		name, path string
	}{
		{vllm.New(), "inference", "/v1/vllm-kubeai/deployments"},
		{postgres.New(), "postgres", "/v1/postgres/deployments"},
	} {
		for _, operation := range []string{"list", "describe", "delete"} {
			t.Run(tc.name+"/"+operation, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					path, method := tc.path, "GET"
					if operation != "list" {
						path += "/dep-1"
					}
					if operation == "delete" {
						method = "DELETE"
					}
					if r.URL.Path != path || r.Method != method {
						t.Errorf("request: %s %s", r.Method, r.URL.Path)
					}
					if operation == "list" {
						fmt.Fprint(w, `{"deployments":[{"id":"dep-1","model_name":"model","status":"running","pricing_mode":"basic","api_key":"secret","extra":"hidden"}]}`)
					} else {
						fmt.Fprint(w, `{"deployment":{"id":"dep-1","api_key":"secret","extra":"visible"}}`)
					}
				}))
				defer server.Close()
				command := tc.prod.Command(product.Deps{Client: func() *api.Client { return api.NewClient(server.URL, "") }})
				if command.Name() != tc.name {
					t.Fatalf("name: %s", command.Name())
				}
				var output bytes.Buffer
				var progress bytes.Buffer
				command.SetOut(&output)
				command.SetErr(&progress)
				args := []string{operation}
				if operation != "list" {
					args = append(args, "dep-1")
				}
				command.SetArgs(args)
				if err := command.ExecuteContext(t.Context()); err != nil {
					t.Fatal(err)
				}
				message := map[string]string{"list": "Loaded deployments", "describe": "Loaded deployment details", "delete": "Deployment deletion request accepted"}[operation]
				if !strings.Contains(progress.String(), "✓ "+message) {
					t.Fatalf("missing success line: %s", progress.String())
				}
				if strings.Contains(output.String(), "secret") {
					t.Fatal("table leaked credentials")
				}
				if operation == "list" && (strings.Contains(output.String(), "hidden") || !strings.Contains(output.String(), "MODEL_NAME")) {
					t.Fatal("list summary fields differ")
				}
				output.Reset()
				progress.Reset()
				command.SetArgs(append(args, "--output", "json"))
				if err := command.ExecuteContext(t.Context()); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(output.String(), "secret") {
					t.Fatal("JSON did not preserve API response")
				}
				if progress.Len() != 0 {
					t.Fatalf("JSON output included progress: %s", progress.String())
				}
			})
		}
	}
}
