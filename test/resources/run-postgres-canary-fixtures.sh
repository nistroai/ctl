#!/usr/bin/env bash
# Exercise real SQL probes and binary CLI lifecycle fixtures using one disposable
# PostgreSQL server. The fixture suites run sequentially because one SQL test
# temporarily installs an event trigger to verify cleanup after INSERT failures.
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/../.." && pwd)"
fixture_dir="$(mktemp -d "${TMPDIR:-/tmp}/nistro-postgres-fixture.XXXXXX")"
watchdog_pid=""
active_pid=""
script_pid="$$"

cleanup() {
  local status="$?"
  trap - EXIT INT TERM
  set +e
  if [[ -n "$active_pid" ]]; then
    kill -TERM "$active_pid" 2>/dev/null
    wait "$active_pid" 2>/dev/null
  fi
  if [[ -n "$watchdog_pid" ]]; then
    kill -TERM "$watchdog_pid" 2>/dev/null
    wait "$watchdog_pid" 2>/dev/null
  fi
  if [[ -f "$fixture_dir/container.id" && ! -L "$fixture_dir/container.id" && -s "$fixture_dir/container.id" ]]; then
    local created_id
    created_id="$(cat "$fixture_dir/container.id")"
    if [[ "$created_id" =~ ^[a-f0-9]{64}$ ]]; then
      # An exited --rm container may already be absent. Verify daemon access as
      # well as absence, so a failed cleanup cannot appear successful.
      if docker container inspect "$created_id" >/dev/null 2>&1; then
        docker rm --force "$created_id" >/dev/null 2>&1
      fi
      if docker container inspect "$created_id" >/dev/null 2>&1 || ! docker info >/dev/null 2>&1; then
        printf 'Could not confirm removal of the owned PostgreSQL fixture container.\n' >&2
        if [[ "$status" == 0 ]]; then status=1; fi
      fi
    else
      printf 'The PostgreSQL fixture container ID was invalid during cleanup.\n' >&2
      if [[ "$status" == 0 ]]; then status=1; fi
    fi
  fi
  local generated_file
  for generated_file in ca.key ca.crt ca.srl server.key server.csr server.ext server.crt container.id; do
    if [[ -f "$fixture_dir/$generated_file" && ! -L "$fixture_dir/$generated_file" ]]; then
      if ! rm -- "$fixture_dir/$generated_file"; then
        if [[ "$status" == 0 ]]; then status=1; fi
      fi
    fi
  done
  if ! rmdir -- "$fixture_dir"; then
    printf 'Could not remove the PostgreSQL fixture temporary directory.\n' >&2
    if [[ "$status" == 0 ]]; then status=1; fi
  fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'printf "Local PostgreSQL fixture exceeded its five-minute deadline.\n" >&2; exit 124' TERM

(
  timer_pid=""
  trap 'if [[ -n "$timer_pid" ]]; then kill -TERM "$timer_pid" 2>/dev/null || true; fi; exit 0' TERM INT
  sleep 300 &
  timer_pid="$!"
  wait "$timer_pid"
  kill -TERM "$script_pid"
) &
watchdog_pid="$!"

run_command() {
  local status=0
  "$@" &
  active_pid="$!"
  wait "$active_pid" || status="$?"
  active_pid=""
  return "$status"
}

urlencode() {
  local input="$1" output="" character="" encoded="" index
  for ((index=0; index<${#input}; index++)); do
    character="${input:index:1}"
    case "$character" in
      [a-zA-Z0-9.~_-]) output+="$character" ;;
      *) printf -v encoded '%%%02X' "'$character"; output+="$encoded" ;;
    esac
  done
  printf '%s' "$output"
}

for required_command in go docker openssl; do
  if ! command -v "$required_command" >/dev/null 2>&1; then
    printf 'The PostgreSQL fixture runner requires %s.\n' "$required_command" >&2
    exit 1
  fi
done
cd -- "$repo_root"
run_command docker info --format '{{.ServerVersion}}' >/dev/null

# A separate CA signs a server certificate valid for the loopback IP used below.
run_command openssl req -x509 -newkey rsa:2048 -nodes \
  -keyout "$fixture_dir/ca.key" -out "$fixture_dir/ca.crt" \
  -days 1 -subj '/CN=NistroCanaryFixtureCA' >/dev/null 2>&1
run_command openssl req -newkey rsa:2048 -nodes \
  -keyout "$fixture_dir/server.key" -out "$fixture_dir/server.csr" \
  -subj '/CN=127.0.0.1' >/dev/null 2>&1
cat > "$fixture_dir/server.ext" <<'CERTIFICATE_EXTENSIONS'
subjectAltName=IP:127.0.0.1
extendedKeyUsage=serverAuth
basicConstraints=critical,CA:FALSE
CERTIFICATE_EXTENSIONS
run_command openssl x509 -req -in "$fixture_dir/server.csr" \
  -CA "$fixture_dir/ca.crt" -CAkey "$fixture_dir/ca.key" -CAcreateserial \
  -out "$fixture_dir/server.crt" -days 1 -sha256 \
  -extfile "$fixture_dir/server.ext" >/dev/null 2>&1

printf 'Starting a disposable PostgreSQL 18 fixture with verified TLS.\n'
run_command docker run --detach --rm --cidfile "$fixture_dir/container.id" \
  --publish 127.0.0.1::5432 \
  --env POSTGRES_USER=postgres --env POSTGRES_PASSWORD=NistroFixturePassword2026 \
  --env POSTGRES_DB=canary_fixture \
  --mount "type=bind,source=$fixture_dir,target=/canary-certs,readonly" \
  postgres:18 bash -c '
    set -e
    cp /canary-certs/server.crt /tmp/server.crt
    cp /canary-certs/server.key /tmp/server.key
    chown postgres:postgres /tmp/server.key
    chmod 600 /tmp/server.key
    exec docker-entrypoint.sh postgres \
      -c ssl=on -c ssl_cert_file=/tmp/server.crt -c ssl_key_file=/tmp/server.key
  ' >/dev/null
container_id="$(cat "$fixture_dir/container.id")"
ready=false
for ((attempt=0; attempt<45; attempt++)); do
  if run_command docker exec "$container_id" pg_isready -h 127.0.0.1 -U postgres -d canary_fixture >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 1
done
if [[ "$ready" != true ]]; then
  printf 'The local PostgreSQL fixture did not become ready.\n' >&2
  exit 1
fi
mapped_address="$(docker port "$container_id" 5432/tcp)"
if [[ ! "$mapped_address" =~ ^127\.0\.0\.1:([0-9]+)$ ]]; then
  printf 'The PostgreSQL fixture did not expose one loopback-only port.\n' >&2
  exit 1
fi
mapped_port="${BASH_REMATCH[1]}"
encoded_ca="$(LC_ALL=C urlencode "$fixture_dir/ca.crt")"
export NISTRO_CANARY_TEST_POSTGRES_URL="postgres://postgres:NistroFixturePassword2026@127.0.0.1:$mapped_port/canary_fixture?sslmode=verify-full&sslrootcert=$encoded_ca"

printf 'Checking SQL persistence, server settings, TLS verification, and cleanup.\n'
run_command go test -race -v -count=1 -p 1 -timeout 90s ./test/canaryutils \
  -run '^TestPostgresCheckpointLocalDatabase$'
printf 'Checking the binary CLI PostgreSQL create/update/delete lifecycle.\n'
run_command go test -race -v -count=1 -p 1 -timeout 90s ./test/smoke_test \
  -run '^TestCanaryLocalPostgresLifecycle$'

table_count="$(docker exec "$container_id" psql -U postgres -d canary_fixture -Atc "SELECT count(*) FROM pg_tables WHERE schemaname='public'")"
if [[ "$table_count" != 0 ]]; then
  printf 'The PostgreSQL fixture tests left public tables behind.\n' >&2
  exit 1
fi
printf 'Local PostgreSQL fixture checks passed; temporary resources will be removed.\n'
