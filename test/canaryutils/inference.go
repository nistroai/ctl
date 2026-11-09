package canaryutils

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// CheckInference discovers the served model and performs one real, nonstreaming
// OpenAI chat completion for the requested model or its unambiguous HF alias.
// Responses must identify the requested model and contain generated text and
// positive token usage; their content is never included in errors. The caller
// may retry after a readiness failure.
// Remote endpoints require HTTPS; HTTP is reserved for loopback fixtures.
func CheckInference(ctx context.Context, baseURL, apiKey, model string) error {
	return checkInference(ctx, baseURL, apiKey, model, false)
}

// CheckInferenceExact requires the explicit served model alias verbatim, without
// applying Hugging Face URL normalization or final-segment alias selection.
func CheckInferenceExact(ctx context.Context, baseURL, apiKey, servedAlias string) error {
	return checkInference(ctx, baseURL, apiKey, servedAlias, true)
}

func checkInference(ctx context.Context, baseURL, apiKey, model string, exactOnly bool) error {
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return errors.New("Inference canary requires an HTTP or HTTPS endpoint without embedded credentials")
	}
	if base.Scheme == "http" {
		host := base.Hostname()
		address := net.ParseIP(host)
		if !strings.EqualFold(host, "localhost") && (address == nil || !address.IsLoopback()) {
			return errors.New("Inference canary requires HTTPS for nonloopback endpoints")
		}
	}
	if strings.TrimSpace(apiKey) == "" {
		return errors.New("Inference canary requires an API key")
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return errors.New("Inference canary requires a configured model or explicit served model alias")
	}
	if strings.HasSuffix(base.Path, "/openai") {
		base.Path += "/v1"
	} else if !strings.HasSuffix(base.Path, "/v1") {
		base.Path = strings.TrimRight(base.Path, "/") + "/openai/v1"
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	client := &http.Client{
		Timeout:       90 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	var models struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := inferenceJSON(ctx, client, http.MethodGet, base.String()+"/models", apiKey, nil, &models); err != nil {
		return err
	}
	advertised := make([]string, 0, len(models.Data))
	for _, served := range models.Data {
		advertised = append(advertised, served.ID)
	}
	model, err = requestedInferenceModel(model, advertised, exactOnly)
	if err != nil {
		return errors.New("Inference canary did not find the requested model in the advertised models")
	}
	request := map[string]any{
		"model": model, "messages": []map[string]string{{"role": "user", "content": "Reply with a short greeting."}},
		"max_tokens": 32, "temperature": 0, "stream": false,
	}
	var completion struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := inferenceJSON(ctx, client, http.MethodPost, base.String()+"/chat/completions", apiKey, request, &completion); err != nil {
		return err
	}
	if strings.TrimSpace(completion.Model) == "" || completion.Model != model {
		return errors.New("Inference canary chat completion did not report the requested model")
	}
	if len(completion.Choices) == 0 || strings.TrimSpace(completion.Choices[0].Message.Content) == "" {
		return errors.New("Inference canary returned no generated text")
	}
	usage := completion.Usage
	// Subtract positive counts so validating their sum cannot overflow an int.
	if usage.PromptTokens <= 0 || usage.CompletionTokens <= 0 || usage.TotalTokens <= 0 || usage.TotalTokens-usage.PromptTokens != usage.CompletionTokens {
		return errors.New("Inference canary returned missing or invalid token usage")
	}
	return nil
}

func requestedInferenceModel(requested string, advertised []string, exactOnly bool) (string, error) {
	if exactOnly {
		for _, id := range advertised {
			if id == requested {
				return id, nil
			}
		}
		return "", errors.New("requested model is absent")
	}
	canonical := requested
	if reference, err := url.Parse(requested); err == nil && reference.Scheme == "https" && reference.Host == "huggingface.co" && reference.User == nil && reference.RawQuery == "" && reference.Fragment == "" {
		path := strings.Trim(reference.Path, "/")
		if len(strings.Split(path, "/")) == 2 {
			canonical = path
		}
	}
	for _, exact := range []string{requested, canonical} {
		for _, id := range advertised {
			if id == exact {
				return id, nil
			}
		}
	}
	// A simple explicit served alias is exact-only. An HF org/model reference
	// can use its final segment when the advertised namespace is unambiguous.
	parts := strings.Split(canonical, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", errors.New("requested model is absent")
	}
	alias := parts[1]
	aliases := 0
	for _, id := range advertised {
		if id == alias {
			aliases++
		} else if strings.Contains(id, "/") && id != canonical && id[strings.LastIndex(id, "/")+1:] == alias {
			return "", errors.New("requested model alias is ambiguous")
		}
	}
	if aliases == 1 {
		return alias, nil
	}
	return "", errors.New("requested model is absent")
}

func inferenceJSON(ctx context.Context, client *http.Client, method, endpoint, apiKey string, payload, response any) error {
	stage := "models"
	if method == http.MethodPost {
		stage = "chat completion"
	}
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("Inference canary %s request could not be encoded", stage)
		}
		body = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return fmt.Errorf("Inference canary %s request is invalid", stage)
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("Accept", "application/json")
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	result, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("Inference canary %s: %w", stage, ctx.Err())
		}
		return fmt.Errorf("Inference canary %s request failed", stage)
	}
	defer result.Body.Close()
	if result.StatusCode < 200 || result.StatusCode >= 300 {
		return fmt.Errorf("Inference canary %s returned HTTP %d", stage, result.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(result.Body, (2<<20)+1))
	if err != nil && ctx.Err() != nil {
		return fmt.Errorf("Inference canary %s: %w", stage, ctx.Err())
	}
	if err != nil || len(raw) > 2<<20 {
		return fmt.Errorf("Inference canary %s response could not be read within the limit", stage)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if decoder.Decode(response) != nil || decoder.Decode(new(any)) != io.EOF {
		return fmt.Errorf("Inference canary %s returned invalid JSON", stage)
	}
	return nil
}
