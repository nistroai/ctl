package postgres

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"nistro-ctl/internal/product"
)

type Product struct{}

func New() Product {
	return Product{}
}

func (Product) Name() string {
	return "postgres"
}

func (Product) Command(deps product.Deps) *cobra.Command {
	postgresCmd := &cobra.Command{
		Use:   "postgres",
		Short: "Manage Postgres deployments",
	}
	product.AddDiscovery(postgresCmd, deps)
	product.AddMutations(postgresCmd, deps, "/v1/postgres/deployments")
	product.AddReads(postgresCmd, deps, "/v1/postgres/deployments")
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
