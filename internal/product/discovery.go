package product

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"nistro-ctl/internal/output"
)

func printPlacementRows(cmd *cobra.Command, rows []map[string]any) error {
	return output.Table(cmd.OutOrStdout(), rows, []string{"cloud", "region", "location", "instance_type"})
}

func discoveryFields(name, tier, command string) ([]inputField, []string) {
	model := []inputField{{"model_url", "string", "Required Hugging Face model URL or ID"}, {"max_model_len", "int", "Required context length in tokens"}}
	if name == "inference" {
		if command == "offers" {
			fields := append(model, inputField{"quantization", "string", "Quantization (default Auto)"})
			required := []string{"model_url", "max_model_len"}
			if tier == "on-demand" {
				return append(fields, inputField{"location", "string", "Required location: US, EU, ASIA, or ANY (cheapest eligible placement)"}), append(required, "location")
			}
			return append(fields, inputField{"cloud", "string", "Required cloud provider"}, inputField{"region", "string", "Required cloud region"}, inputField{"instance_type", "string", "Required instance type; use the hardware recommendation's sku"}, inputField{"gpu_type", "string", "GPU type (default Any)"}, inputField{"replicas", "int", "Fixed replica count from 1 to 8 (default 1); price covers all replicas"}), append(required, "cloud", "region", "instance_type")
		}
		return append(model, inputField{"quantization", "string", "Quantization (default Auto)"}, inputField{"gpu_type", "string", "GPU type (default Any)"}, inputField{"location", "string", "Required location: US, EU, or ASIA"}), []string{"model_url", "max_model_len", "location"}
	}
	fields := []inputField{{"storage_gib", "number", "Required storage size in GiB"}}
	if tier == "on-demand" {
		return append(fields, inputField{"vcpu", "int", "Required integer vCPUs from 1 to 8; memory is 4 GiB per vCPU"}, inputField{"location", "string", "Required location: US, EU, ASIA, or ANY (cheapest eligible placement)"}), []string{"vcpu", "storage_gib", "location"}
	}
	return append(fields, inputField{"cloud", "string", "Required cloud provider"}, inputField{"region", "string", "Required cloud region"}, inputField{"instance_type", "string", "Instance type (omit for Any); determines compute capacity"}, inputField{"replicas", "int", "Standby replicas from 0 to 8 (plus one primary; default 1)"}), []string{"storage_gib", "cloud", "region"}
}

