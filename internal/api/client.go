package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

type SignInRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type SignInResponse struct {
	Token string         `json:"token"`
	Auth  map[string]any `json:"auth,omitempty"`
}

type DeployRequest struct {
	Model           string `json:"model"`
	URL             string `json:"url,omitempty"`
	Quantization    string `json:"quantization,omitempty"`
	ContextLength   int    `json:"context_length,omitempty"`
	Location        string `json:"location,omitempty"`
	Cloud           string `json:"cloud,omitempty"`
	Region          string `json:"region,omitempty"`
	InstanceType    string `json:"instance_type,omitempty"`
	GPUArchitecture string `json:"gpu_architecture,omitempty"`
	GPUType         string `json:"gpu_type,omitempty"`
	Dedicated       bool   `json:"dedicated,omitempty"`
	PricingMode     string `json:"pricing_mode,omitempty"`
	AcceptCheapest  bool   `json:"accept_cheapest,omitempty"`
	AccountConfigID string `json:"cloud_provider_account_config_id,omitempty"`
}

type PostgresDeployRequest struct {
	VCPU             float64 `json:"vcpu"`
	MemoryGiB        float64 `json:"memory_gib"`
	StorageGiB       float64 `json:"storage_gib"`
	Location         string  `json:"location,omitempty"`
	Cloud            string  `json:"cloud,omitempty"`
	Region           string  `json:"region,omitempty"`
	InstanceType     string  `json:"instance_type,omitempty"`
	AcceptCheapest   bool    `json:"accept_cheapest,omitempty"`
	DatabaseName     string  `json:"database_name,omitempty"`
	DatabaseUsername string  `json:"database_username,omitempty"`
	DatabasePassword string  `json:"database_password,omitempty"`
	PricingMode      string  `json:"pricing_mode,omitempty"`
	AccountConfigID  string  `json:"cloud_provider_account_config_id,omitempty"`
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
	VLLMAPIKey           string          `json:"vllm_api_key"`
	SelectedOffer        json.RawMessage `json:"selected_offer"`
}

