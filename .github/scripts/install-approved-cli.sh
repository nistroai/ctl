#!/usr/bin/env bash
# Download fixed release assets; never execute an installer supplied by a caller.
set -euo pipefail
: "${CLI_VERSION:?}" "${CLI_SHA:?}" "${CLI_RELEASE_ID:?}" "${RUNNER_TEMP:?}" "${GITHUB_ENV:?}"
[[ "$CLI_SHA" =~ ^[a-f0-9]{40}$ && "$CLI_RELEASE_ID" =~ ^[1-9][0-9]*$ ]] || exit 1
install_dir="$(mktemp -d "$RUNNER_TEMP/nistro-approved.XXXXXX")"
archive='nistro-linux-amd64.tar.gz'
release_metadata="$(gh api "repos/nistroai/ctl/releases/$CLI_RELEASE_ID")"
[[ "$(jq -r .tag_name <<< "$release_metadata")" == "$CLI_VERSION" ]]
for filename in "$archive" SHA256SUMS; do
  asset_id="$(jq -er --arg name "$filename" '[.assets[] | select(.name == $name) | .id] | if length == 1 then .[0] else error("Expected exactly one fixed release asset") end' <<< "$release_metadata")"
  [[ "$asset_id" =~ ^[1-9][0-9]*$ ]]
  gh api "repos/nistroai/ctl/releases/assets/$asset_id" -H 'Accept: application/octet-stream' > "$install_dir/$filename"
done
cd -- "$install_dir"
# Require the single expected checksum rather than executing filenames from the manifest.
python3 - "$archive" <<'PY'
import hashlib, pathlib, re, sys
name = sys.argv[1]
matching = [line for line in pathlib.Path("SHA256SUMS").read_text().splitlines() if re.fullmatch(r"[a-fA-F0-9]{64} [ *]" + re.escape(name), line)]
if len(matching) != 1 or hashlib.sha256(pathlib.Path(name).read_bytes()).hexdigest() != matching[0][:64].lower():
    raise SystemExit("The fixed CLI archive checksum did not match")
PY
gh attestation verify "$archive" --repo nistroai/ctl \
  --signer-workflow nistroai/ctl/.github/workflows/release.yml \
  --source-digest "$CLI_SHA" --deny-self-hosted-runners --format json > verified-provenance.json
# Only extract the expected regular binary, avoiding links and archive traversal.
python3 - "$archive" <<'PY'
import os, pathlib, sys, tarfile
with tarfile.open(sys.argv[1], "r:gz") as archive:
    members = archive.getmembers()
    if len(members) != 1 or members[0].name != "nistro-linux-amd64" or not members[0].isfile() or members[0].size > 300_000_000:
        raise SystemExit("CLI archive must contain only its expected regular binary")
    with archive.extractfile(members[0]) as source:
        pathlib.Path("nistro").write_bytes(source.read())
os.chmod("nistro", 0o700)
PY
printf 'NISTRO_TEST_BINARY=%s/nistro\n' "$install_dir" >> "$GITHUB_ENV"
printf 'CLI_PROVENANCE_FILE=%s/verified-provenance.json\n' "$install_dir" >> "$GITHUB_ENV"
