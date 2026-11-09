# CLI binary test framework

The framework runs `nistro` with Go's `os/exec`, then asserts its exit code,
stdout, stderr, and persisted session behavior. Tests never import `cmd` or
the CLI's internal packages. This validates the executable users install,
including command registration, flag parsing, HTTP requests, and formatting.

| Suite | Server | Coverage | When it runs |
| --- | --- | --- | --- |
| `test/integration_test` | Local `httptest.Server` | Help/version, login/logout, session isolation and token overrides, JSON/table output, errors, validation, discovery, and create/update/delete for both products | Normal Go tests on PRs/merge queue; published binaries on release runners |
| `TestProductionSmoke` in `test/smoke_test` | Configured Nistro API, production by default | Released version, password login and saved session, deployment list/describe, Postgres placement catalog, hardware recommendations, and pricing offers for both tiers | Stable CLI releases, reusable workflow calls, and manual Actions runs |
| `TestProductionCanary` in `test/smoke_test` | Configured Nistro API plus newly provisioned data endpoints | Create/list/describe/readiness, real inference and SQL, all supported updates, post-update data checks, delete and confirmation for both products and tiers | Four separate canary jobs after smoke checks; explicitly enabled local runs |

The smoke suite uses read and discovery operations. The canary suite additionally
provisions dedicated deployments through the installed CLI, makes real inference
requests and PostgreSQL queries, checks supported updates, and deletes its
deployments. Normal PR tests use local fixtures; live suites require explicit
enablement and a dedicated account with provisioning permissions.

## README commands first

The initial automation contract is the repository's `README.md`.
`TestREADMECommands` reads its CLI examples from the checkout and executes the
actual binary with their documented flags and values. It currently runs all 13
product command lines and covers all 21 documented command paths through those
examples plus the useful-command and authentication scenarios.

The README suite checks:

- The seven create/update examples, including flag-only creates, the relative
  `inference.json` file, replica updates, and Postgres parameters.
- The six discovery examples with the README's context length, placements,
  replica counts, and default table/explicit JSON outputs.
- Login through environment credentials, flags, and stdin; session persistence,
  logout, token overrides, and server selection.
- List/describe/delete for both products, with JSON API-field preservation and
  table credential redaction.
- File and stdin configuration for all five discovery commands supporting
  `--config-file`, with flags overriding file values.
- The documented binary help command.

Deployment IDs, credentials, and `inference.json` are supplied by isolated local
fixtures. Commands are passed as process arguments, without evaluating README
text in a shell. A documented invocation missing a successful test fails the
suite. Interactive terminal prompts are outside this initial automation suite;
login uses the README's noninteractive alternatives.

Run this first scope from the repository root:

```bash
make test-readme
NISTRO_TEST_BINARY=/absolute/path/to/nistro make test-readme
```

PR and release checks include this suite through the existing `go test` commands.
The broader all-command guard below also covers the generated `help` command,
which is why its executable-command count is 22 rather than the README's 21.

## Every available command

`TestAllAvailableCommands` discovers the command tree from the actual binary's
help output and runs the behavior scenarios below. It verifies successful
execution of each command, not just its help text. All product commands are
checked in both JSON and table formats, including default table output for
create/update/delete.

| Command family | Executable commands covered |
| --- | --- |
| General | `help`, `login`, `logout`; root `--version` and `--help` |
| Inference | `create on-demand`, `create scale`, `update scale`, `list`, `describe`, `delete`, `offers on-demand`, `offers scale`, `recommend-hardware scale` |
| Postgres | `create on-demand`, `create scale`, `update on-demand`, `update scale`, `list`, `describe`, `delete`, `offers on-demand`, `offers scale`, `placements scale` |

This currently covers 22 public executable commands and navigation for all 11
command groups, including the root. The guard fails when a command is added or
removed without updating the scenario registry. Each registered scenario must
actually execute its commands successfully in the required output formats.
Help-only invocations and expected error tests do not satisfy the guard.

Auth tests check stdin and password flags, persistence across processes,
per-server sessions, token overrides, and logout. Mutation scenarios check all
four create tiers and all three supported update tiers against local fixtures,
including Postgres Scale offer selection and its update settings. Discovery
tests check exact requests, table columns, price visibility, and config overrides.

## Run locally

From the repository root:

```bash
# Existing unit tests and local binary contract tests; live tests skip.
make test

# Build the CLI once in TestMain and run only binary contract tests.
make test-binary

# Run only README examples and documented authentication/config behavior.
make test-readme

# Exercise a specific existing executable instead of building source.
NISTRO_TEST_BINARY=/absolute/path/to/nistro make test-binary
```

Run live smoke tests against an installed release:

