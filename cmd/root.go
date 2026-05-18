package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"nistro-ctl/internal/api"
)

var rootCfg struct {
	server string
	token  string
}

var rootCmd = &cobra.Command{
	Use:           "nistro",
	Short:         "Manage Nistro vLLM services",
	SilenceUsage:  true,
	SilenceErrors: true,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVar(&rootCfg.server, "server", env("NISTRO_SERVER", "http://127.0.0.1:8080"), "Nistro server URL")
	rootCmd.PersistentFlags().StringVar(&rootCfg.token, "token", env("NISTRO_TOKEN", ""), "Omnistrate Provider login JWT")
	rootCmd.AddCommand(vllmCmd, usageCmd, billingCmd)
}

func client() *api.Client {
	return api.NewClient(strings.TrimRight(rootCfg.server, "/"), rootCfg.token)
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
