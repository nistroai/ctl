package cmd

import (
	"encoding/json"
	"os"
	"strings"

	cliproduct "nistro-ctl/internal/product"
)

func registerProducts(products ...cliproduct.CLIProduct) {
	deps := cliproduct.Deps{
		Client:    client,
		Env:       env,
		PrintJSON: printJSON,
		Stdout:    os.Stdout,
		Stderr:    os.Stderr,
	}
	for _, prod := range products {
		if prod != nil {
			rootCmd.AddCommand(prod.Command(deps))
		}
	}
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
