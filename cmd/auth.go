package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"nistro-ctl/internal/api"
)

var loginCfg struct {
	email         string
	password      string
	passwordStdin bool
}

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Sign in and save your session",
	Example: `  nistro login
  printf '%s' "$NISTRO_PASSWORD" | nistro login --email "$NISTRO_EMAIL" --password-stdin`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		email := firstNonEmpty(loginCfg.email, env("NISTRO_EMAIL", ""))
		password := firstNonEmpty(loginCfg.password, env("NISTRO_PASSWORD", ""))
		if loginCfg.passwordStdin {
			if email == "" {
				return errors.New("--email or NISTRO_EMAIL is required with --password-stdin")
			}
			data, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 65537))
			if err != nil {
				return fmt.Errorf("read password from stdin: %w", err)
			}
			if len(data) > 65536 {
				return errors.New("password input exceeds 64 KiB")
			}
			password = strings.TrimRight(string(data), "\r\n")
			if password == "" {
				return errors.New("password from stdin is empty")
			}
		}
		email, password, err := loginCredentials(cmd, email, password)
		if err != nil {
			return err
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
		if err := saveSession(rootCfg.server, resp.Token); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Logged in successfully.")
		return nil
	},
}

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Clear your saved session for this server",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := saveSession(rootCfg.server, ""); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Logged out. Saved session cleared.")
		if rootCfg.token != "" {
			fmt.Fprintln(cmd.OutOrStdout(), "NISTRO_TOKEN is still configured; unset it to stop using that token.")
		}
		return nil
	},
}

func init() {
	loginCmd.Flags().StringVar(&loginCfg.email, "email", "", "Nistro account email")
	loginCmd.Flags().StringVar(&loginCfg.password, "password", "", "Nistro account password")
	loginCmd.Flags().BoolVar(&loginCfg.passwordStdin, "password-stdin", false, "Read the password from stdin")
	loginCmd.MarkFlagsMutuallyExclusive("password", "password-stdin")
}

func loginCredentials(cmd *cobra.Command, email, password string) (string, string, error) {
	if email != "" && password != "" {
		return email, password, nil
	}
	if err := requireLoginTerminal(cmd); err != nil {
		return "", "", err
	}
	if email == "" {
		field := huh.NewInput().Title("Please enter your email:").Placeholder("Email").Value(&email).Validate(func(value string) error {
			if strings.TrimSpace(value) == "" {
				return errors.New("email is required")
			}
			return nil
		})
		if err := loginForm(cmd, field); err != nil {
			return "", "", err
		}
		email = strings.TrimSpace(email)
	}
	if password == "" {
		field := huh.NewInput().Title("Please enter your password:").Placeholder("Password").EchoMode(huh.EchoModePassword).Value(&password).Validate(func(value string) error {
			if value == "" {
				return errors.New("password is required")
			}
			return nil
		})
		if err := loginForm(cmd, field); err != nil {
			return "", "", err
		}
	}
	return email, password, nil
}

func requireLoginTerminal(cmd *cobra.Command) error {
	input, ok := cmd.InOrStdin().(*os.File)
	if !ok || !term.IsTerminal(input.Fd()) {
		return errors.New("interactive login requires a terminal; use --email with --password-stdin, set NISTRO_EMAIL and NISTRO_PASSWORD")
	}
	return nil
}

func loginForm(cmd *cobra.Command, fields ...huh.Field) error {
	return huh.NewForm(huh.NewGroup(fields...)).WithInput(cmd.InOrStdin()).WithOutput(cmd.ErrOrStderr()).RunWithContext(cmd.Context())
}
