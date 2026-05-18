# nistro

Bubble Tea based CLI for Nistro vLLM deployments.

```bash
go run . --server http://127.0.0.1:8080 vllm create \
  --model Qwen/Qwen2.5-7B-Instruct \
  --context-length 64K \
  --location US \
  --api-key replace-me
```

Useful commands:

- `nistro vllm create --model <hf-model> --location <US|EU|APAC>` shows a provider table sorted by price when cloud and region are not specified, then watches launch sidecar checkpoints live after deployment creation.
- `nistro vllm create --model <hf-model> -y` accepts the cheapest available option without showing the table.
- `nistro vllm create --cloud aws --region us-east-1 --instance-type p5.48xlarge` pins placement when a customer has a provider preference.
- `nistro vllm create --gpu-type nvidia-h100 --dedicated` forces whole GPU placement for the requested GPU type.
- `nistro vllm observe <deployment-id> --api-key <vllm-api-key>` opens the usage and GPU observability TUI.
- `nistro usage current --customer-id <id>` shows priced usage imported from Omnistrate metering.

Configuration:

- `NISTRO_SERVER` sets the REST API base URL.
- `NISTRO_TOKEN` is the Omnistrate Provider login JWT sent as the bearer token for all server API calls.
- `VLLM_API_KEY` is used by create and observe when `--api-key` is omitted.
