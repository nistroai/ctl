# CLI binary test framework

The framework runs `nistro` with Go's `os/exec`, then asserts its exit code,
stdout, stderr, and persisted session behavior. Tests never import `cmd` or
the CLI's internal packages. This validates the executable users install,
including command registration, flag parsing, HTTP requests, and formatting.

| Suite | Server | Coverage | When it runs |
| --- | --- | --- | --- |
| `test/integration_test` | Local `httptest.Server` | Help/version, login/logout, session isolation and token overrides, JSON/table output, errors, validation, discovery, and create/update/delete for both products | Normal Go tests on PRs/merge queue; published binaries on release runners |
| `test/smoke_test` | Configured Nistro API, production by default | Released version, password login and saved session, deployment list/describe, Postgres placement catalog, hardware recommendations, and pricing offers for both tiers | Stable CLI releases, reusable workflow calls, and manual Actions runs |

Production checks use read and discovery operations. Discovery POSTs calculate
recommendations/prices; they do not create deployments. Create/update/delete
coverage runs against the local test server. A passing smoke run confirms the
CLI and those API paths work together; it does not confirm resource provisioning,
model inference requests, or SQL queries.

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

## Production account configuration

Create a GitHub environment named `production` in the repository running the
workflow and configure these secrets:

| Secret | Purpose |
| --- | --- |
| `SMOKE_TEST_EMAIL` and `SMOKE_TEST_PASSWORD` | Recommended: exercise login and session persistence as well as authenticated commands |
| `SMOKE_TEST_TOKEN` | Alternative authentication; skips password login coverage |
| `CLI_REPOSITORY_TOKEN` | Optional read access to `nistroai/ctl` for a caller whose token cannot access it |

Enabled smoke runs fail if authentication is absent or incomplete. Credentials
need access to both product lists and discovery endpoints. An empty deployment
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

The existing Release workflow now installs and tests the published binary on
Linux AMD64/ARM64, macOS, and Windows. After these checks, every
stable release calls `.github/workflows/smoke-tests.yml` against production.
Prereleases still run binary contract tests and can run live tests manually.

The smoke workflow resolves `latest` once to an exact published tag, checks out
that tag's tests, and installs its checksum-verified release archive. It never
builds a substitute binary. Failed tests fail the job, and Go JSON results are
uploaded even on test failure. `-count=1` prevents Go's cache from reusing a prior
successful production result. Isolated runs may overlap, so GitHub concurrency
groups cannot replace a pending production release check.

The release pipeline calls the workflow directly after publishing because
releases created with `GITHUB_TOKEN` do not trigger another workflow listening
for `release: published`. Smoke failure marks the release workflow failed;
the already published release remains available.

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
the environment, API server, and release tag. No periodic schedule is required.

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
Keep production mutations in a separate opt-in
suite with dedicated resources and cleanup if provisioning canaries are added.

Reference docs: [CLI](https://dev.nistro.ai/docs/cli),
[Postgres](https://dev.nistro.ai/docs/postgres), and
[Inference](https://dev.nistro.ai/docs/inference). The current CLI requires
`--location` for `inference recommend-hardware scale`; the CLI docs currently
show a stale `--cloud`/`--region` example. Tests follow the executable's supported
location → recommendation → placement sequence.
