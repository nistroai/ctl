package postgres

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
	return "postgres"
}

func (Product) Command(deps product.Deps) *cobra.Command {
	var createCfg struct {
		vcpu            float64
		memory          string
		storage         string
		location        string
		cloud           string
		region          string
		instanceType    string
		databaseName    string
		databaseUser    string
		databasePass    string
		yes             bool
		accountConfigID string
	}
	var eventsCfg struct {
		watch bool
	}
	postgresCmd := &cobra.Command{
		Use:   "postgres",
		Short: "Manage Postgres deployments",
	}
	postgresCreateCmd := &cobra.Command{
		Use:   "create",
		Short: "Create a managed Postgres database",
		RunE: func(cmd *cobra.Command, args []string) error {
			memoryGiB, err := parseGiB(createCfg.memory)
			if err != nil {
				return fmt.Errorf("--memory: %w", err)
			}
			storageGiB, err := parseGiB(createCfg.storage)
			if err != nil {
				return fmt.Errorf("--storage: %w", err)
			}
			if createCfg.vcpu <= 0 {
				return fmt.Errorf("--vcpu must be greater than zero")
			}
			pricingMode := "basic"
			if strings.TrimSpace(createCfg.instanceType) != "" {
				pricingMode = "dedicated"
			}
			req := api.PostgresDeployRequest{
				VCPU:             createCfg.vcpu,
				MemoryGiB:        memoryGiB,
				StorageGiB:       storageGiB,
				Location:         strings.ToUpper(firstNonEmpty(createCfg.location, "US")),
				Cloud:            createCfg.cloud,
				Region:           createCfg.region,
				InstanceType:     createCfg.instanceType,
				AcceptCheapest:   createCfg.yes,
				DatabaseName:     firstNonEmpty(createCfg.databaseName, "app"),
				DatabaseUsername: firstNonEmpty(createCfg.databaseUser, "app"),
				DatabasePassword: createCfg.databasePass,
				PricingMode:      pricingMode,
				AccountConfigID:  createCfg.accountConfigID,
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
			defer cancel()
			c := deps.Client()
			var selectedPreview *api.PostgresOffer
			if !createCfg.yes && req.Cloud == "" && req.Region == "" && req.InstanceType == "" {
				options, err := c.PostgresOffers(ctx, req)
				if err != nil {
					return err
				}
				if len(options.DeploymentOptions) == 0 {
					return fmt.Errorf("no postgres deployment options returned")
				}
				selector := tui.NewPostgresOfferSelector(options.DeploymentOptions)
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
				for _, offer := range options.DeploymentOptions {
					if strings.EqualFold(offer.Cloud, req.Cloud) && strings.EqualFold(offer.Region, req.Region) && strings.EqualFold(offer.InstanceType, req.InstanceType) {
						offerCopy := offer
						selectedPreview = &offerCopy
						break
					}
				}
			}
			resp, err := c.CreatePostgres(ctx, req)
			if err != nil {
				return err
			}
			if selectedPreview == nil {
				selectedPreview = &resp.SelectedOffer
			}
			printCreateResponse(deps, req, resp, *selectedPreview)
			fmt.Fprintf(deps.Stderr, "watching Postgres events for %s (q to quit)\n", resp.Deployment.ID)
			watchCtx, watchCancel := context.WithCancel(cmd.Context())
			defer watchCancel()
			program := tea.NewProgram(tui.NewPostgresEventsModel(resp.Deployment.ID))
			go tui.StreamPostgresEvents(watchCtx, program, c, resp.Deployment.ID, 2*time.Second)
			if _, err := program.Run(); err != nil {
				return err
			}
			return nil
		},
	}

	postgresListCmd := &cobra.Command{
		Use:   "list",
		Short: "List Postgres deployments",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			deployments, err := deps.Client().PostgresDeployments(ctx)
			if err != nil {
				return err
			}
			deps.PrintJSON(map[string]any{"deployments": deployments})
			return nil
		},
	}

	postgresStatusCmd := &cobra.Command{
		Use:   "status <deployment-id>",
		Short: "Show Postgres deployment status",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			deployment, err := deps.Client().PostgresDeployment(ctx, args[0])
			if err != nil {
				return err
			}
			deps.PrintJSON(deployment)
			return nil
		},
	}

	postgresDeleteCmd := &cobra.Command{
		Use:   "delete <deployment-id>",
		Short: "Delete a Postgres deployment",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			deployment, err := deps.Client().DeletePostgres(ctx, args[0])
			if err != nil {
				return err
			}
			deps.PrintJSON(deployment)
			return nil
		},
	}

	postgresEventsCmd := &cobra.Command{
		Use:   "events <deployment-id>",
		Short: "Show Postgres deployment events",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if eventsCfg.watch {
				watchCtx, watchCancel := context.WithCancel(cmd.Context())
				defer watchCancel()
				program := tea.NewProgram(tui.NewPostgresEventsModel(args[0]))
				go tui.StreamPostgresEvents(watchCtx, program, deps.Client(), args[0], 2*time.Second)
				_, err := program.Run()
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			events, err := deps.Client().PostgresEvents(ctx, args[0])
			if err != nil {
				return err
			}
			deps.PrintJSON(map[string]any{"events": events})
			return nil
		},
	}

	postgresCreateCmd.Flags().Float64Var(&createCfg.vcpu, "vcpu", 0, "Required vCPU amount, fractional values allowed")
	postgresCreateCmd.Flags().StringVar(&createCfg.memory, "memory", "", "Required memory in GiB, e.g. 2 or 2Gi")
	postgresCreateCmd.Flags().StringVar(&createCfg.storage, "storage", "", "Required storage in GiB, e.g. 50 or 50Gi")
	postgresCreateCmd.Flags().StringVar(&createCfg.location, "location", "US", "Location: US, EU, or APAC")
	postgresCreateCmd.Flags().StringVar(&createCfg.cloud, "cloud", "", "Cloud override")
	postgresCreateCmd.Flags().StringVar(&createCfg.region, "region", "", "Region override")
	postgresCreateCmd.Flags().StringVar(&createCfg.instanceType, "instance-type", "", "Instance type override")
	postgresCreateCmd.Flags().StringVar(&createCfg.databaseName, "database", "app", "Database name")
	postgresCreateCmd.Flags().StringVar(&createCfg.databaseUser, "username", "app", "Database username")
	postgresCreateCmd.Flags().StringVar(&createCfg.databasePass, "password", "", "Database password; generated by the server when omitted")
	postgresCreateCmd.Flags().BoolVarP(&createCfg.yes, "yes", "y", false, "Accept cheapest option without table")
	postgresCreateCmd.Flags().StringVar(&createCfg.accountConfigID, "cloud-account-config-id", "", "Deploy in a connected cloud account")
	postgresEventsCmd.Flags().BoolVar(&eventsCfg.watch, "watch", false, "Watch events until interrupted")
	_ = postgresCreateCmd.MarkFlagRequired("vcpu")
	_ = postgresCreateCmd.MarkFlagRequired("memory")
	_ = postgresCreateCmd.MarkFlagRequired("storage")
	postgresCmd.AddCommand(postgresCreateCmd, postgresListCmd, postgresStatusCmd, postgresDeleteCmd, postgresEventsCmd)
	return postgresCmd
}

