#!/usr/bin/env python3
"""Require the selected live case, allowing the explicit token-only login skip."""
import json
import os
import pathlib
import sys

path, name, case = sys.argv[1:]
events = [json.loads(line) for line in pathlib.Path(path).read_text().splitlines() if line.strip()]
tests = [event for event in events if event.get("Test") == name or event.get("Test", "").startswith(name + "/")]
passed = {event["Test"] for event in tests if event.get("Action") == "pass"}
selected = name + "/" + case
login = name + "/login"
email, password, token = (os.environ.get(key, "").strip() for key in ("NISTRO_EMAIL", "NISTRO_PASSWORD", "NISTRO_TOKEN"))
password_mode = bool(email and password)
token_only = not email and not password and bool(token)
skipped = {event["Test"] for event in tests if event.get("Action") == "skip"}
if (name not in passed or selected not in passed
        or not (password_mode or token_only)
        or (password_mode and login not in passed)
        or (token_only and login not in passed and login not in skipped)
        or any(test != login or not token_only for test in skipped)
        or any(event.get("Action") == "fail" for event in events)):
    raise SystemExit("Operational suite did not pass its selected case and required authentication checks")
print(f"Verified required operational case: {name}/{case}")
if token_only and login in skipped:
    print("Token authentication passed; password login coverage was explicitly skipped")
