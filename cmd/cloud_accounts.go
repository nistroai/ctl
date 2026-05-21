package cmd

import (
	"context"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"nistro-ctl/internal/api"
)

var cloudAccountsCmd = &cobra.Command{
	Use:   "cloud-accounts",
	Short: "Manage connected cloud accounts",
}

var cloudAccountsListCfg struct {
	cloud string
}

var cloudAccountsConnectCfg struct {
	cloud               string
	cloudProviderID     string
	name                string
	description         string
	awsAccountID        string
	awsBootstrapRoleARN string
	gcpProjectID        string
	gcpProjectNumber    string
	gcpServiceAccount   string
	azureSubscriptionID string
	azureTenantID       string
	nebiusTenantID      string
}

var cloudAccountsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List connected cloud accounts",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
		defer cancel()
		accounts, err := client().CloudAccounts(ctx, cloudAccountsListCfg.cloud)
		if err != nil {
			return err
		}
		printJSON(accounts)
		return nil
	},
}

var cloudAccountsConnectCmd = &cobra.Command{
	Use:   "connect",
	Short: "Connect a cloud account",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
		defer cancel()
		resp, err := client().CreateCloudAccount(ctx, api.CloudAccountRequest{
			CloudProvider:       strings.ToLower(strings.TrimSpace(cloudAccountsConnectCfg.cloud)),
			CloudProviderID:     cloudAccountsConnectCfg.cloudProviderID,
			Name:                cloudAccountsConnectCfg.name,
			Description:         cloudAccountsConnectCfg.description,
			AWSAccountID:        cloudAccountsConnectCfg.awsAccountID,
			AWSBootstrapRoleARN: cloudAccountsConnectCfg.awsBootstrapRoleARN,
			GCPProjectID:        cloudAccountsConnectCfg.gcpProjectID,
			GCPProjectNumber:    cloudAccountsConnectCfg.gcpProjectNumber,
			GCPServiceAccount:   cloudAccountsConnectCfg.gcpServiceAccount,
			AzureSubscriptionID: cloudAccountsConnectCfg.azureSubscriptionID,
			AzureTenantID:       cloudAccountsConnectCfg.azureTenantID,
			NebiusTenantID:      cloudAccountsConnectCfg.nebiusTenantID,
		})
		if err != nil {
			return err
		}
		printJSON(resp)
		return nil
	},
}

func init() {
	cloudAccountsListCmd.Flags().StringVar(&cloudAccountsListCfg.cloud, "cloud", "", "Cloud filter: aws, gcp, azure, or nebius")

	cloudAccountsConnectCmd.Flags().StringVar(&cloudAccountsConnectCfg.cloud, "cloud", "", "Cloud provider: aws, gcp, azure, or nebius")
	cloudAccountsConnectCmd.Flags().StringVar(&cloudAccountsConnectCfg.cloudProviderID, "cloud-provider-id", "", "Omnistrate cloud provider ID override")
	cloudAccountsConnectCmd.Flags().StringVar(&cloudAccountsConnectCfg.name, "name", "", "Display name")
	cloudAccountsConnectCmd.Flags().StringVar(&cloudAccountsConnectCfg.description, "description", "", "Description")
	cloudAccountsConnectCmd.Flags().StringVar(&cloudAccountsConnectCfg.awsAccountID, "aws-account-id", "", "AWS account ID")
	cloudAccountsConnectCmd.Flags().StringVar(&cloudAccountsConnectCfg.awsBootstrapRoleARN, "aws-bootstrap-role-arn", "", "AWS bootstrap role ARN")
	cloudAccountsConnectCmd.Flags().StringVar(&cloudAccountsConnectCfg.gcpProjectID, "gcp-project-id", "", "GCP project ID")
	cloudAccountsConnectCmd.Flags().StringVar(&cloudAccountsConnectCfg.gcpProjectNumber, "gcp-project-number", "", "GCP project number")
	cloudAccountsConnectCmd.Flags().StringVar(&cloudAccountsConnectCfg.gcpServiceAccount, "gcp-service-account-email", "", "GCP service account email")
	cloudAccountsConnectCmd.Flags().StringVar(&cloudAccountsConnectCfg.azureSubscriptionID, "azure-subscription-id", "", "Azure subscription ID")
	cloudAccountsConnectCmd.Flags().StringVar(&cloudAccountsConnectCfg.azureTenantID, "azure-tenant-id", "", "Azure tenant ID")
	cloudAccountsConnectCmd.Flags().StringVar(&cloudAccountsConnectCfg.nebiusTenantID, "nebius-tenant-id", "", "Nebius tenant ID")
	_ = cloudAccountsConnectCmd.MarkFlagRequired("cloud")

	cloudAccountsCmd.AddCommand(cloudAccountsListCmd, cloudAccountsConnectCmd)
}
