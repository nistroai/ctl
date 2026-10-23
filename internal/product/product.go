package product

import (
	"io"

	"github.com/spf13/cobra"

	"nistro-ctl/internal/api"
)

type Deps struct {
	Client    func() *api.Client
	Env       func(string, string) string
	PrintJSON func(any)
	Stdout    io.Writer
	Stderr    io.Writer
}

type CLIProduct interface {
	Name() string
	Command(Deps) *cobra.Command
}

func tierLabel(tier string) string {
	switch tier {
	case "on-demand":
		return "On-Demand"
	case "scale":
		return "Scale"
	default:
		return tier
	}
}
