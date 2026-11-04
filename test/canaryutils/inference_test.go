package canaryutils

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCheckInferencePathsAndGeneration(t *testing.T) {
	for _, test := range []struct{ basePath, apiPath, model string }{
		{"", "/openai/v1", "served-model"},
		{"/openai", "/openai/v1", "served-model"},
		{"/openai/", "/openai/v1", "served-model"},
		{"/openai/v1", "/openai/v1", "served-model"},
		{"/openai/v1/", "/openai/v1", "served-model"},
		{"/gateway/openai/v1", "/gateway/openai/v1", "served-model"},
		{"/gateway/openai", "/gateway/openai/v1", "served-model"},
		{"/v1", "/v1", "served-model"},
	} {
		t.Run(test.basePath, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Header.Get("Authorization") != "Bearer fake-inference-secret" || r.Header.Get("Accept") != "application/json" {
					t.Error("data-plane probe omitted selected authentication or JSON accept header")
				}
				if r.Method == "GET" && r.URL.Path == test.apiPath+"/models" {
					fmt.Fprint(w, `{"data":[{"id":""},{"id":"served-model"}]}`)
					return
				}
				if r.Method != "POST" || r.URL.Path != test.apiPath+"/chat/completions" || r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("unexpected OpenAI request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(400)
					return
				}
				var request map[string]any
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				want := map[string]any{
					"model": "served-model", "max_tokens": float64(32), "temperature": float64(0), "stream": false,
					"messages": []any{map[string]any{"role": "user", "content": "Reply with a short greeting."}},
				}
				if !reflect.DeepEqual(request, want) {
					t.Errorf("completion request = %#v, want %#v", request, want)
				}
				fmt.Fprint(w, validCompletion)
			}))
			defer server.Close()
			if err := CheckInference(t.Context(), server.URL+test.basePath, "fake-inference-secret", test.model); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 2 {
				t.Fatalf("probe made %d calls, want model discovery followed by generation", calls.Load())
			}
		})
	}
}

const validCompletion = `{"model":"served-model","choices":[{"message":{"content":"A generated greeting"}}],"usage":{"prompt_tokens":6,"completion_tokens":3,"total_tokens":9}}`

type recordingTransport func(*http.Request) (*http.Response, error)

func (transport recordingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestCheckInferenceRejectsRemoteHTTPBeforeSendingCredentials(t *testing.T) {
	var requests atomic.Int32
	original := http.DefaultTransport
	http.DefaultTransport = recordingTransport(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, errors.New("unexpected network request")
	})
	defer func() { http.DefaultTransport = original }()
	for _, endpoint := range []string{
		"http://inference.example.invalid", "http://192.0.2.10", "http://[2001:db8::1]", "http://localhost.example.invalid",
	} {
		err := CheckInference(t.Context(), endpoint, "fake-inference-secret", "served-model")
		if err == nil || !strings.Contains(err.Error(), "requires HTTPS") || strings.Contains(err.Error(), "fake-inference-secret") {
			t.Fatalf("remote HTTP endpoint was not rejected before authentication: %v", err)
		}
	}
	if requests.Load() != 0 {
		t.Fatal("remote HTTP validation sent a network request")
	}
}