func parseGiB(value string) (float64, error) {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.TrimSuffix(value, "gib")
	value = strings.TrimSuffix(value, "gi")
	value = strings.TrimSuffix(value, "gb")
	value = strings.TrimSuffix(value, "g")
	if strings.TrimSpace(value) == "" {
		return 0, fmt.Errorf("value is required")
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid GiB value %q", value)
	}
	if parsed <= 0 {
		return 0, fmt.Errorf("value must be greater than zero")
	}
	return parsed, nil
}

func printCreateResponse(deps product.Deps, req api.PostgresDeployRequest, resp api.PostgresCreateResponse, selected api.PostgresOffer) {
	fmt.Fprintf(deps.Stdout, "✓ Requested Postgres: %.3g vCPU, %.3g Gi memory, %.3g Gi storage\n", req.VCPU, req.MemoryGiB, req.StorageGiB)
	fmt.Fprintf(deps.Stdout, "✓ Found %d compatible offers\n", len(resp.Recommendation.DeploymentOptions))
	fmt.Fprintf(deps.Stdout, "✓ Selected cheapest %s placement\n", firstNonEmpty(req.Location, "US"))
	fmt.Fprintf(deps.Stdout, "  cloud: %s\n", selected.Cloud)
	fmt.Fprintf(deps.Stdout, "  region: %s\n", selected.Region)
	fmt.Fprintf(deps.Stdout, "  instance: %s\n", selected.InstanceType)
	fmt.Fprintf(deps.Stdout, "  compute: %.3g vCPU / %.3g Gi\n", selected.VCPU, selected.RAMGiB)
	fmt.Fprintf(deps.Stdout, "  storage: %.3g Gi %s\n", selected.StorageGiB, selected.StorageType)
	fmt.Fprintf(deps.Stdout, "  estimated compute: $%.4f/hr\n", selected.ComputePricePerHourUSD)
	fmt.Fprintf(deps.Stdout, "  estimated storage: $%.2f/mo\n\n", selected.StoragePricePerMonthUSD)
	fmt.Fprintf(deps.Stdout, "✓ Created Nistro deployment record\n")
	fmt.Fprintf(deps.Stdout, "  deployment: %s\n\n", resp.Deployment.ID)
	if resp.Deployment.OmnistrateInstanceID != "" {
		fmt.Fprintf(deps.Stdout, "✓ Submitted Omnistrate instance create request\n")
		fmt.Fprintf(deps.Stdout, "  instance: %s\n", resp.Deployment.OmnistrateInstanceID)
		fmt.Fprintf(deps.Stdout, "  status: %s\n", resp.Deployment.Status)
	} else {
		fmt.Fprintf(deps.Stdout, "✓ Created Nistro deployment record; Omnistrate submission is pending configuration\n")
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
