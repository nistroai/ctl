package cmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"nistro-ctl/internal/api"
	"nistro-ctl/internal/buildinfo"
	postgresproduct "nistro-ctl/internal/products/postgres"
	vllmproduct "nistro-ctl/internal/products/vllm"
)

var rootCfg struct {
	server      string
	token       string
	httpTimeout time.Duration
}

var rootCmd = &cobra.Command{
	Use:               "nistro",
	Short:             "Manage Nistro services",
	Version:           buildinfo.String(),
	SilenceUsage:      true,
	SilenceErrors:     true,
	CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		timeout, err := parseHTTPTimeout(os.Getenv("NISTRO_HTTP_TIMEOUT"))
		if err != nil {
			return err
		}
		rootCfg.httpTimeout = timeout
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
	rootCfg.httpTimeout = api.DefaultHTTPTimeout
	registerProducts(vllmproduct.New(), postgresproduct.New())
	rootCmd.AddCommand(loginCmd, logoutCmd)
}

func client() *api.Client {
	return api.NewClient(strings.TrimRight(rootCfg.server, "/"), rootCfg.token, api.WithTimeout(rootCfg.httpTimeout))
}

func parseHTTPTimeout(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return api.DefaultHTTPTimeout, nil
	}
	timeout, err := time.ParseDuration(value)
	if err != nil || timeout <= 0 {
		return 0, fmt.Errorf("NISTRO_HTTP_TIMEOUT must be a positive duration such as 30s or 2m")
	}
	return timeout, nil
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
