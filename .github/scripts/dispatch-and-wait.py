#!/usr/bin/env python3
"""Dispatch a trusted default-branch validation and propagate its exact result."""
import argparse
import base64
import datetime
import json
import os
import re
import subprocess
import tempfile
import time
import uuid
import urllib.error
import urllib.request

TARGETS = {
    "omnistrate/nistro-e2e-tests": ("master", "service-canary.yml"),
    "nistroai/ctl": ("main", "smoke-tests.yml"),
}
APP_TOKEN = None


class AppToken:
    """Renew the scoped installation token while slow inference jobs run."""
    def __init__(self, app_id, private_key):
        if not re.fullmatch(r"[1-9][0-9]*", app_id) or not private_key:
            raise RuntimeError("Configure CANARY_APP_ID and CANARY_APP_PRIVATE_KEY in the protected dispatch environment")
        self.app_id, self.private_key = app_id, private_key
        self.token, self.expiry = "", 0

    def get(self):
        if self.expiry > time.time() + 300:
            return self.token
        encode = lambda value: base64.urlsafe_b64encode(value).rstrip(b"=")
        issued = int(time.time()) - 60
        unsigned = encode(b'{"alg":"RS256","typ":"JWT"}') + b"." + encode(json.dumps(
            {"iat": issued, "exp": issued + 540, "iss": self.app_id}).encode())
        with tempfile.NamedTemporaryFile() as key:
            key.write(self.private_key.encode())
            key.flush()
            signature = subprocess.run(["openssl", "dgst", "-sha256", "-sign", key.name],
                                       input=unsigned, capture_output=True, timeout=15)
            if signature.returncode:
                raise RuntimeError("GitHub App signing failed")
        jwt = (unsigned + b"." + encode(signature.stdout)).decode()
        installation = github_request("repos/omnistrate/nistro-e2e-tests/installation", jwt)
        credential = github_request(f"app/installations/{installation['id']}/access_tokens", jwt,
                                    {"repositories": ["nistro-e2e-tests"],
                                     "permissions": {"actions": "write", "contents": "read"}})
        self.token = credential["token"]
        self.expiry = datetime.datetime.fromisoformat(credential["expires_at"].replace("Z", "+00:00")).timestamp()
        print(f"::add-mask::{self.token}", flush=True)
        return self.token


def github_request(path, token, payload=None):
    # GitHub App JWTs require Bearer authentication. Keep credentials out of
    # subprocess arguments, and refuse API redirects to another destination.
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
    token = APP_TOKEN.get() if APP_TOKEN is not None else os.environ.get("GH_TOKEN", "")
    return github_request(path, token, payload)


def selected_run(runs, branch, correlation):
    matches = [run for run in runs if run.get("head_branch") == branch
               and run.get("event") == "workflow_dispatch"
               and run.get("display_title", "").endswith(f"[{correlation}]")]
    if len(matches) > 1:
        raise RuntimeError("Validation correlation matched multiple runs")
    return matches[0] if matches else None


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--repository", choices=TARGETS, required=True)
    parser.add_argument("--workflow", choices=("smoke-tests.yml", "dispatch-canaries.yml"))
    parser.add_argument("--environment", choices=("dev", "production"), required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--suite", choices=("lifecycle", "health"), default="lifecycle")
    parser.add_argument("--source-run-id", required=True)
    parser.add_argument("--timeout-minutes", type=int, default=115)
    args = parser.parse_args()
    if (not re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?", args.version)
            or not re.fullmatch(r"[1-9][0-9]*", args.source_run_id)
            or not 1 <= args.timeout_minutes <= 180):
        raise RuntimeError("Expected an exact release tag, source run ID, and bounded deadline")
    branch, workflow = TARGETS[args.repository]
    if args.workflow:
        if args.repository != "nistroai/ctl":
            raise RuntimeError("Workflow overrides are restricted to trusted public dispatchers")
        workflow = args.workflow
    global APP_TOKEN
    if args.repository == "omnistrate/nistro-e2e-tests":
        APP_TOKEN = AppToken(os.environ.get("CANARY_APP_ID", ""), os.environ.get("CANARY_APP_PRIVATE_KEY", ""))
    correlation = f"release-{args.source_run_id}-{os.environ.get('GITHUB_RUN_ATTEMPT', '1')}-{uuid.uuid4().hex}"
    created = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    inputs = {"environment": args.environment, "cli-version": args.version,
              "source-run-id": args.source_run_id, "correlation-id": correlation}
    if args.repository == "omnistrate/nistro-e2e-tests" or workflow == "dispatch-canaries.yml":
        inputs["suite"] = args.suite
    api(f"repos/{args.repository}/actions/workflows/{workflow}/dispatches", {"ref": branch, "inputs": inputs})
    deadline = time.monotonic() + args.timeout_minutes * 60
    run = None
    errors = 0
    while time.monotonic() < deadline:
        try:
            if run is None:
                runs = api(f"repos/{args.repository}/actions/workflows/{workflow}/runs?event=workflow_dispatch&branch={branch}&created=%3E%3D{created}&per_page=100")["workflow_runs"]
                run = selected_run(runs, branch, correlation)
                if run:
                    print(f"Validation run: {run['html_url']}", flush=True)
                    if os.environ.get("GITHUB_OUTPUT"):
                        with open(os.environ["GITHUB_OUTPUT"], "a", encoding="utf-8") as output:
                            output.write(f"run-id={run['id']}\nrun-url={run['html_url']}\n")
                    if os.environ.get("GITHUB_STEP_SUMMARY"):
                        with open(os.environ["GITHUB_STEP_SUMMARY"], "a", encoding="utf-8") as summary:
                            summary.write(f"\n{args.environment} validation: {run['html_url']}\n")
            else:
                run = api(f"repos/{args.repository}/actions/runs/{run['id']}")
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
    except (RuntimeError, KeyError, subprocess.TimeoutExpired) as error:
        raise SystemExit(str(error))
