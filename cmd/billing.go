package cmd

import (
	"context"
	"time"

	"github.com/spf13/cobra"
)

var billingCmd = &cobra.Command{
	Use:   "billing",
	Short: "Manage billing",
}

var billingCustomerID string

var billingStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show billing status",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
		defer cancel()
		status, err := client().BillingStatus(ctx, firstNonEmpty(billingCustomerID, env("NISTRO_CUSTOMER_ID", "")))
		if err != nil {
			return err
		}
		printJSON(status)
		return nil
	},
}

var billingPublishStatusCmd = &cobra.Command{
	Use:   "publish-status",
	Short: "Show month-end publish status through usage records",
	RunE: func(cmd *cobra.Command, args []string) error {
		return showUsage(cmd, time.Now().UTC().AddDate(0, -1, 0).Format("2006-01"))
	},
}

func init() {
	billingCmd.PersistentFlags().StringVar(&billingCustomerID, "customer-id", "", "Customer ID")
	billingCmd.AddCommand(billingStatusCmd, billingPublishStatusCmd)
}
