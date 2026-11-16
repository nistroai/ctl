# CLI binary test framework

The framework runs `nistro` with Go's `os/exec`, then asserts its exit code,
stdout, stderr, and persisted session behavior. Tests never import `cmd` or
the CLI's internal packages. This validates the executable users install,
including command registration, flag parsing, HTTP requests, and formatting.

| Suite | Server | Coverage | When it runs |
| --- | --- | --- | --- |
| `test/integration_test` | Local `httptest.Server` | Help/version, login/logout, session isolation and token overrides, JSON/table output, errors, validation, discovery, and create/update/delete for both products | Normal Go tests on PRs/merge queue; published binaries on release runners |
| `TestProductionSmoke` in `test/smoke_test` | Protected Dev/production API | Released version, password login and saved session, deployment list/describe, Postgres placement catalog, hardware recommendations, and pricing offers for both tiers | Every release candidate against Dev; stable releases against production; trusted manual runs |
| Private `omnistrate/nistro-e2e-tests` | Protected Dev/production API and data endpoints | Four fresh lifecycle cases and periodic persistent-deployment health | Dev candidates before stable promotion; production after stable promotion; daily lifecycle/hourly health |

CLI smoke uses authentication, read and discovery operations. Deployment lifecycle
code, its inference/SQL probes, and its HTTP/TLS PostgreSQL regression fixtures
live in the private [Nistro E2E repository](https://github.com/omnistrate/nistro-e2e-tests).
Normal public PR checks need no Nistro account. Live runs require an approved
server and dedicated account.

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

## Local checks

```sh
make test
make test-binary
make test-readme
NISTRO_TEST_BINARY=/absolute/path/to/nistro make test-binary
python3 -m unittest discover -s .github/scripts -p 'test_*.py'
```

The public test runner still builds a CLI for local contracts when no explicit
binary is selected. Live smoke requires an installed binary and never builds a
replacement. Its command helper creates an isolated temporary session directory
and redacts known credentials from diagnostics.

For authorized local smoke, select an approved binary, set `NISTRO_SERVER`,
`NISTRO_TEST_ALLOWED_API_HOSTS`, and `NISTRO_EMAIL`/`NISTRO_PASSWORD` or
`NISTRO_TOKEN`, then run `make smoke-test`. Do not paste passwords into process
arguments; login uses password stdin. Set `NISTRO_TEST_EXPECTED_VERSION` to assert
the exact version. A token explicitly skips password-login coverage. Configure
`NISTRO_SMOKE_INFERENCE_ID` and `NISTRO_SMOKE_POSTGRES_ID` for describe coverage;
without configured or existing deployments, describe is an explicit optional skip.

## Protected workflows and release ordering

`smoke-tests.yml` accepts only an environment and published release selector,
with optional originating Release/correlation IDs. It accepts no server or test
harness ref. Run it on `main`; other refs fail. Tests and download code are checked
out at the immutable workflow SHA. Before account credentials are injected, the
workflow validates the environment's exact HTTPS API host, resolves the release's
explicit tag to an immutable commit on `main`, verifies every Release prerequisite
job, and checks the downloaded archive's checksum and GitHub provenance against
that source commit. A version-shaped branch cannot select test code.

The installed binary is verified against the approved version. Control-plane
redirects cannot change origin or downgrade HTTPS; login bodies and tokens are
not forwarded to another origin. CLI `NISTRO_HTTP_TIMEOUT` defaults to `30s` for
ordinary use and accepts positive Go durations. Live smoke uses `2m` by default,
with a three-minute subprocess deadline, twenty-minute suite deadline and
thirty-minute job deadline. Configured live request deadlines must be below three
minutes. These cover authentication/read/discovery; cold inference provisioning
has separate private per-tier lifecycle and cleanup deadlines.

Release ordering is:

1. Build and publish versioned candidate assets with checksums and provenance.
2. Pass source, installer, installed-binary and container checks for every tag.
3. Dispatch the trusted public smoke and private canary workers against Dev and
   wait for both corresponding runs to succeed.
4. Promote only intended stable releases. A prerelease suffix or original
   prerelease flag prevents stable/latest GitHub and image alias updates.
5. Dispatch production checks after stable promotion and wait for their results.

The tag-context release jobs hold no production account or App secrets. They use
`GITHUB_TOKEN` to dispatch public workers on `main`. `dispatch-canaries.yml` uses
a scoped GitHub App to dispatch the private worker on `master`, correlates the
exact run, renews its one-hour installation token during longer waits, and
propagates failure. Queuing a run alone cannot satisfy validation. Production
failure occurs after promotion; the release remains available.

Historical archives without the new attestations cannot pass operational
verification. Merge both coordinated PRs and configure the following settings
before publishing the first attested candidate.

## Repository setup

Protect `main` and create environments `dev`, `production`, and `canary-dispatch`.
Restrict their deployment branches to `main`; apply the team's environment
protection policy. In both `dev` and `production`, configure:

| Kind | Name | Purpose |
| --- | --- | --- |
| Variable | `NISTRO_SERVER` | Approved root HTTPS API URL |
| Variable | `NISTRO_ALLOWED_API_HOSTS` | Exact comma-separated API hostnames; no wildcard or URL |
| Secrets | `SMOKE_TEST_EMAIL`, `SMOKE_TEST_PASSWORD` | Dedicated smoke account |
| Secret | `SMOKE_TEST_TOKEN` | Alternative token authentication |
| Variable | `NISTRO_HTTP_TIMEOUT` | Optional request deadline, default `2m` for live smoke |
| Variables | `NISTRO_SMOKE_INFERENCE_ID`, `NISTRO_SMOKE_POSTGRES_ID` | Dedicated describe fixtures |
| Variables | `NISTRO_SMOKE_MODEL`, `NISTRO_SMOKE_MAX_MODEL_LEN`, `NISTRO_SMOKE_LOCATION` | Optional discovery inputs |

In `canary-dispatch`, store `CANARY_APP_ID` and `CANARY_APP_PRIVATE_KEY`. Install
that App on only `omnistrate/nistro-e2e-tests`, granting Actions read/write,
Contents read, and Metadata read. Public PR jobs never receive these secrets.
Configure private `dev`/`production` environments, account credentials, persistent
IDs, and per-tier configs using that repository's README. These settings have not
been provisioned by the migration, and fixture success is not live validation.

## Extending coverage

Keep local CLI request/output/authentication scenarios in `test/integration_test`
and register successful command behavior in `commandScenarios`. Keep public live
read/discovery checks in `TestProductionSmoke`. Add lifecycle, data-plane,
persistent-health and broader E2E scenarios in the private repository, including
their fixture regressions and bounded cleanup.
