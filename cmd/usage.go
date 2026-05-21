package cmd

import (
	"context"
	"time"

	"github.com/spf13/cobra"
)

var usageCmd = &cobra.Command{
	Use:   "usage",
	Short: "Show usage and pricing",
}

var usageCurrentCmd = &cobra.Command{
	Use:   "current",
	Short: "Show current month usage",
	RunE: func(cmd *cobra.Command, args []string) error {
		period := time.Now().UTC().Format("2006-01")
		return showUsage(cmd, period)
	},
}

var usageMonthCmd = &cobra.Command{
	Use:   "month <yyyy-mm>",
	Short: "Show usage for a billing month",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return showUsage(cmd, args[0])
	},
}

func init() {
	usageCmd.AddCommand(usageCurrentCmd, usageMonthCmd)
}

func showUsage(cmd *cobra.Command, period string) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()
	rows, err := client().Usage(ctx, period)
	if err != nil {
		return err
	}
	printJSON(map[string]any{"period": period, "usage": rows})
	return nil
}