func TestCheckInferenceTargetsRequestedModel(t *testing.T) {
	const configured = "Qwen/Qwen2.5-7B-Instruct"
	for _, test := range []struct {
		name, requested, selected string
		advertised                []string
		exact                     bool
	}{
		{"unrelated model appears first", configured, configured, []string{"unrelated-model", configured}, false},
		{"Hugging Face URL", "https://huggingface.co/" + configured, configured, []string{"unrelated-model", configured}, false},
		{"unambiguous final segment", configured, "Qwen2.5-7B-Instruct", []string{"unrelated-model", "Qwen2.5-7B-Instruct"}, false},
		{"explicit served alias", "served-alias", "served-alias", []string{"unrelated-model", "served-alias"}, true},
		{"explicit alias is exact", configured, "", []string{"Qwen2.5-7B-Instruct"}, true},
		{"ambiguous final segment", configured, "", []string{"Other/Qwen2.5-7B-Instruct", "Qwen2.5-7B-Instruct"}, false},
		{"missing configured model", configured, "", []string{"unrelated-model"}, false},
		{"no configured identity", "", "", []string{"unrelated-model"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var completion map[string]any
			if err := json.Unmarshal([]byte(validCompletion), &completion); err != nil {
				t.Fatal(err)
			}
			completion["model"] = test.selected
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method == "GET" {
					data := []map[string]string{}
					for _, id := range test.advertised {
						data = append(data, map[string]string{"id": id})
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
					return
				}
				var request struct{ Model string }
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if test.selected == "" || request.Model != test.selected {
					t.Error("completion targeted a model other than the configured identity")
					w.WriteHeader(400)
					return
				}
				_ = json.NewEncoder(w).Encode(completion)
			}))
			defer server.Close()
			probe := CheckInference
			if test.exact {
				probe = CheckInferenceExact
			}
			err := probe(t.Context(), server.URL, "fake-inference-secret", test.requested)
			if test.selected != "" {
				if err != nil || calls.Load() != 2 {
					t.Fatalf("requested model was not discovered and used for generation: %v", err)
				}
			} else {
				if err == nil || calls.Load() > 1 {
					t.Fatal("missing or ambiguous configured identity allowed a completion")
				}
				if test.requested == "" && calls.Load() != 0 {
					t.Fatal("missing model identity sent a network request")
				}
			}
		})
	}
}

func TestCheckInferenceRejectsInvalidCompletionModel(t *testing.T) {
	const configured = "Qwen/Qwen2.5-7B-Instruct"
	const modelDiagnostic = "chat completion did not report the requested model"
	for _, test := range []struct {
		name, requested, diagnostic string
		responseModel               any
		omitModel, exact            bool
	}{
		{name: "unrelated response model is also advertised", responseModel: "unrelated-model", diagnostic: modelDiagnostic},
		{name: "missing response model", omitModel: true, diagnostic: modelDiagnostic},
		{name: "empty response model", responseModel: "", diagnostic: modelDiagnostic},
		{name: "whitespace response model", responseModel: " ", diagnostic: modelDiagnostic},
		{name: "null response model", responseModel: nil, diagnostic: modelDiagnostic},
		{name: "numeric response model", responseModel: 42, diagnostic: "invalid JSON"},
		{name: "object response model", responseModel: map[string]string{"id": configured}, diagnostic: "invalid JSON"},
		{name: "exact alias rejects configured model", requested: "runtime-alias", responseModel: configured, exact: true, diagnostic: modelDiagnostic},
		{name: "exact configured identity rejects final segment", responseModel: "Qwen2.5-7B-Instruct", exact: true, diagnostic: modelDiagnostic},
	} {
		t.Run(test.name, func(t *testing.T) {
			requested := test.requested
			if requested == "" {
				requested = configured
			}
			var completion map[string]any
			if err := json.Unmarshal([]byte(validCompletion), &completion); err != nil {
				t.Fatal(err)
			}
			if test.omitModel {
				delete(completion, "model")
			} else {
				completion["model"] = test.responseModel
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method == http.MethodGet && r.URL.Path == "/openai/v1/models" {
					_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{
						{"id": configured}, {"id": "Qwen2.5-7B-Instruct"}, {"id": "unrelated-model"}, {"id": "runtime-alias"},
					}})
					return
				}
				if r.Method != http.MethodPost || r.URL.Path != "/openai/v1/chat/completions" {
					t.Error("unexpected completion model validation request")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				var body struct{ Model string }
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model != requested {
					t.Error("completion did not target the selected advertised model")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				_ = json.NewEncoder(w).Encode(completion)
			}))
			defer server.Close()
			probe := CheckInference
			if test.exact {
				probe = CheckInferenceExact
			}
			err := probe(t.Context(), server.URL, "fake-inference-secret", requested)
			if err == nil || !strings.Contains(err.Error(), test.diagnostic) || calls.Load() != 2 {
				t.Fatalf("invalid completion model was not rejected after generation: %v", err)
			}
			if strings.Contains(err.Error(), "fake-inference-secret") || strings.Contains(err.Error(), server.URL) || strings.Contains(err.Error(), configured) || strings.Contains(err.Error(), "unrelated-model") {
				t.Fatal("completion model error exposed credentials or response details")
			}
		})
	}
}

