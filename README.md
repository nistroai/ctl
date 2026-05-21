# nistro

Bubble Tea based CLI for Nistro deployments.

```bash
go run . --server http://127.0.0.1:8080 vllm create \
  --model Qwen/Qwen2.5-7B-Instruct \
  --context-length 64K \
  --location US
```

Useful commands:

- `nistro auth signin --email <email> --password <password>` prints an Omnistrate customer JWT for `NISTRO_TOKEN`.
- `nistro vllm list` and `nistro postgres list` show deployments for the authenticated tenant.
- `nistro vllm create --model <hf-model> --location <US|EU|APAC>` shows a provider table sorted by price when cloud and region are not specified, then watches launch sidecar checkpoints live after deployment creation.
- `nistro vllm create --model <hf-model> -y` accepts the cheapest available option without showing the table.
- `nistro vllm create --cloud aws --region us-east-1 --instance-type p5.48xlarge` pins placement when a customer has a provider preference.
- `nistro vllm create --cloud-account-config-id <id>` deploys into a connected customer cloud account.
- `nistro vllm quantizations --model <hf-model>` shows server-detected quantization options.
- `nistro vllm create --gpu-type nvidia-h100 --dedicated` forces whole GPU placement for the requested GPU type.
- `nistro vllm events <deployment-id> --watch` watches launch events.
- `nistro vllm observe <deployment-id>` opens the usage and GPU observability TUI once the server returns the generated vLLM API key.
- `nistro postgres create --vcpu 0.5 --memory 2Gi --storage 50Gi --location US` shows a sorted Postgres offer table before creating.
- `nistro postgres create --vcpu 0.5 --memory 2 --storage 50 -y` accepts the cheapest compatible Postgres offer.
- `nistro postgres events <deployment-id> --watch` watches deployment events.
- `nistro cloud-accounts list` lists connected AWS, GCP, Azure, and Nebius accounts.
- `nistro billing details` shows Omnistrate payment method and portal details.
- `nistro usage current` shows priced usage imported from Omnistrate metering.

Configuration:

- `NISTRO_SERVER` sets the REST API base URL.
- `NISTRO_TOKEN` is the Omnistrate customer JWT sent as the bearer token for server API calls.
- `VLLM_API_KEY` can still override the observe key for manual debugging.
