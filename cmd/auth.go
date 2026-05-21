package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"nistro-ctl/internal/api"
)

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Authenticate to Nistro",
}

var signinCfg struct {
	email    string
	password string
}

var authSigninCmd = &cobra.Command{
	Use:   "signin",
	Short: "Sign in and print an Omnistrate customer JWT",
	RunE: func(cmd *cobra.Command, args []string) error {
		email := strings.TrimSpace(firstNonEmpty(signinCfg.email, env("NISTRO_EMAIL", "")))
		password := strings.TrimSpace(firstNonEmpty(signinCfg.password, env("NISTRO_PASSWORD", "")))
		if email == "" || password == "" {
			return errors.New("email and password are required")
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
		defer cancel()
		resp, err := api.NewClient(strings.TrimRight(rootCfg.server, "/"), "").SignIn(ctx, api.SignInRequest{
			Email:    email,
			Password: password,
		})
		if err != nil {
			return err
		}
		if resp.Token == "" {
			return errors.New("server did not return an Omnistrate customer JWT")
		}
		fmt.Fprintln(cmd.OutOrStdout(), resp.Token)
		return nil
	},
}

func init() {
	authSigninCmd.Flags().StringVar(&signinCfg.email, "email", "", "Nistro account email")
	authSigninCmd.Flags().StringVar(&signinCfg.password, "password", "", "Nistro account password")
	authCmd.AddCommand(authSigninCmd)
}