```bash
sh install.sh --version v0.1.0 --install-dir "$PWD/dist/smoke-bin" --no-modify-path
export NISTRO_TEST_BINARY="$PWD/dist/smoke-bin/nistro"
export NISTRO_TEST_EXPECTED_VERSION=v0.1.0
export NISTRO_SERVER=https://api.nistro.ai
export NISTRO_EMAIL=cli-smoke@example.com
read -r -s NISTRO_PASSWORD
export NISTRO_PASSWORD
make smoke-test
unset NISTRO_PASSWORD
```

Use a release that includes this framework and a real dedicated test account.
On Windows, select `nistro.exe` and run from Git Bash. A token can replace the
email/password pair via `NISTRO_TOKEN`; this mode explicitly skips password login
coverage. With email/password configured, the suite logs in through
`--password-stdin` and subsequent subprocesses use the saved session, even if
the parent shell has a token. The password is never passed as a process argument.

Every CLI gets a temporary home and config directory. Tests do not use or clear
your normal `~/.nistro` sessions. The helper enforces a command timeout and
redacts known credentials and saved session tokens from failure diagnostics.
Tests assert API JSON without printing it because deployment details can
contain credentials.


## Deployment lifecycle canaries

The reusable workflow enables `run-canaries` by default. After the smoke suite
passes, four separate jobs exercise these lifecycles with the same exact
installed release:

| Case | Default create | Supported update |
| --- | --- | --- |
| Postgres On-Demand | 2 vCPU, 50 GiB, location `ANY`, unique database/user/password | Increase storage by 10 GiB |
| Postgres Scale | A placement from the live catalog, 50 GiB, one standby, unique database/user/password | Increase the standby count |
| Inference On-Demand | `Qwen/Qwen2.5-0.5B-Instruct`, context length 2048, location `ANY` | This tier has no CLI update command |
| Inference Scale | The same small model, live hardware recommendation, one replica | Increase the replica count |

Each case runs through the CLI binary:

1. List existing deployments, create a dedicated resource, and capture its ID.
   A returned ID already present in the initial list is rejected without mutation
   or cleanup of that existing resource.
2. Require the new ID to appear in list/describe and poll provisioning status.
   Terminal failures fail immediately; transient availability and serving warmup
   have bounded retries. Idle On-Demand inference is tested by making a request.
3. Verify the data endpoint. Inference requires the configured model's advertised
   identity (or an explicit exact runtime alias), then sends a real chat-completion
   request, requiring the returned model to match the selected identity, generated
   text, and positive token usage whose total equals prompt plus completion
   tokens. PostgreSQL creates a uniquely named table in the initial connection's
   schema and commits a marker using parameterized SQL. The fully qualified
   table name remains stable across later `search_path` changes.
4. Compare the requested update with the effective initial deployment settings
   and reject an update that changes no value. Run the supported update, require
   the reported target values, and poll readiness again. A timestamp or updating
   status alone cannot prove an applied update. Reconnect to PostgreSQL and read
   the same committed marker; repeat inference after its replica update. When parameter
   overrides are supplied, verify them with `current_setting`, including resetting
   keys removed by a replacement parameter set.
5. Remove the SQL checkpoint, request deployment deletion, and confirm both
   describe deletion/not-found and absence from list. Hiding a still-deleting
   deployment from list alone cannot pass this check.

Cleanup is registered as soon as a new ID is accepted and uses an independent
five-minute deadline after a test failure. It deletes only the owned ID, before
logging out. Created IDs are recorded in test output. A hard runner termination
or forced cancellation can prevent cleanup; inspect those IDs if a run is
interrupted. These canaries create billable CPU/GPU/storage resources, so use a
dedicated test account with capacity and provisioning permissions.

Each workflow case has a 30-minute lifecycle deadline, a 40-minute Go test
deadline, and a 45-minute runner deadline, leaving time for cleanup. Missing
credentials, failed prerequisites, malformed configuration, and a selected
release that lacks the requested suite fail instead of producing an empty pass.

For a local run, select an installed binary and credentials as above, then run:

```bash
# All four cases, with a separate deadline for each.
make canary-test

# One case while validating account configuration.
NISTRO_CANARY_CASES=postgres/on-demand make canary-test
```

`ENABLE_CANARY_TEST=true` is required when calling `go test` directly. The
optional local settings are `NISTRO_CANARY_TIMEOUT` (30m),
`NISTRO_CANARY_CLEANUP_TIMEOUT` (5m), `NISTRO_CANARY_POLL_INTERVAL` (10s),
`NISTRO_CANARY_PROBE_TIMEOUT` (90s), `NISTRO_CANARY_MODEL`, and
`NISTRO_CANARY_LOCATION` (US). Keep Go's overall deadline longer than all selected
lifecycles and their cleanup. The Make target reserves 150 minutes for all four.
The workflow also accepts `NISTRO_CANARY_MODEL`, `NISTRO_CANARY_LOCATION`, and
`NISTRO_CANARY_SERVED_MODEL` as environment variables. Use the served-model
override when the runtime advertises another alias.

