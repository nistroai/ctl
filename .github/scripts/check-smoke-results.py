#!/usr/bin/env python3
"""Require real smoke coverage; optional describe/token-login skips stay explicit."""
import json
import os
import pathlib
import sys

events = [json.loads(line) for line in pathlib.Path(sys.argv[1]).read_text().splitlines() if line.strip()]
passed = {event.get("Test") for event in events if event.get("Action") == "pass"}
skipped = {event.get("Test") for event in events if event.get("Action") == "skip"}
required = {"TestProductionSmoke", "TestProductionSmoke/version", "TestProductionSmoke/inference/deployments",
            "TestProductionSmoke/postgres/deployments", "TestProductionSmoke/postgres/discovery",
            "TestProductionSmoke/inference/discovery"}
if not required.issubset(passed) or any(event.get("Action") == "fail" for event in events):
    raise SystemExit("Smoke validation lacks required passing authentication/read/discovery coverage")
login = "TestProductionSmoke/login"
token_auth = not os.environ.get("NISTRO_EMAIL", "").strip() and not os.environ.get("NISTRO_PASSWORD") and bool(os.environ.get("NISTRO_TOKEN", "").strip())
if (token_auth and login not in skipped) or (not token_auth and login not in passed):
    raise SystemExit("Smoke authentication coverage is missing")
optional = {"TestProductionSmoke/inference/deployments/describe", "TestProductionSmoke/postgres/deployments/describe"}
if token_auth:
    optional.add(login)
if skipped - optional:
    raise SystemExit("Required smoke coverage skipped")
print(f"Required smoke coverage passed; optional skips: {len(skipped)}")