type PostgresDeployment struct {
	ID                   string          `json:"id"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
	CustomerID           string          `json:"customer_id"`
	Location             string          `json:"location"`
	Cloud                string          `json:"cloud"`
	Region               string          `json:"region"`
	InstanceType         string          `json:"instance_type"`
	VCPU                 float64         `json:"vcpu"`
	MemoryGiB            float64         `json:"memory_gib"`
	StorageGiB           float64         `json:"storage_gib"`
	DatabaseName         string          `json:"database_name"`
	DatabaseUsername     string          `json:"database_username"`
	Status               string          `json:"status"`
	StatusMessage        string          `json:"status_message"`
	OmnistrateInstanceID string          `json:"omnistrate_instance_id"`
	WriterEndpointURL    string          `json:"writer_endpoint_url"`
	ReaderEndpointURL    string          `json:"reader_endpoint_url"`
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

type PostgresDeploymentEvent struct {
	ID           int64           `json:"id"`
	DeploymentID string          `json:"deployment_id"`
	CreatedAt    time.Time       `json:"created_at"`
	Status       string          `json:"status"`
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

type PostgresOffer struct {
	Cloud                     string  `json:"cloud"`
	Region                    string  `json:"region"`
	InstanceType              string  `json:"instance_type"`
	VCPU                      float64 `json:"vcpu"`
	RAMGiB                    float64 `json:"ram_gib"`
	StorageGiB                float64 `json:"storage_gib"`
	StorageType               string  `json:"storage_type"`
	ComputePricePerHourUSD    float64 `json:"compute_price_per_hour_usd"`
	StoragePricePerMonthUSD   float64 `json:"storage_price_per_month_usd"`
	EstimatedPricePerHourUSD  float64 `json:"estimated_price_per_hour_usd"`
	EstimatedPricePerMonthUSD float64 `json:"estimated_price_per_month_usd"`
	FractionalVCPUSatisfied   bool    `json:"fractional_vcpu_satisfied"`
	RequestedVCPU             float64 `json:"requested_vcpu"`
	RequestedMemoryGiB        float64 `json:"requested_memory_gib"`
	RequestedStorageGiB       float64 `json:"requested_storage_gib"`
}

type RecommendationResponse struct {
	DeploymentOptions []Offer  `json:"deployment_options"`
	Warnings          []string `json:"warnings"`
}

type PostgresRecommendationResponse struct {
	DeploymentOptions []PostgresOffer `json:"deployment_options"`
	Warnings          []string        `json:"warnings"`
}

type CreateResponse struct {
	Deployment     Deployment             `json:"deployment"`
	SelectedOffer  Offer                  `json:"selected_offer"`
	Recommendation RecommendationResponse `json:"recommendation"`
}

type PostgresCreateResponse struct {
	Deployment     PostgresDeployment             `json:"deployment"`
	SelectedOffer  PostgresOffer                  `json:"selected_offer"`
	Recommendation PostgresRecommendationResponse `json:"recommendation"`
	Omnistrate     map[string]any                 `json:"omnistrate,omitempty"`
}

type CloudAccountRequest struct {
	CloudProvider       string `json:"cloud_provider"`
	CloudProviderID     string `json:"cloud_provider_id,omitempty"`
	Name                string `json:"name,omitempty"`
	Description         string `json:"description,omitempty"`
	AWSAccountID        string `json:"aws_account_id,omitempty"`
	AWSBootstrapRoleARN string `json:"aws_bootstrap_role_arn,omitempty"`
	GCPProjectID        string `json:"gcp_project_id,omitempty"`
	GCPProjectNumber    string `json:"gcp_project_number,omitempty"`
	GCPServiceAccount   string `json:"gcp_service_account_email,omitempty"`
	AzureSubscriptionID string `json:"azure_subscription_id,omitempty"`
	AzureTenantID       string `json:"azure_tenant_id,omitempty"`
	NebiusTenantID      string `json:"nebius_tenant_id,omitempty"`
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

// Resource preserves the full response for deployment tables and details.
func (c *Client) Resource(ctx context.Context, method, path string, payload any) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.doJSON(ctx, method, path, payload, &out)
	if len(out) == 0 && err == nil {
		out = json.RawMessage(`{"ok":true}`)
	}
	return out, err
}

func (c *Client) SignIn(ctx context.Context, req SignInRequest) (SignInResponse, error) {
	var out SignInResponse
	err := c.doJSON(ctx, http.MethodPost, "/v1/auth/signin", req, &out)
	return out, err
}

func (c *Client) Recommend(ctx context.Context, req DeployRequest) (RecommendationResponse, error) {
	var out RecommendationResponse
	err := c.doJSON(ctx, http.MethodPost, "/v1/recommendations", req, &out)
	return out, err
}

func (c *Client) Quantizations(ctx context.Context, req DeployRequest) (map[string]any, error) {
	path := "/v1/vllm/quantizations"
	if query := deployQuery(req); len(query) > 0 {
		path += "?" + query.Encode()
	}
	var out map[string]any
	err := c.doJSON(ctx, http.MethodGet, path, nil, &out)
	return out, err
}

func (c *Client) Create(ctx context.Context, req DeployRequest) (CreateResponse, error) {
	var out CreateResponse
	err := c.doJSON(ctx, http.MethodPost, "/v1/vllm/deployments", req, &out)
	return out, err
}

func (c *Client) Deployments(ctx context.Context) ([]Deployment, error) {
	var out struct {
		Deployments []Deployment `json:"deployments"`
	}
	err := c.doJSON(ctx, http.MethodGet, "/v1/vllm/deployments", nil, &out)
	return out.Deployments, err
}

func (c *Client) PostgresOffers(ctx context.Context, req PostgresDeployRequest) (PostgresRecommendationResponse, error) {
	var out PostgresRecommendationResponse
	err := c.doJSON(ctx, http.MethodPost, "/v1/postgres/offers", req, &out)
	return out, err
}

func (c *Client) CreatePostgres(ctx context.Context, req PostgresDeployRequest) (PostgresCreateResponse, error) {
	var out PostgresCreateResponse
	err := c.doJSON(ctx, http.MethodPost, "/v1/postgres/deployments", req, &out)
	return out, err
}

func (c *Client) PostgresDeployments(ctx context.Context) ([]PostgresDeployment, error) {
	var out struct {
		Deployments []PostgresDeployment `json:"deployments"`
	}
	err := c.doJSON(ctx, http.MethodGet, "/v1/postgres/deployments", nil, &out)
	return out.Deployments, err
}

func (c *Client) PostgresDeployment(ctx context.Context, id string) (PostgresDeployment, error) {
	var out PostgresDeployment
	err := c.doJSON(ctx, http.MethodGet, "/v1/postgres/deployments/"+url.PathEscape(id), nil, &out)
	return out, err
}

func (c *Client) PostgresEvents(ctx context.Context, id string) ([]PostgresDeploymentEvent, error) {
	var out struct {
		Events []PostgresDeploymentEvent `json:"events"`
	}
	err := c.doJSON(ctx, http.MethodGet, "/v1/postgres/deployments/"+url.PathEscape(id)+"/events", nil, &out)
	return out.Events, err
}

func (c *Client) DeletePostgres(ctx context.Context, id string) (PostgresDeployment, error) {
	var out PostgresDeployment
	err := c.doJSON(ctx, http.MethodDelete, "/v1/postgres/deployments/"+url.PathEscape(id), nil, &out)
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

func (c *Client) Usage(ctx context.Context, period string) ([]UsageSummary, error) {
	path := "/v1/usage"
	query := url.Values{}
	if period != "" {
		query.Set("period", period)
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

func (c *Client) BillingStatus(ctx context.Context) (map[string]any, error) {
	path := "/v1/billing/status"
	var out map[string]any
	err := c.doJSON(ctx, http.MethodGet, path, nil, &out)
	return out, err
}

func (c *Client) BillingDetails(ctx context.Context, returnURL string) (map[string]any, error) {
	path := "/v1/billing/details"
	if strings.TrimSpace(returnURL) != "" {
		query := url.Values{}
		query.Set("return_url", strings.TrimSpace(returnURL))
		path += "?" + query.Encode()
	}
	var out map[string]any
	err := c.doJSON(ctx, http.MethodGet, path, nil, &out)
	return out, err
}

func (c *Client) CloudAccounts(ctx context.Context, cloud string) (map[string]any, error) {
	path := "/v1/cloud-accounts"
	if strings.TrimSpace(cloud) != "" {
		query := url.Values{}
		query.Set("cloud", strings.TrimSpace(cloud))
		path += "?" + query.Encode()
	}
	var out map[string]any
	err := c.doJSON(ctx, http.MethodGet, path, nil, &out)
	return out, err
}

func (c *Client) CreateCloudAccount(ctx context.Context, req CloudAccountRequest) (map[string]any, error) {
	var out map[string]any
	err := c.doJSON(ctx, http.MethodPost, "/v1/cloud-accounts", req, &out)
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

func deployQuery(req DeployRequest) url.Values {
	query := url.Values{}
	setQueryString(query, "model", req.Model)
	setQueryString(query, "url", req.URL)
	setQueryString(query, "quantization", req.Quantization)
	if req.ContextLength > 0 {
		query.Set("context_length", strconv.Itoa(req.ContextLength))
	}
	setQueryString(query, "location", req.Location)
	setQueryString(query, "cloud", req.Cloud)
	setQueryString(query, "region", req.Region)
	setQueryString(query, "instance_type", req.InstanceType)
	setQueryString(query, "gpu_architecture", req.GPUArchitecture)
	setQueryString(query, "gpu_type", req.GPUType)
	setQueryString(query, "pricing_mode", req.PricingMode)
	if req.Dedicated {
		query.Set("dedicated", "true")
	}
	return query
}

func setQueryString(query url.Values, key, value string) {
	if strings.TrimSpace(value) != "" {
		query.Set(key, strings.TrimSpace(value))
	}
}
