package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"nistro-ctl/internal/api"
	"nistro-ctl/internal/tui"
)

var vllmCmd = &cobra.Command{
	Use:   "vllm",
	Short: "Manage vLLM deployments",
}

var createCfg struct {
	model           string
	quantization    string
	contextLength   string
	location        string
	cloud           string
	region          string
	instanceType    string
	gpuArchitecture string
	gpuType         string
	dedicated       bool
	yes             bool
	apiKey          string
	customerID      string
	stripeCustomer  string
	subscriptionID  string
}

var vllmCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a managed vLLM service",
	RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(createCfg.model) == "" {
			return fmt.Errorf("--model is required")
		}
		contextLength, err := parseContextLength(createCfg.contextLength)
		if err != nil {
			return err
		}
		req := api.DeployRequest{
			Model:            createCfg.model,
			Quantization:     createCfg.quantization,
			ContextLength:    contextLength,
			Location:         strings.ToUpper(firstNonEmpty(createCfg.location, "US")),
			Cloud:            createCfg.cloud,
			Region:           createCfg.region,
			InstanceType:     createCfg.instanceType,
			GPUArchitecture:  createCfg.gpuArchitecture,
			GPUType:          createCfg.gpuType,
			Dedicated:        createCfg.dedicated,
			AcceptCheapest:   createCfg.yes,
			VLLMAPIKey:       firstNonEmpty(createCfg.apiKey, env("VLLM_API_KEY", "")),
			CustomerID:       firstNonEmpty(createCfg.customerID, env("NISTRO_CUSTOMER_ID", "")),
			StripeCustomerID: firstNonEmpty(createCfg.stripeCustomer, env("STRIPE_CUSTOMER_ID", "")),
			SubscriptionID:   firstNonEmpty(createCfg.subscriptionID, env("NISTRO_OMNI_SUBSCRIPTION_ID", "")),
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
		defer cancel()
		c := client()
		if !createCfg.yes && req.Cloud == "" && req.Region == "" {
			rec, err := c.Recommend(ctx, req)
			if err != nil {
				return err
			}
			if len(rec.DeploymentOptions) == 0 {
				return fmt.Errorf("no deployment options returned")
			}
			selector := tui.NewOfferSelector(rec.DeploymentOptions)
			if _, err := tea.NewProgram(selector).Run(); err != nil {
				return err
			}
			selected, err := selector.Selected()
			if err != nil {
				return err
			}
			req.Cloud = selected.Cloud
			req.Region = selected.Region
			req.InstanceType = selected.SKU
			req.GPUType = firstNonEmpty(selected.GPUType, selected.GPU)
			req.GPUArchitecture = selected.GPUArchitecture
		}
		resp, err := c.Create(ctx, req)
		if err != nil {
			return err
		}
		printJSON(resp)
		fmt.Fprintf(os.Stderr, "watching launch events for %s (q to quit)\n", resp.Deployment.ID)
		watchCtx, watchCancel := context.WithCancel(cmd.Context())
		defer watchCancel()
		program := tea.NewProgram(tui.NewLaunchEventsModel(resp.Deployment.ID))
		go tui.StreamLaunchEvents(watchCtx, program, c, resp.Deployment.ID, 2*time.Second)
		if _, err := program.Run(); err != nil {
			return err
		}
		if resp.Deployment.ObservabilityURL != "" {
			fmt.Fprintf(os.Stderr, "observe: nistro vllm observe %s --api-key <vllm-api-key>\n", resp.Deployment.ID)
		}
		return nil
	},
}

var vllmStatusCmd = &cobra.Command{
	Use:   "status <deployment-id>",
	Short: "Show deployment status",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
		defer cancel()
		deployment, err := client().Deployment(ctx, args[0])
		if err != nil {
			return err
		}
		printJSON(deployment)
		return nil
	},
}

var vllmDeleteCmd = &cobra.Command{
	Use:   "delete <deployment-id>",
	Short: "Delete a deployment",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
		defer cancel()
		deployment, err := client().Delete(ctx, args[0])
		if err != nil {
			return err
		}
		printJSON(deployment)
		return nil
	},
}

var observeCfg struct {
	apiKey   string
	endpoint string
}

var vllmObserveCmd = &cobra.Command{
	Use:   "observe <deployment-id>",
	Short: "Open the vLLM hardware and throughput TUI",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
		defer cancel()
		c := client()
		endpoint := observeCfg.endpoint
		if endpoint == "" {
			deployment, err := c.Deployment(ctx, args[0])
			if err != nil {
				return err
			}
			endpoint = deployment.ObservabilityURL
		}
		if endpoint == "" {
			return fmt.Errorf("deployment does not have an observability endpoint yet")
		}
		apiKey := firstNonEmpty(observeCfg.apiKey, env("VLLM_API_KEY", ""))
		if apiKey == "" {
			return fmt.Errorf("--api-key or VLLM_API_KEY is required")
		}
		_, err := tea.NewProgram(tui.NewObserveModel(c, endpoint, apiKey)).Run()
		return err
	},
}

func init() {
	vllmCreateCmd.Flags().StringVar(&createCfg.model, "model", "", "Hugging Face model name")
	vllmCreateCmd.Flags().StringVar(&createCfg.quantization, "quantization", "", "Optional quantization")
	vllmCreateCmd.Flags().StringVar(&createCfg.contextLength, "context-length", "64K", "Context length")
	vllmCreateCmd.Flags().StringVar(&createCfg.location, "location", "US", "Location: US, EU, or APAC")
	vllmCreateCmd.Flags().StringVar(&createCfg.cloud, "cloud", "", "Cloud override")
	vllmCreateCmd.Flags().StringVar(&createCfg.region, "region", "", "Region override")
	vllmCreateCmd.Flags().StringVar(&createCfg.instanceType, "instance-type", "", "Instance type override")
	vllmCreateCmd.Flags().StringVar(&createCfg.gpuArchitecture, "gpu-architecture", "", "GPU architecture override")
	vllmCreateCmd.Flags().StringVar(&createCfg.gpuType, "gpu-type", "", "GPU type override")
	vllmCreateCmd.Flags().BoolVar(&createCfg.dedicated, "dedicated", false, "Require whole GPUs")
	vllmCreateCmd.Flags().BoolVarP(&createCfg.yes, "yes", "y", false, "Accept cheapest option without table")
	vllmCreateCmd.Flags().StringVar(&createCfg.apiKey, "api-key", "", "vLLM API key")
	vllmCreateCmd.Flags().StringVar(&createCfg.customerID, "customer-id", "", "Nistro customer ID")
	vllmCreateCmd.Flags().StringVar(&createCfg.stripeCustomer, "stripe-customer-id", "", "Stripe customer ID")
	vllmCreateCmd.Flags().StringVar(&createCfg.subscriptionID, "subscription-id", "", "Omnistrate subscription ID")
	vllmObserveCmd.Flags().StringVar(&observeCfg.apiKey, "api-key", "", "vLLM API key")
	vllmObserveCmd.Flags().StringVar(&observeCfg.endpoint, "endpoint", "", "Observer endpoint override")
	vllmCmd.AddCommand(vllmCreateCmd, vllmStatusCmd, vllmDeleteCmd, vllmObserveCmd)
}

func parseContextLength(value string) (int, error) {
	value = strings.TrimSpace(strings.ToUpper(value))
	multiplier := 1
	if strings.HasSuffix(value, "K") {
		multiplier = 1000
		value = strings.TrimSuffix(value, "K")
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("invalid context length %q", value)
	}
	return parsed * multiplier, nil
}

func printJSON(value any) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
