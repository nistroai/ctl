package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

type DeployRequest struct {
	Model            string `json:"model"`
	Quantization     string `json:"quantization,omitempty"`
	ContextLength    int    `json:"context_length,omitempty"`
	Location         string `json:"location,omitempty"`
	Cloud            string `json:"cloud,omitempty"`
	Region           string `json:"region,omitempty"`
	InstanceType     string `json:"instance_type,omitempty"`
	GPUArchitecture  string `json:"gpu_architecture,omitempty"`
	GPUType          string `json:"gpu_type,omitempty"`
	Dedicated        bool   `json:"dedicated,omitempty"`
	AcceptCheapest   bool   `json:"accept_cheapest,omitempty"`
	VLLMAPIKey       string `json:"vllm_api_key,omitempty"`
	CustomerID       string `json:"customer_id,omitempty"`
	StripeCustomerID string `json:"stripe_customer_id,omitempty"`
	SubscriptionID   string `json:"subscription_id,omitempty"`
}

type Deployment struct {
	ID                   string          `json:"id"`
	Model                string          `json:"model"`
	ContextLength        int             `json:"context_length"`
	Location             string          `json:"location"`
	Cloud                string          `json:"cloud"`
	Region               string          `json:"region"`
	GPUType              string          `json:"gpu_type"`
	GPUArchitecture      string          `json:"gpu_architecture"`
	Dedicated            bool            `json:"dedicated"`
	GPUFraction          float64         `json:"gpu_fraction"`
	GPUResourceName      string          `json:"gpu_resource_name"`
	MIGProfile           string          `json:"mig_profile"`
	Status               string          `json:"status"`
	StatusMessage        string          `json:"status_message"`
	OmnistrateInstanceID string          `json:"omnistrate_instance_id"`
	EndpointURL          string          `json:"endpoint_url"`
	ObservabilityURL     string          `json:"observability_url"`
	SelectedOffer        json.RawMessage `json:"selected_offer"`
}

type LaunchEvent struct {
	DeploymentID string          `json:"deployment_id"`
	EventKey     string          `json:"event_key"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
	ObservedAt   time.Time       `json:"observed_at"`
	Phase        string          `json:"phase"`
	Level        string          `json:"level"`
	Message      string          `json:"message"`
	Payload      json.RawMessage `json:"payload,omitempty"`
}

type Offer struct {
	Cloud           string  `json:"cloud"`
	Region          string  `json:"region"`
	SKU             string  `json:"sku"`
	GPU             string  `json:"gpu"`
	GPUType         string  `json:"gpu_type"`
	GPUArchitecture string  `json:"gpu_architecture"`
	GPUVRAMGB       int     `json:"gpu_vram_gb"`
	GPUCount        int     `json:"gpu_count"`
	PricePerHourUSD float64 `json:"price_per_hour_usd"`
	Dedicated       bool    `json:"dedicated"`
	GPUFraction     float64 `json:"gpu_fraction"`
	MIGProfile      string  `json:"mig_profile"`
	MIGMemoryGB     float64 `json:"mig_memory_gb"`
}

type RecommendationResponse struct {
	DeploymentOptions []Offer  `json:"deployment_options"`
	Warnings          []string `json:"warnings"`
}

type CreateResponse struct {
	Deployment     Deployment             `json:"deployment"`
	SelectedOffer  Offer                  `json:"selected_offer"`
	Recommendation RecommendationResponse `json:"recommendation"`
}

type UsageSummary struct {
	CustomerID        string    `json:"customer_id"`
	ExternalPayerID   string    `json:"external_payer_id"`
	DeploymentID      string    `json:"deployment_id"`
	Period            string    `json:"period"`
	Dimension         string    `json:"dimension"`
	Value             float64   `json:"value"`
	PricePerUnit      float64   `json:"price_per_unit"`
	SubtotalUSD       float64   `json:"subtotal_usd"`
	Finalized         bool      `json:"finalized"`
	PublishedToStripe bool      `json:"published_to_stripe"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type ObserveSummary struct {
	Timestamp           time.Time `json:"timestamp"`
	TokensPerSecond     float64   `json:"tokens_per_second"`
	PromptTokensPerSec  float64   `json:"prompt_tokens_per_second"`
	OutputTokensPerSec  float64   `json:"output_tokens_per_second"`
	RunningRequests     float64   `json:"running_requests"`
	WaitingRequests     float64   `json:"waiting_requests"`
	ContextLength       int       `json:"context_length"`
	KVCacheUsagePercent float64   `json:"kv_cache_usage_percent"`
	GPUUtilization      float64   `json:"gpu_utilization_percent"`
	VRAMUsedGB          float64   `json:"vram_used_gb"`
	VRAMTotalGB         float64   `json:"vram_total_gb"`
	MIGProfile          string    `json:"mig_profile"`
	GPUFraction         float64   `json:"gpu_fraction"`
}

func NewClient(baseURL, token string) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), token: token, httpClient: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) Recommend(ctx context.Context, req DeployRequest) (RecommendationResponse, error) {
	var out RecommendationResponse
	err := c.doJSON(ctx, http.MethodPost, "/v1/recommendations", req, &out)
	return out, err
}

func (c *Client) Create(ctx context.Context, req DeployRequest) (CreateResponse, error) {
	var out CreateResponse
	err := c.doJSON(ctx, http.MethodPost, "/v1/vllm/deployments", req, &out)
	return out, err
}

func (c *Client) Deployment(ctx context.Context, id string) (Deployment, error) {
	var out Deployment
	err := c.doJSON(ctx, http.MethodGet, "/v1/vllm/deployments/"+url.PathEscape(id), nil, &out)
	return out, err
}

func (c *Client) LaunchEvents(ctx context.Context, id string) ([]LaunchEvent, error) {
	var out struct {
		Events []LaunchEvent `json:"events"`
	}
	err := c.doJSON(ctx, http.MethodGet, "/v1/vllm/deployments/"+url.PathEscape(id)+"/launch-events", nil, &out)
	return out.Events, err
}

func (c *Client) Delete(ctx context.Context, id string) (Deployment, error) {
	var out Deployment
	err := c.doJSON(ctx, http.MethodDelete, "/v1/vllm/deployments/"+url.PathEscape(id), nil, &out)
	return out, err
}

func (c *Client) Usage(ctx context.Context, period, customerID string) ([]UsageSummary, error) {
	path := "/v1/usage"
	query := url.Values{}
	if period != "" {
		query.Set("period", period)
	}
	if customerID != "" {
		query.Set("customer_id", customerID)
	}
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	var out struct {
		Usage []UsageSummary `json:"usage"`
	}
	err := c.doJSON(ctx, http.MethodGet, path, nil, &out)
	return out.Usage, err
}

func (c *Client) BillingStatus(ctx context.Context, customerID string) (map[string]string, error) {
	path := "/v1/billing/status"
	if customerID != "" {
		path += "?customer_id=" + url.QueryEscape(customerID)
	}
	var out map[string]string
	err := c.doJSON(ctx, http.MethodGet, path, nil, &out)
	return out, err
}

func (c *Client) Observe(ctx context.Context, endpoint, apiKey string) (ObserveSummary, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(endpoint, "/")+"/v1/observability/summary", nil)
	if err != nil {
		return ObserveSummary{}, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return ObserveSummary{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ObserveSummary{}, fmt.Errorf("observer returned %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out ObserveSummary
	if err := json.Unmarshal(raw, &out); err != nil {
		return ObserveSummary{}, err
	}
	return out, nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, payload any, dst any) error {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if dst != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, dst); err != nil {
			return err
		}
	}
	return nil
}