func addPlacements(parent *cobra.Command, deps Deps) {
	group := &cobra.Command{Use: "placements", Short: "List available Postgres Scale clouds, regions, and instance types"}
	group.AddCommand(&cobra.Command{Use: "scale", Args: cobra.NoArgs, Short: "Show the hosted Postgres Scale placement catalog", RunE: func(cmd *cobra.Command, _ []string) error {
		format, _ := cmd.Flags().GetString("output")
		if format != "table" && format != "json" {
			return fmt.Errorf("output must be table or json")
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
		defer cancel()
		var progress *progressLine
		if format != "json" {
			progress = startProgress(cmd.ErrOrStderr(), "Loading placements")
		}
		raw, err := deps.Client().Resource(ctx, "GET", "/v1/postgres/scale/catalog", nil)
		if progress != nil {
			progress.finish("Loaded placements", err == nil)
		}
		if err != nil {
			return err
		}
		if format == "json" {
			return output.Print(cmd.OutOrStdout(), raw, format)
		}
		var catalog struct {
			Clouds []struct {
				Cloud   string `json:"cloud"`
				Regions []struct {
					Region    string   `json:"region"`
					Location  string   `json:"location"`
					Instances []string `json:"instance_types"`
				} `json:"regions"`
			} `json:"clouds"`
		}
		if err := json.Unmarshal(raw, &catalog); err != nil {
			return err
		}
		rows := []map[string]any{}
		for _, cloud := range catalog.Clouds {
			for _, region := range cloud.Regions {
				for _, instance := range region.Instances {
					rows = append(rows, map[string]any{"cloud": cloud.Cloud, "region": region.Region, "location": region.Location, "instance_type": instance})
				}
			}
		}
		return printPlacementRows(cmd, rows)
	}})
	parent.AddCommand(group)
}

func AddDiscovery(parent *cobra.Command, deps Deps) {
	if parent.Name() == "postgres" {
		addPlacements(parent, deps)
	}
	names := []string{"offers"}
	if parent.Name() == "inference" {
		names = []string{"recommend-hardware", "offers"}
	}
	for _, name := range names {
		group := &cobra.Command{Use: name, Short: "Show " + name + " before creating or updating deployments"}
		tiers := []string{"on-demand", "scale"}
		if name == "recommend-hardware" {
			group.Short = "Recommend hardware before creating Inference Scale deployments"
			tiers = []string{"scale"}
		}
		for _, tier := range tiers {
			fields, required := discoveryFields(parent.Name(), tier, name)
			cmd := &cobra.Command{Use: tier, Short: "Show " + tierLabel(tier) + " " + name, Args: cobra.NoArgs}
			if name == "recommend-hardware" {
				cmd.Short = "Recommend hardware for a " + tierLabel(tier) + " inference deployment"
			}
			cmd.Long = cmd.Short + ". Provide settings with flags or --config-file. JSON keys use underscores; flags use hyphens. Flags override JSON values."
			if name == "recommend-hardware" {
				cmd.Long += " Location is required: US, EU, or ASIA. Location ANY is not available for Scale."
			} else if tier == "scale" {
				cmd.Long += " Cloud and region are required; the server derives location from them."
			}
			cmd.Flags().String("config-file", "", "JSON configuration file; use - to read from stdin")
			for _, field := range fields {
				flag := strings.ReplaceAll(field.key, "_", "-")
				switch field.kind {
				case "int":
					cmd.Flags().Int(flag, 0, field.help)
				case "number":
					cmd.Flags().Float64(flag, 0, field.help)
				default:
					cmd.Flags().String(flag, "", field.help)
				}
			}
			cmd.RunE = func(cmd *cobra.Command, _ []string) error {
				format, _ := cmd.Flags().GetString("output")
				if format != "table" && format != "json" {
					return fmt.Errorf("output must be table or json")
				}
				body, err := mutationInput(cmd, fields)
				if err != nil {
					return err
				}
				if err := requireFields(body, required); err != nil {
					return err
				}
				if err := validateMutation(body, parent.Name(), tier, "discovery"); err != nil {
					return err
				}
				if parent.Name() == "postgres" && tier == "scale" {
					if _, ok := body["replicas"]; !ok {
						body["replicas"] = 1
					}
					if value, ok := body["replicas"]; ok && number(value) < 0 {
						return fmt.Errorf("replicas must be an integer from 0 to 8")
					}
				}
				mode := "basic"
				if tier == "scale" {
					mode = "dedicated"
				}
				body["pricing_mode"] = mode
				method, path := "POST", "/v1/postgres/offers"
				if parent.Name() == "postgres" {
					if tier == "on-demand" {
						body["memory_gib"] = number(body["vcpu"]) * 4
					} else {
						// Match the website's Any-instance request; the server filters pinned placements.
						body["vcpu"], body["memory_gib"] = 1, 1
					}
				} else {
					model := body["model_url"].(string)
					key := "model"
					if strings.HasPrefix(strings.ToLower(model), "https://") || strings.HasPrefix(strings.ToLower(model), "http://") {
						key = "url"
					}
					body[key], body["context_length"] = model, body["max_model_len"]
					delete(body, "model_url")
					delete(body, "max_model_len")
					path = "/v1/vllm/hardware-recommendations"
					if name == "offers" {
						path = "/v1/vllm/price-estimates"
						if tier == "scale" {
							body["node_count"] = 1
							if replicas, ok := body["replicas"]; ok {
								body["node_count"] = replicas
								delete(body, "replicas")
							}
						}
					}
					body["dedicated"] = tier == "scale"
				}
				ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
				defer cancel()
				var progress *progressLine
				progressName := "offers"
				if name == "recommend-hardware" {
					progressName = "hardware recommendations"
				}
				if format != "json" {
					progress = startProgress(cmd.ErrOrStderr(), "Loading "+progressName)
				}
				raw, err := deps.Client().Resource(ctx, method, path, body)
				if progress != nil {
					progress.finish("Loaded "+progressName, err == nil)
				}
				if err != nil {
					return err
				}
				if parent.Name() == "postgres" {
					raw, err = withoutMonthlyOfferPrice(raw)
					if err != nil {
						return err
					}
				}
				if format == "json" {
					return output.Print(cmd.OutOrStdout(), raw, format)
				}
				// Keep complete offer and model fields visible for configuration decisions.
				var data map[string]any
				if err := json.Unmarshal(raw, &data); err != nil {
					return err
				}
				options, _ := data["deployment_options"].([]any)
				if parent.Name() == "inference" {
					if name == "recommend-hardware" {
						return printRecommendationsTable(cmd, options)
					}
					return printInferenceOffersTable(cmd, options, tier)
				}
				tableOptions := output.TableOptions{ShowHourlyPrice: parent.Name() == "postgres" && tier == "scale"}
				removeOfferPriceFields(data, tableOptions.ShowHourlyPrice)
				delete(data, "deployment_options")
				if parent.Name() == "postgres" {
					for _, offer := range options {
						if row, ok := offer.(map[string]any); ok {
							delete(row, "source")
							if tier == "scale" {
								delete(row, "requested_memory_gib")
								delete(row, "requested_vcpu")
							}
						}
					}
				}
				if len(data) > 0 {
					metadata, _ := json.Marshal(data)
					if err := output.Print(cmd.OutOrStdout(), metadata, "table", tableOptions); err != nil {
						return err
					}
				}
				if len(options) > 0 {
					return printOffersTable(cmd, options, tableOptions)
				}
				if len(options) == 0 && len(data) == 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "No results.")
				}
				return nil
			}
			if cmd != group {
				group.AddCommand(cmd)
			}
		}
		parent.AddCommand(group)
	}
}