Create overrides merge with generated defaults. Update overrides replace the
default update and must change at least one effective setting, including values
injected by the CLI or platform. Equivalent PostgreSQL units such as `32MB` and
`32768kB` do not count as a change. Integer settings are compared exactly after
unit normalization, so adjacent large values remain distinct. Supply each
override as one JSON object in its optional environment secret:

| GitHub secret / local environment variable | Example override |
| --- | --- |
| `NISTRO_CANARY_POSTGRES_ON_DEMAND_CREATE_CONFIG` | `{"vcpu":1,"storage_gib":20,"location":"US"}` |
| `NISTRO_CANARY_POSTGRES_ON_DEMAND_UPDATE_CONFIG` | `{"vcpu":2,"storage_gib":30}` |
| `NISTRO_CANARY_POSTGRES_SCALE_CREATE_CONFIG` | `{"cloud":"aws","region":"us-east-1","instance_type":"m7i.large","replicas":0}` |
| `NISTRO_CANARY_POSTGRES_SCALE_UPDATE_CONFIG` | `{"replicas":1,"parameters":{"work_mem":"32MB"}}` |
| `NISTRO_CANARY_INFERENCE_ON_DEMAND_CREATE_CONFIG` | `{"model_url":"Qwen/Qwen2.5-0.5B-Instruct","max_model_len":2048}` |
| `NISTRO_CANARY_INFERENCE_SCALE_CREATE_CONFIG` | `{"cloud":"aws","region":"us-east-1","instance_type":"<available SKU>"}` |
| `NISTRO_CANARY_INFERENCE_SCALE_UPDATE_CONFIG` | `{"replicas":2}` |

PostgreSQL `parameters` updates replace the custom parameter set. Omitting this
field leaves parameters unchanged; supplying `{}` removes the custom set. When
both create parameters and a replacement update are configured, the canary first
creates with platform defaults and records the relevant SQL settings. It then
applies and verifies the requested initial custom parameters through a setup
update before testing the replacement. This supplies trusted reset expectations
for removed keys instead of assuming built-in PostgreSQL defaults. The SQL
checkpoint remains present throughout these updates.

Setting comparisons accept equivalent units and scientific notation. Integers
compare exactly; real values allow a `1e-9` relative rounding tolerance without
an absolute minimum. Zero only matches zero, so tiny parameter changes remain
detectable both before and after an update.

Generated configuration files are owner-readable and never printed. Inference
uses the documented `/openai/v1` API base path and HTTPS, allowing HTTP only for
loopback test fixtures. PostgreSQL verifies TLS certificates and hostnames with
no plaintext fallback. If the database uses a private CA, configure the optional
`CANARY_POSTGRES_CA_CERT` PEM secret; the workflow writes it to a temporary trust
file. Locally, set `NISTRO_CANARY_POSTGRES_SSLROOTCERT` to that PEM file's path.

PR checks also run the canary implementation against local HTTP fixtures and a
disposable TLS-enabled PostgreSQL 18 container. Run the real SQL fixture checks
locally with Docker and OpenSSL:

```bash
bash test/resources/run-postgres-canary-fixtures.sh
```

The script binds only to loopback, checks both PostgreSQL tiers and checkpoint
failure cases, and cleans its container and temporary certificates. No Nistro
account or production endpoint is needed for these checks.

## Production account configuration

Create a GitHub environment named `production` in the repository running the
workflow and configure these secrets:

| Secret | Purpose |
| --- | --- |
| `SMOKE_TEST_EMAIL` and `SMOKE_TEST_PASSWORD` | Recommended: exercise login and session persistence as well as authenticated commands |
| `SMOKE_TEST_TOKEN` | Alternative authentication; skips password login coverage |
| `CLI_REPOSITORY_TOKEN` | Optional read access to `nistroai/ctl` for a caller whose token cannot access it |

Enabled smoke runs fail if authentication is absent or incomplete. Credentials
need access to both product lists, discovery endpoints, and deployment
create/update/delete operations for the lifecycle canaries. An empty deployment
list is valid. To require describe coverage even for a new test account, keep
dedicated existing fixtures and configure the optional variables below.

| GitHub environment variable / local environment variable | Default / purpose |
| --- | --- |
| `NISTRO_SMOKE_INFERENCE_ID` | Describe this inference deployment; otherwise use the first listed deployment or skip describe if none exists |
| `NISTRO_SMOKE_POSTGRES_ID` | Describe this Postgres deployment; same fallback as inference |
| `NISTRO_SMOKE_MODEL` | `Qwen/Qwen2.5-7B-Instruct`; public model used for inference discovery |
| `NISTRO_SMOKE_MAX_MODEL_LEN` | `4096`; model context length |
| `NISTRO_SMOKE_LOCATION` | `US`; location for Inference Scale recommendations (`US`, `EU`, or `ASIA`) |

