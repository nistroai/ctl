package product

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"nistro-ctl/internal/output"
)

func AddReads(parent *cobra.Command, deps Deps, path string) {
	var format string
	parent.PersistentFlags().StringVarP(&format, "output", "o", "table", "Output format: table or json")
	for _, name := range []string{"list", "describe", "delete"} {
		use, count := name, 0
		if name != "list" {
			use, count = name+" ID", 1
		}
		summary := map[string]string{"list": "List deployments across tiers", "describe": "Describe a deployment", "delete": "Delete a deployment"}[name]
		command := &cobra.Command{Use: use, Short: summary, Args: cobra.ExactArgs(count)}
		command.RunE = func(cmd *cobra.Command, args []string) error {
			if format != "table" && format != "json" {
				return fmt.Errorf("output must be table or json")
			}
			resourcePath := path
			if len(args) > 0 {
				id := args[0]
				if strings.TrimSpace(id) == "" || strings.ContainsAny(id, "/\\?#") || id == "." || id == ".." {
					return fmt.Errorf("invalid resource ID %q", id)
				}
				resourcePath += "/" + url.PathEscape(id)
			}
			method := "GET"
			if name == "delete" {
				method = "DELETE"
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			messages := map[string][2]string{
				"list":     {"Loading deployments", "Loaded deployments"},
				"describe": {"Loading deployment details", "Loaded deployment details"},
				"delete":   {"Deleting deployment", "Deployment deletion request accepted"},
			}
			var progress *progressLine
			if format != "json" {
				progress = startProgress(cmd.ErrOrStderr(), messages[name][0])
			}
			raw, err := deps.Client().Resource(ctx, method, resourcePath, nil)
			if progress != nil {
				progress.finish(messages[name][1], err == nil)
			}
			if err != nil {
				return err
			}
			return output.Print(cmd.OutOrStdout(), raw, format)
		}
		parent.AddCommand(command)
	}
}
