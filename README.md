# nistro

CLI for Nistro inference and Postgres deployments.

Build and run the local binary (requires Go 1.24.2 or newer):

```bash
make build
./dist/nistro-$(go env GOOS)-$(go env GOARCH) --help
```

The binary is written to `dist/nistro-<os>-<arch>` (`.exe` on Windows).
Override the target platform with, for example, `make build GOOS=linux GOARCH=amd64`.

Install the latest published release on macOS, Linux, or Windows (Git Bash):

```bash
curl -fsSL https://raw.githubusercontent.com/nistroai/ctl/main/install.sh | sh
```

The installer verifies the release checksum and places `nistro` in
`~/.nistro/bin`. Install a specific release with:

```bash
curl -fsSL https://raw.githubusercontent.com/nistroai/ctl/main/install.sh | sh -s -- --version v0.1.0
```

Merges to `main` automatically create the next semantic-version tag and publish
archives for Linux, macOS, and Windows on AMD64 and ARM64. The default version
bump is a patch; include `#minor` or `#major` in the merge commit message for a
larger bump. A manually pushed `v*` tag also publishes a release:

```bash
git tag v0.1.0
git push origin v0.1.0
```

Create and update require inputs through flags or `--config-file`; there are no
configuration prompts, confirmation prompts, or automatic event watchers.
Optional settings use the defaults shown in command help. JSON files use
underscore keys (for example `model_url`); flags use hyphens (`--model-url`).
Flags override file values. Use `--config-file -` to read JSON from stdin.

```bash
nistro inference create base --model-url Qwen/Qwen2.5-7B-Instruct --max-model-len 32768 --location US
nistro inference create scale --config-file inference.json
nistro inference update scale <id> --replicas 2
nistro postgres create base --vcpu 2 --storage-gib 50 --location US
nistro postgres create scale --cloud aws --region us-east-1 --instance-type m7i.large --storage-gib 100 --location US --replicas 1
nistro postgres update base <id> --vcpu 4 --storage-gib 100
nistro postgres update scale <id> --replicas 2 --parameters work_mem=32MB
```

Required create inputs:

| Product/tier | Required fields |
| --- | --- |
| Inference Base | `model_url`, `max_model_len`, `location` |
| Inference Scale | Base fields plus `cloud`, `region`, `instance_type`, `replicas` (1–8) |
| Postgres Base | `vcpu`, `storage_gib`, `location`; memory is calculated at 4 GiB per vCPU |
| Postgres Scale | `storage_gib`, `location`, `cloud`, `region`, `instance_type`; `replicas` is optional (0–8 standbys, default 1); CPU/memory come from the matching offer |

Inference supports Scale replica updates. Postgres Base supports CPU/storage
updates; Scale supports storage, PostgreSQL version, replicas, superuser access,
and parameters. Provide at least one update field. The CLI checks the deployment
tier before updating; the server validates expansions and version upgrades.

Inspect model and placement options before selecting create/update inputs:

```bash
nistro inference offers base --model-url Qwen/Qwen2.5-7B-Instruct --max-model-len 32768 --location US
nistro inference offers scale --model-url Qwen/Qwen2.5-7B-Instruct --max-model-len 32768 --location US -o json
nistro postgres placements scale
nistro postgres offers base --vcpu 2 --storage-gib 50 --location US
nistro postgres offers scale --storage-gib 100 --cloud aws --region us-east-1 --replicas 2 -o json
```

Discovery commands accept only the fields used by the website:

| Command | Inputs |
| --- | --- |
| Inference offers Base/Scale | Model URL/ID, context length, location, optional quantization and GPU type |
| Postgres offers Base | vCPU, storage, location; memory is calculated at 4 GiB per vCPU |
| Postgres offers Scale | Required storage, cloud, and region; optional instance type and standby replicas (0–8, plus one primary); compute is resolved from placement |
| Postgres placements Scale | No inputs; lists available clouds, regions, and instance types |

Inference and Postgres offers also accept `--config-file`.
Inference offer tables show GPU, required VRAM, actual GPU VRAM, tensor parallel
size, estimated hourly price, and GPU memory utilization. Scale also shows cloud,
region, and instance type. Postgres offer tables omit source; Scale also omits
requested CPU and memory. Postgres offers and create/update responses omit
`estimated_price_per_month_usd` in both table and JSON output.

JSON output preserves other API fields and contains no progress messages.
Placements uses `/v1/postgres/scale/catalog`. Inference placement choices come
from inference offers; there is no separate inference placement API.

Useful commands:

- `nistro login` prompts for email and password, then saves a session for the selected server in `~/.nistro/sessions.json` with owner-only file permissions. Password input is masked. Use `--email` and/or `--password` to skip the corresponding prompts.
- `nistro login --email <email> --password-stdin` reads the password from stdin, for example `printf '%s' "$NISTRO_PASSWORD" | nistro login --email "$NISTRO_EMAIL" --password-stdin`.
- `nistro logout` clears the saved session for the selected server.
- `nistro inference list` and `nistro postgres list` show deployments for the authenticated tenant.
- `nistro inference describe <id>` and `nistro postgres describe <id>` show deployment details.
- `nistro inference delete <id>` and `nistro postgres delete <id>` request deployment deletion.
- List, describe, and delete use the same table formatter as `nistro-ctl`: lists show only summary fields, details redact credentials, and `--output json` returns the complete API response.

Configuration:

- The default REST API server is production: `https://api.nistro.ai`. Override it with `--server` or `NISTRO_SERVER` (for example, `http://127.0.0.1:8080` for local development).
- Commands use the session saved by `nistro login`. `NISTRO_TOKEN` can override it for automation. Logout clears the saved session; unset `NISTRO_TOKEN` to stop using that override.
- `NISTRO_EMAIL` and `NISTRO_PASSWORD` can supply login credentials instead of flags.