The suite selects a Postgres placement from the live catalog and an inference
cloud/region/SKU from live hardware recommendations. It avoids pinning hardware
that may disappear from the catalog. On-Demand pricing uses location `ANY`.

For local runs, set `ENABLE_SMOKE_TEST=true`, `NISTRO_TEST_BINARY`, and either
`NISTRO_EMAIL`/`NISTRO_PASSWORD` or `NISTRO_TOKEN`. `NISTRO_SERVER` defaults to
`https://api.nistro.ai`; `NISTRO_TEST_EXPECTED_VERSION` optionally asserts an exact
version. The Actions workflow always asserts the selected release version.

## GitHub Actions

Publishing a draft release starts the existing Release workflow, which marks
the release as a prerelease while validating it. The workflow installs and tests
the uploaded binary on Linux AMD64/ARM64, macOS, and Windows. Installer, binary
contract, and container checks must succeed before promotion to stable/latest.
After promotion, every stable version tag calls `.github/workflows/smoke-tests.yml`
against production, including all four lifecycle canaries. Tags with a prerelease
suffix still run binary contract tests and can run live tests manually.

The smoke workflow resolves `latest` once to an exact published tag, checks out
that tag's tests, and installs its checksum-verified release archive. It never
builds a substitute binary. Failed tests fail the job, and Go JSON results are
uploaded even on test failure. `-count=1` prevents Go's cache from reusing a prior
successful production result. Each canary matrix job uses its own isolated session and deployment IDs.
Runs may overlap, so GitHub concurrency groups cannot replace a pending
production release check.

The release pipeline calls the workflow directly after promotion because
releases updated with `GITHUB_TOKEN` do not trigger another workflow listening
for `release: published`. Smoke failure marks the release workflow failed;
the already promoted release remains available.

To validate the CLI after **backend production releases**, add a dependent job
to that repository's deployment workflow:

```yaml
jobs:
  # Your existing deployment job is named deploy-production in this example.
  cli-smoke:
    needs: deploy-production
    permissions:
      contents: read
    uses: nistroai/ctl/.github/workflows/smoke-tests.yml@main
    with:
      environment: production
      cli-version: latest
      server: https://api.nistro.ai
      run-canaries: true
    secrets:
      SMOKE_TEST_EMAIL: ${{ secrets.SMOKE_TEST_EMAIL }}
      SMOKE_TEST_PASSWORD: ${{ secrets.SMOKE_TEST_PASSWORD }}
```

Pin the workflow reference and `cli-version` to a release tag when you want a
fixed test version. The workflow explicitly checks out `nistroai/ctl`, so a
backend caller does not accidentally run its own repository's tests. Configure
the `production` environment in the caller repository, or pass repository
secrets as shown. If `nistroai/ctl` is private, allow reusable workflow access
from that caller and provide `CLI_REPOSITORY_TOKEN` with repository read access;
the existing installer also needs access to the release download URLs.

For a manual rerun, open **Actions → CLI Smoke Tests → Run workflow** and select
the environment, API server, and release tag. `run-canaries` defaults to true;
turn it off for a read/discovery-only rerun. No periodic schedule is required.

## Add scenarios

Add local API contract scenarios to `test/integration_test` and live read/discovery
scenarios to `test/smoke_test`. Reuse `testutils.New` to keep sessions isolated:

```go
cli := testutils.New(t, testutils.Options{
    Binary: binary,
    Server: server.URL, // httptest.Server for local contract coverage
})
result := cli.MustRun(t, "", "postgres", "list", "-o", "json")
// Decode result.Stdout and assert the behavior. Never log raw deployment JSON.
```

Use `cli.Run` when a nonzero exit is expected. Use `cli.Redact` before including
command output in diagnostics. Register new command behavior in
`commandScenarios` in `test/integration_test/command_coverage_test.go` and use
`newCLI` there so successful execution is recorded by the coverage guard.
Add lifecycle scenarios to `TestProductionCanary` with dedicated resources and
bounded cleanup. Keep local fixture coverage for expected failures and request
contracts.

Reference docs: [CLI](https://dev.nistro.ai/docs/cli),
[Postgres](https://dev.nistro.ai/docs/postgres), and
[Inference](https://dev.nistro.ai/docs/inference). The current CLI requires
`--location` for `inference recommend-hardware scale`; the CLI docs currently
show a stale `--cloud`/`--region` example. Tests follow the executable's supported
location → recommendation → placement sequence.