func TestCheckInferenceRejectsUnusableResponses(t *testing.T) {
	for _, test := range []struct {
		name, models, completion, diagnostic string
	}{
		{"missing model", `{"data":[{"id":"different-model"}]}`, validCompletion, "requested model"},
		{"malformed models", `{"data":`, validCompletion, "invalid JSON"},
		{"empty generated text", `{"data":[{"id":"served-model"}]}`, `{"model":"served-model","choices":[{"message":{"content":" "}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`, "no generated text"},
		{"no choices", `{"data":[{"id":"served-model"}]}`, `{"model":"served-model","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`, "no generated text"},
		{"missing usage", `{"data":[{"id":"served-model"}]}`, `{"model":"served-model","choices":[{"message":{"content":"hello"}}]}`, "token usage"},
		{"inconsistent usage", `{"data":[{"id":"served-model"}]}`, `{"model":"served-model","choices":[{"message":{"content":"hello"}}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":1}}`, "token usage"},
		{"malformed completion", `{"data":[{"id":"served-model"}]}`, `{"secret":"fake-inference-secret"`, "invalid JSON"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					fmt.Fprint(w, test.models)
				} else {
					fmt.Fprint(w, test.completion)
				}
			}))
			defer server.Close()
			err := CheckInference(t.Context(), server.URL, "fake-inference-secret", "served-model")
			if err == nil || !strings.Contains(err.Error(), test.diagnostic) {
				t.Fatalf("probe error = %v, want %q", err, test.diagnostic)
			}
			if strings.Contains(err.Error(), "fake-inference-secret") || strings.Contains(err.Error(), server.URL) {
				t.Fatal("probe error exposed credentials or endpoint details")
			}
		})
	}
}

func TestCheckInferenceCallerCanRetryReadiness(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `{"message":"warming up fake-inference-secret"}`)
			return
		}
		if r.Method == "GET" {
			fmt.Fprint(w, `{"data":[{"id":"served-model"}]}`)
		} else {
			fmt.Fprint(w, validCompletion)
		}
	}))
	defer server.Close()
	err := CheckInference(t.Context(), server.URL, "fake-inference-secret", "served-model")
	if err == nil || !strings.Contains(err.Error(), "HTTP 503") || calls.Load() != 1 || strings.Contains(err.Error(), "fake-inference-secret") {
		t.Fatalf("readiness failure did not return immediately with safe diagnostics: %v", err)
	}
	if err := CheckInference(t.Context(), server.URL, "fake-inference-secret", "served-model"); err != nil || calls.Load() != 3 {
		t.Fatalf("fresh caller retry did not perform model discovery and generation: %v", err)
	}
}

func TestCheckInferenceDeadline(t *testing.T) {
	for _, stage := range []string{"models", "completion response body"} {
		t.Run(stage, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if stage == "completion response body" {
					if r.Method == "GET" {
						fmt.Fprint(w, `{"data":[{"id":"served-model"}]}`)
						return
					}
					fmt.Fprint(w, `{"choices":[`)
					w.(http.Flusher).Flush()
				}
				<-r.Context().Done()
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
			defer cancel()
			started := time.Now()
			err := CheckInference(ctx, server.URL, "fake-inference-secret", "served-model")
			if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
				t.Fatalf("probe ignored its caller deadline: %v", err)
			}
		})
	}
}
