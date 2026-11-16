#!/usr/bin/env python3
"""Resolve a published CLI release and verify its trusted Release run, before auth."""
import json
import os
import re
import subprocess
import sys
from urllib.parse import quote, urlsplit

REPOSITORY = "nistroai/ctl"


def fail(message):
    raise ValueError(message)


def api(path):
    result = subprocess.run(["gh", "api", path], check=True, capture_output=True, text=True)
    return json.loads(result.stdout)


def validate_target(server, allowed):
    parsed = urlsplit(server)
    hosts = [host.strip().lower() for host in allowed.split(",") if host.strip()]
    if (parsed.scheme != "https" or not parsed.hostname or parsed.username is not None
            or parsed.password is not None or parsed.port not in (None, 443)
            or parsed.query or parsed.fragment or parsed.path not in ("", "/")
            or not hosts or any(not re.fullmatch(r"[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?", host) for host in hosts)
            or parsed.hostname.lower() not in hosts):
        fail("NISTRO_SERVER must be a root HTTPS URL on the protected exact-host allowlist")
    return server.rstrip("/")


def required_jobs():
    return (({"Promote release to latest"} if os.environ.get("TARGET_ENVIRONMENT") == "production" else set())
            | {"test", "prepare", "publish", "Merge multi-arch manifest and sign"}
            | {f"build ({system}, {arch})" for system in ("linux", "darwin", "windows") for arch in ("amd64", "arm64")}
            | {f"verify-installer ({system})" for system in ("ubuntu-latest", "macos-latest", "windows-latest", "ubuntu-24.04-arm")}
            | {f"test-install-script / Install on {system}" for system in ("ubuntu-latest", "macos-latest", "windows-latest", "ubuntu-24.04-arm")}
            | {f"Docker e2e test linux/{arch}" for arch in ("amd64", "arm64")})


def approve_run(run, sha, tag, explicit):
    if (run.get("name") != "Release" or run.get("path") != ".github/workflows/release.yml"
            or run.get("event") != "release" or run.get("head_sha") != sha
            or run.get("head_branch") != tag):
        fail("Source run must be the fixed Release workflow for the exact published tag and commit")
    if run.get("status") == "completed":
        if run.get("conclusion") != "success":
            fail("The selected CLI Release run did not succeed")
    elif not explicit or run.get("status") != "in_progress":
        fail("Scheduled/manual latest runs require a completed successful CLI Release")
    jobs = []
    page = 1
    while True:
        batch = api(f"repos/{REPOSITORY}/actions/runs/{run['id']}/attempts/{run['run_attempt']}/jobs?per_page=100&page={page}")["jobs"]
        jobs.extend(batch)
        if len(batch) < 100:
            break
        page += 1
    observed = {job["name"]: job for job in jobs}
    if not required_jobs().issubset(observed) or any(
            observed[name].get("status") != "completed" or observed[name].get("conclusion") != "success"
            for name in required_jobs()):
        fail("Release has not passed every source/build/publish/installer/Docker prerequisite")


def resolve(requested, source_run):
    latest = requested == "latest"
    if not latest and not re.fullmatch(r"v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?", requested):
        fail("CLI_VERSION must be latest or an explicit published semantic-version tag")
    release = api(f"repos/{REPOSITORY}/releases/" + ("latest" if latest else "tags/" + quote(requested, safe="")))
    tag = release["tag_name"]
    if not re.fullmatch(r"v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?", tag) or (not latest and tag != requested):
        fail("Published release did not resolve to the requested semantic version")
    semantic_prerelease = "-" in tag.split("+", 1)[0]
    if release.get("draft") or not release.get("published_at") or (latest and (release.get("prerelease") or semantic_prerelease)):
        fail("CLI release must be published; latest must be stable")
    if os.environ.get("TARGET_ENVIRONMENT") == "production" and (release.get("prerelease") or semantic_prerelease):
        fail("Production smoke requires a promoted stable CLI release")
    ref = api(f"repos/{REPOSITORY}/git/ref/tags/{quote(tag, safe='')}")["object"]
    for _ in range(8):
        if ref["type"] == "commit":
            break
        if ref["type"] != "tag":
            fail("Release tag does not resolve to a commit")
        ref = api(f"repos/{REPOSITORY}/git/tags/{ref['sha']}")["object"]
    else:
        fail("Too many annotated-tag indirections")
    sha = ref["sha"]
    if not re.fullmatch(r"[0-9a-f]{40}", sha):
        fail("Release commit must be an immutable Git SHA")
    comparison = api(f"repos/{REPOSITORY}/compare/{sha}...main")
    if comparison.get("status") not in ("ahead", "identical") or comparison.get("merge_base_commit", {}).get("sha") != sha:
        fail("Release commit must be an ancestor of the protected CLI main branch")
    if source_run:
        if not re.fullmatch(r"[1-9][0-9]*", source_run):
            fail("SOURCE_RUN_ID must be a numeric CLI Release run ID")
        run = api(f"repos/{REPOSITORY}/actions/runs/{source_run}")
    else:
        runs = api(f"repos/{REPOSITORY}/actions/workflows/release.yml/runs?head_sha={sha}&event=release&status=success&per_page=100")["workflow_runs"]
        matching = [run for run in runs if run.get("head_branch") == tag]
        if not matching:
            fail("No successful CLI Release run exists for this exact tag and commit")
        run = matching[0]
    approve_run(run, sha, tag, bool(source_run))
    return {"version": tag, "sha": sha, "source-run-id": str(run["id"]), "release-id": str(release["id"])}


def main():
    environment = os.environ.get("TARGET_ENVIRONMENT")
    if (os.environ.get("GITHUB_REPOSITORY") != REPOSITORY
            or os.environ.get("GITHUB_REF") != "refs/heads/main"
            or os.environ.get("GITHUB_EVENT_NAME") != "workflow_dispatch"
            or environment not in ("dev", "production")):
        fail("Live validation requires the protected CLI main workflow and approved environment")
    workflow_sha = os.environ.get("GITHUB_WORKFLOW_SHA", "")
    checked_out = subprocess.run(["git", "rev-parse", "HEAD"], check=True, capture_output=True, text=True).stdout.strip()
    if not re.fullmatch(r"[0-9a-f]{40}", workflow_sha) or checked_out != workflow_sha:
        fail("Harness must be checked out at the immutable trusted workflow commit")
    resolved = resolve(os.environ.get("CLI_VERSION", "latest"), os.environ.get("SOURCE_RUN_ID", ""))
    resolved["server"] = validate_target(os.environ.get("NISTRO_SERVER", ""), os.environ.get("NISTRO_ALLOWED_API_HOSTS", ""))
    with open(os.environ["GITHUB_OUTPUT"], "a", encoding="utf-8") as output:
        for key, value in resolved.items():
            if "\n" in value or "\r" in value:
                fail("Invalid multiline approval output")
            output.write(f"{key}={value}\n")
    with open("approved-release.json", "w", encoding="utf-8") as report:
        json.dump({**resolved, "harness-sha": workflow_sha, "environment": environment}, report, indent=2)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, subprocess.CalledProcessError) as error:
        print(f"Release approval failed: {error if not isinstance(error, subprocess.CalledProcessError) else 'GitHub/git verification failed'}", file=sys.stderr)
        sys.exit(1)
