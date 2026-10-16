package vllm

import (
	"github.com/spf13/cobra"

	"nistro-ctl/internal/product"
)

type Product struct{}

func New() Product {
	return Product{}
}

func (Product) Name() string {
	return "inference"
}

func (Product) Command(deps product.Deps) *cobra.Command {
	vllmCmd := &cobra.Command{
		Use:   "inference",
		Short: "Manage inference deployments",
	}
	product.AddDiscovery(vllmCmd, deps)
	product.AddMutations(vllmCmd, deps, "/v1/vllm-kubeai/deployments")
	product.AddReads(vllmCmd, deps, "/v1/vllm-kubeai/deployments")
	return vllmCmd
}
