import json
import os
import pathlib
import subprocess
import tempfile
import unittest


class SmokeResultsTests(unittest.TestCase):
    def check(self, events, token=False):
        with tempfile.TemporaryDirectory() as folder:
            path = pathlib.Path(folder) / "results.jsonl"
            path.write_text("\n".join(json.dumps(event) for event in events))
            env = dict(os.environ, NISTRO_EMAIL="" if token else "fixture@example.test",
                       NISTRO_PASSWORD="" if token else "fixture-password", NISTRO_TOKEN="fixture-token" if token else "")
            return subprocess.run(["python3", str(pathlib.Path(__file__).with_name("check-smoke-results.py")), str(path)],
                                  capture_output=True, env=env).returncode

    def events(self):
        return [{"Action": "pass", "Test": name} for name in (
            "TestProductionSmoke", "TestProductionSmoke/version", "TestProductionSmoke/login",
            "TestProductionSmoke/inference/deployments", "TestProductionSmoke/postgres/deployments",
            "TestProductionSmoke/inference/discovery", "TestProductionSmoke/postgres/discovery")]

    def test_required_coverage_and_optional_describe_skip(self):
        events = self.events() + [{"Action": "skip", "Test": "TestProductionSmoke/postgres/deployments/describe"}]
        self.assertEqual(self.check(events), 0)
        for broken in ([], events[1:], events + [{"Action": "fail"}],
                       events + [{"Action": "skip", "Test": "TestProductionSmoke/inference/discovery/scale"}]):
            self.assertNotEqual(self.check(broken), 0)

    def test_token_login_skip_is_explicit_and_password_login_is_required(self):
        events = [{**event, "Action": "skip"} if event["Test"] == "TestProductionSmoke/login" else event for event in self.events()]
        self.assertEqual(self.check(events, token=True), 0)
        self.assertNotEqual(self.check(events), 0)
        self.assertNotEqual(self.check([event for event in events if event["Test"] != "TestProductionSmoke/login"], token=True), 0)


if __name__ == "__main__":
    unittest.main()