func removeOfferPriceFields(value any, showHourlyPrice bool) {
	switch entry := value.(type) {
	case map[string]any:
		for key, child := range entry {
			if lower := strings.ToLower(key); strings.Contains(lower, "price") && !(showHourlyPrice && lower == "estimated_price_per_hour_usd") {
				delete(entry, key)
				continue
			}
			removeOfferPriceFields(child, showHourlyPrice)
		}
	case []any:
		for _, child := range entry {
			removeOfferPriceFields(child, showHourlyPrice)
		}
	}
}

func withoutMonthlyOfferPrice(raw json.RawMessage) (json.RawMessage, error) {
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var remove func(any)
	remove = func(value any) {
		switch entry := value.(type) {
		case map[string]any:
			delete(entry, "estimated_price_per_month_usd")
			for _, child := range entry {
				remove(child)
			}
		case []any:
			for _, child := range entry {
				remove(child)
			}
		}
	}
	remove(value)
	return json.Marshal(value)
}

func printOffersTable(cmd *cobra.Command, offers []any, options output.TableOptions) error {
	rows := make([]map[string]any, 0, len(offers))
	for _, offer := range offers {
		if row, ok := offer.(map[string]any); ok {
			rows = append(rows, row)
		}
	}
	return output.Table(cmd.OutOrStdout(), rows, nil, options)
}

func printRecommendationsTable(cmd *cobra.Command, offers []any) error {
	columns := []string{"cloud", "region", "instance_type", "gpu_type"}
	rows := make([]map[string]any, 0, len(offers))
	for _, offer := range offers {
		if row, ok := offer.(map[string]any); ok {
			row["instance_type"] = row["sku"]
			rows = append(rows, row)
		}
	}
	return output.Table(cmd.OutOrStdout(), rows, columns)
}

func printInferenceOffersTable(cmd *cobra.Command, offers []any, tier string) error {
	columns := []string{"gpu_type", "required_vram_gb", "actual_vram_gb", "tensor_parallel_size", "gpu_memory_utilization", "price_per_hour_usd"}
	if tier == "scale" {
		columns = append([]string{"cloud", "region", "instance_type", "node_count"}, columns...)
	}
	rows := make([]map[string]any, 0, len(offers))
	for _, offer := range offers {
		if row, ok := offer.(map[string]any); ok {
			if tier == "scale" {
				row["instance_type"] = row["sku"]
			}
			rows = append(rows, row)
		}
	}
	return output.Table(cmd.OutOrStdout(), rows, columns, output.TableOptions{ShowHourlyPrice: true})
}
