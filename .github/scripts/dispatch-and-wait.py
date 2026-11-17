#!/usr/bin/env python3
"""Run the trusted same-repository smoke workflow and propagate its result."""
import argparse
import datetime
import json
import os
import re
import time
import uuid
import urllib.error
import urllib.request

REPOSITORY = "nistroai/ctl"
BRANCH = "main"
WORKFLOW = "smoke-tests.yml"


def github_request(path, token, payload=None):
    # Keep GITHUB_TOKEN out of subprocess arguments and refuse API redirects.
    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, req, fp, code, msg, headers, newurl):
            raise RuntimeError("GitHub API redirects are not permitted")
    request = urllib.request.Request("https://api.github.com/" + path,
        data=json.dumps(payload).encode() if payload is not None else None,
        headers={"Authorization": "Bearer " + token, "Accept": "application/vnd.github+json",
                 "Content-Type": "application/json", "X-GitHub-Api-Version": "2022-11-28"})
    try:
        with urllib.request.build_opener(NoRedirect).open(request, timeout=45) as response:
            raw = response.read()
    except (urllib.error.URLError, TimeoutError):
        raise RuntimeError("GitHub validation dispatch/status request failed")
    return json.loads(raw) if raw.strip() else None


def api(path, payload=None):
    return github_request(path, os.environ.get("GH_TOKEN", ""), payload)


def selected_run(runs, branch, correlation):
    matches = [run for run in runs if run.get("head_branch") == branch
               and run.get("event") == "workflow_dispatch"
               and run.get("display_title", "").endswith(f"[{correlation}]")]
    if len(matches) > 1:
        raise RuntimeError("Validation correlation matched multiple runs")
    return matches[0] if matches else None


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--environment", choices=("Dev-Smoke-Test", "Prod-Canary-Test"), required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--source-run-id", required=True)
    parser.add_argument("--timeout-minutes", type=int, default=60)
    args = parser.parse_args()
    if (not re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?", args.version)
            or not re.fullmatch(r"[1-9][0-9]*", args.source_run_id)
            or not 1 <= args.timeout_minutes <= 60):
        raise RuntimeError("Expected an exact release tag, source run ID, and bounded deadline")
    correlation = f"release-{args.source_run_id}-{os.environ.get('GITHUB_RUN_ATTEMPT', '1')}-{uuid.uuid4().hex}"
    created = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    inputs = {"environment": args.environment, "cli-version": args.version,
              "source-run-id": args.source_run_id, "correlation-id": correlation}
    api(f"repos/{REPOSITORY}/actions/workflows/{WORKFLOW}/dispatches", {"ref": BRANCH, "inputs": inputs})
    deadline = time.monotonic() + args.timeout_minutes * 60
    run = None
    errors = 0
    while time.monotonic() < deadline:
        try:
            if run is None:
                runs = api(f"repos/{REPOSITORY}/actions/workflows/{WORKFLOW}/runs?event=workflow_dispatch&branch={BRANCH}&created=%3E%3D{created}&per_page=100")["workflow_runs"]
                run = selected_run(runs, BRANCH, correlation)
                if run:
                    print(f"Validation run: {run['html_url']}", flush=True)
                    if os.environ.get("GITHUB_OUTPUT"):
                        with open(os.environ["GITHUB_OUTPUT"], "a", encoding="utf-8") as output:
                            output.write(f"run-id={run['id']}\nrun-url={run['html_url']}\n")
                    if os.environ.get("GITHUB_STEP_SUMMARY"):
                        with open(os.environ["GITHUB_STEP_SUMMARY"], "a", encoding="utf-8") as summary:
                            summary.write(f"\n{args.environment} validation: {run['html_url']}\n")
            else:
                run = api(f"repos/{REPOSITORY}/actions/runs/{run['id']}")
            errors = 0
            if run and run.get("status") == "completed":
                if run.get("conclusion") != "success":
                    raise RuntimeError(f"Validation completed with {run.get('conclusion', 'unknown')}")
                print("Validation completed successfully", flush=True)
                return
        except RuntimeError as error:
            # A terminal failed run must never be treated as a transient API error.
            if run and run.get("status") == "completed":
                raise
            errors += 1
            if errors >= 5:
                raise error
        time.sleep(10)
    raise RuntimeError("Validation did not finish within its dispatch/wait deadline")


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, KeyError) as error:
        raise SystemExit(str(error))
