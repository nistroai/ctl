package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"nistro-ctl/internal/api"
	"nistro-ctl/internal/buildinfo"
	postgresproduct "nistro-ctl/internal/products/postgres"
	vllmproduct "nistro-ctl/internal/products/vllm"
)

var rootCfg struct {
	server string
	token  string
}

var rootCmd = &cobra.Command{
	Use:               "nistro",
	Short:             "Manage Nistro services",
	Version:           buildinfo.String(),
	SilenceUsage:      true,
	SilenceErrors:     true,
	CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		if cmd == loginCmd || cmd == logoutCmd || cmd.Name() == "completion" || cmd.Parent() != nil && cmd.Parent().Name() == "completion" {
			return nil
		}
		if rootCfg.token != "" {
			return nil
		}
		token, err := loadSession(rootCfg.server)
		if err != nil {
			return err
		}
		rootCfg.token = token
		return nil
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.SetVersionTemplate("nistro {{.Version}}\n")
	rootCmd.PersistentFlags().StringVar(&rootCfg.server, "server", env("NISTRO_SERVER", "https://api.nistro.ai"), "Nistro server URL")
	rootCfg.token = env("NISTRO_TOKEN", "")
	registerProducts(vllmproduct.New(), postgresproduct.New())
	rootCmd.AddCommand(loginCmd, logoutCmd)
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
