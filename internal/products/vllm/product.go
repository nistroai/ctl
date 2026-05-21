package vllm

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"nistro-ctl/internal/api"
	"nistro-ctl/internal/product"
	"nistro-ctl/internal/tui"
)

type Product struct{}

func New() Product {
	return Product{}
}

func (Product) Name() string {
	return "vllm"
}

func (Product) Command(deps product.Deps) *cobra.Command {
	var createCfg struct {
		model           string
		url             string
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
		accountConfigID string
	}
	var observeCfg struct {
		apiKey   string
		endpoint string
	}
	var eventsCfg struct {
		watch bool
	}
	var quantizationCfg struct {
		model         string
		url           string
		quantization  string
		contextLength string
	}
	vllmCmd := &cobra.Command{
		Use:   "vllm",
		Short: "Manage vLLM deployments",
	}
	vllmCreateCmd := &cobra.Command{
		Use:   "create",
		Short: "Create a managed vLLM service",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(createCfg.model) == "" && strings.TrimSpace(createCfg.url) == "" {
				return fmt.Errorf("--model or --url is required")
			}
			contextLength, err := parseContextLength(createCfg.contextLength)
			if err != nil {
				return err
			}
			pricingMode := "basic"
			if strings.TrimSpace(createCfg.instanceType) != "" {
				pricingMode = "dedicated"
				createCfg.dedicated = true
			}
			req := api.DeployRequest{
				Model:           createCfg.model,
				URL:             createCfg.url,
				Quantization:    createCfg.quantization,
				ContextLength:   contextLength,
				Location:        strings.ToUpper(firstNonEmpty(createCfg.location, "US")),
				Cloud:           createCfg.cloud,
				Region:          createCfg.region,
				InstanceType:    createCfg.instanceType,
				GPUArchitecture: createCfg.gpuArchitecture,
				GPUType:         createCfg.gpuType,
				Dedicated:       createCfg.dedicated,
				PricingMode:     pricingMode,
				AcceptCheapest:  createCfg.yes,
				AccountConfigID: createCfg.accountConfigID,
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
			defer cancel()
			c := deps.Client()
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
				req.Dedicated = selected.Dedicated
			}
			resp, err := c.Create(ctx, req)
			if err != nil {
				return err
			}
			deps.PrintJSON(resp)
			fmt.Fprintf(deps.Stderr, "watching launch events for %s (q to quit)\n", resp.Deployment.ID)
			watchCtx, watchCancel := context.WithCancel(cmd.Context())
			defer watchCancel()
			program := tea.NewProgram(tui.NewLaunchEventsModel(resp.Deployment.ID))
			go tui.StreamLaunchEvents(watchCtx, program, c, resp.Deployment.ID, 2*time.Second)
			if _, err := program.Run(); err != nil {
				return err
			}
			if resp.Deployment.ObservabilityURL != "" {
				fmt.Fprintf(deps.Stderr, "observe: nistro vllm observe %s\n", resp.Deployment.ID)
			}
			return nil
		},
	}

	vllmListCmd := &cobra.Command{
		Use:   "list",
		Short: "List vLLM deployments",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			deployments, err := deps.Client().Deployments(ctx)
			if err != nil {
				return err
			}
			deps.PrintJSON(map[string]any{"deployments": deployments})
			return nil
		},
	}

	vllmStatusCmd := &cobra.Command{
		Use:   "status <deployment-id>",
		Short: "Show deployment status",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			deployment, err := deps.Client().Deployment(ctx, args[0])
			if err != nil {
				return err
			}
			deps.PrintJSON(deployment)
			return nil
		},
	}

	vllmDeleteCmd := &cobra.Command{
		Use:   "delete <deployment-id>",
		Short: "Delete a deployment",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			deployment, err := deps.Client().Delete(ctx, args[0])
			if err != nil {
				return err
			}
			deps.PrintJSON(deployment)
			return nil
		},
	}

	vllmEventsCmd := &cobra.Command{
		Use:   "events <deployment-id>",
		Short: "Show vLLM launch events",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if eventsCfg.watch {
				watchCtx, watchCancel := context.WithCancel(cmd.Context())
				defer watchCancel()
				program := tea.NewProgram(tui.NewLaunchEventsModel(args[0]))
				go tui.StreamLaunchEvents(watchCtx, program, deps.Client(), args[0], 2*time.Second)
				_, err := program.Run()
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			events, err := deps.Client().LaunchEvents(ctx, args[0])
			if err != nil {
				return err
			}
			deps.PrintJSON(map[string]any{"events": events})
			return nil
		},
	}

	vllmQuantizationsCmd := &cobra.Command{
		Use:   "quantizations",
		Short: "Show server-detected quantization options for a model",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(quantizationCfg.model) == "" && strings.TrimSpace(quantizationCfg.url) == "" {
				return fmt.Errorf("--model or --url is required")
			}
			contextLength, err := parseContextLength(firstNonEmpty(quantizationCfg.contextLength, "64K"))
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
			defer cancel()
			resp, err := deps.Client().Quantizations(ctx, api.DeployRequest{
				Model:         quantizationCfg.model,
				URL:           quantizationCfg.url,
				Quantization:  quantizationCfg.quantization,
				ContextLength: contextLength,
			})
			if err != nil {
				return err
			}
			deps.PrintJSON(resp)
			return nil
		},
	}

	vllmObserveCmd := &cobra.Command{
		Use:   "observe <deployment-id>",
		Short: "Open the vLLM hardware and throughput TUI",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			c := deps.Client()
			endpoint := observeCfg.endpoint
			if endpoint == "" {
				deployment, err := c.Deployment(ctx, args[0])
				if err != nil {
					return err
				}
				endpoint = deployment.ObservabilityURL
				if observeCfg.apiKey == "" {
					observeCfg.apiKey = deployment.VLLMAPIKey
				}
			}
			if endpoint == "" {
				return fmt.Errorf("deployment does not have an observability endpoint yet")
			}
			apiKey := firstNonEmpty(observeCfg.apiKey, deps.Env("VLLM_API_KEY", ""))
			if apiKey == "" {
				return fmt.Errorf("deployment does not have a generated vLLM API key yet; wait until it is ready or pass --api-key")
			}
			_, err := tea.NewProgram(tui.NewObserveModel(c, endpoint, apiKey)).Run()
			return err
		},
	}

	vllmCreateCmd.Flags().StringVar(&createCfg.model, "model", "", "Hugging Face model name")
	vllmCreateCmd.Flags().StringVar(&createCfg.url, "url", "", "Hugging Face model URL")
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
	vllmCreateCmd.Flags().StringVar(&createCfg.accountConfigID, "cloud-account-config-id", "", "Deploy in a connected cloud account")
	vllmEventsCmd.Flags().BoolVar(&eventsCfg.watch, "watch", false, "Watch events until interrupted")
	vllmQuantizationsCmd.Flags().StringVar(&quantizationCfg.model, "model", "", "Hugging Face model name")
	vllmQuantizationsCmd.Flags().StringVar(&quantizationCfg.url, "url", "", "Hugging Face model URL")
	vllmQuantizationsCmd.Flags().StringVar(&quantizationCfg.quantization, "quantization", "", "Optional quantization")
	vllmQuantizationsCmd.Flags().StringVar(&quantizationCfg.contextLength, "context-length", "64K", "Context length")
	vllmObserveCmd.Flags().StringVar(&observeCfg.apiKey, "api-key", "", "vLLM API key")
	vllmObserveCmd.Flags().StringVar(&observeCfg.endpoint, "endpoint", "", "Observer endpoint override")
	vllmCmd.AddCommand(vllmCreateCmd, vllmListCmd, vllmStatusCmd, vllmDeleteCmd, vllmEventsCmd, vllmQuantizationsCmd, vllmObserveCmd)
	return vllmCmd
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
