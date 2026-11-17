import importlib.util
import os
import pathlib
import subprocess
import tempfile
import unittest
from unittest.mock import mock_open, patch

spec = importlib.util.spec_from_file_location("approval", pathlib.Path(__file__).with_name("approve-release.py"))
approval = importlib.util.module_from_spec(spec)
spec.loader.exec_module(approval)


class TargetPolicyTests(unittest.TestCase):
    def test_exact_protected_https_host(self):
        self.assertEqual(approval.validate_target("https://api.nistro.ai/", "api.nistro.ai"), "https://api.nistro.ai")

    def test_rejects_credentials_subdomains_redirect_paths_and_ports(self):
        for server in ("http://api.nistro.ai", "https://api.nistro.ai.evil.test", "https://evil.test",
                       "https://user:pass@api.nistro.ai", "https://api.nistro.ai:8443", "https://api.nistro.ai/login",
                       "https://api.nistro.ai?redirect=evil", "https://api.nistro.ai#evil"):
            with self.subTest(server=server), self.assertRaises(ValueError):
                approval.validate_target(server, "api.nistro.ai")

    def test_empty_allowlist_is_closed(self):
        with self.assertRaises(ValueError):
            approval.validate_target("https://api.nistro.ai", "")

    def test_every_smoke_run_validates_protected_target(self):
        sha = "a" * 40
        resolved = {"version": "v1.0.0", "sha": sha, "release-id": "1", "source-run-id": "1"}
        env = {"GITHUB_REPOSITORY": "nistroai/ctl", "GITHUB_REF": "refs/heads/main",
               "GITHUB_EVENT_NAME": "workflow_dispatch", "TARGET_ENVIRONMENT": "Prod-Canary-Test",
               "GITHUB_WORKFLOW_SHA": sha, "NISTRO_ALLOWED_API_HOSTS": "api.nistro.ai",
               "GITHUB_OUTPUT": "fixture-output", "APPROVAL_MODE": "dispatch"}
        for server in ("https://api.nistro.ai", "https://evil.test"):
            with self.subTest(server=server), patch.dict(os.environ, {**env, "NISTRO_SERVER": server}), \
                    patch.object(approval.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, sha)), \
                    patch.object(approval, "resolve", return_value=dict(resolved)), \
                    patch("builtins.open", mock_open()) as output:
                if server == "https://evil.test":
                    with self.assertRaisesRegex(ValueError, "allowlist"):
                        approval.main()
                    output.assert_not_called()
                else:
                    approval.main()
                    self.assertTrue(any("server=https://api.nistro.ai" in str(call) for call in output().write.call_args_list))


class ReleaseRunTests(unittest.TestCase):
    def setUp(self):
        self.sha = "a" * 40
        self.run = {"id": 1, "run_attempt": 1, "name": "Release", "path": ".github/workflows/release.yml",
                    "event": "release", "head_sha": self.sha, "head_branch": "v1.0.0",
                    "status": "completed", "conclusion": "success"}

    def test_completed_success_requires_exact_workflow_tag_and_source(self):
        jobs = [{"name": name, "status": "completed", "conclusion": "success"} for name in approval.required_jobs()]
        with patch.object(approval, "api", return_value={"jobs": jobs}):
            approval.approve_run(self.run, self.sha, "v1.0.0", False)
        for change in ({"event": "pull_request"}, {"head_sha": "b" * 40}, {"head_branch": "v1.0.1"},
                       {"path": ".github/workflows/other.yml"}, {"conclusion": "failure"}):
            with self.subTest(change=change), self.assertRaises(ValueError):
                approval.approve_run({**self.run, **change}, self.sha, "v1.0.0", True)

    def test_completed_success_rejects_missing_or_skipped_source_prerequisites(self):
        jobs = [{"name": name, "status": "completed", "conclusion": "success"} for name in approval.required_jobs()]
        for broken in ([job for job in jobs if job["name"] != "verify-installer (ubuntu-24.04-arm)"],
                       [{**job, "conclusion": "skipped"} if job["name"] == "test" else job for job in jobs]):
            with patch.object(approval, "api", return_value={"jobs": broken}), self.assertRaises(ValueError):
                approval.approve_run(self.run, self.sha, "v1.0.0", False)

    def test_production_rejects_semver_prerelease_even_when_api_marks_stable(self):
        for tag in ("v1.0.0-rc.1", "v1.0.0-rc.1+build.2"):
            release = {"tag_name": tag, "draft": False, "prerelease": False, "published_at": "2026-11-16T00:00:00Z"}
            with patch.dict(os.environ, {"TARGET_ENVIRONMENT": "Prod-Canary-Test"}), patch.object(approval, "api", return_value=release), self.assertRaises(ValueError):
                approval.resolve(tag, "1")

    def test_hyphens_in_build_metadata_are_not_semver_prereleases(self):
        tag = "v1.0.0+build-with-hyphen"
        release = {"id": 1, "tag_name": tag, "draft": False, "prerelease": False, "published_at": "2026-11-16T00:00:00Z"}
        run = {**self.run, "head_branch": tag}
        responses = [release, {"object": {"type": "commit", "sha": self.sha}},
                     {"status": "identical", "merge_base_commit": {"sha": self.sha}}, run]
        with patch.dict(os.environ, {"TARGET_ENVIRONMENT": "Prod-Canary-Test"}), patch.object(approval, "api", side_effect=responses), patch.object(approval, "approve_run"):
            self.assertEqual(approval.resolve(tag, "1")["version"], tag)

    def test_production_requires_completed_stable_promotion(self):
        base_jobs = [{"name": name, "status": "completed", "conclusion": "success"} for name in approval.required_jobs()]
        with patch.dict(os.environ, {"TARGET_ENVIRONMENT": "Prod-Canary-Test"}):
            for jobs in (base_jobs, base_jobs + [{"name": "Promote release to latest", "status": "completed", "conclusion": "skipped"}]):
                with patch.object(approval, "api", return_value={"jobs": jobs}), self.assertRaises(ValueError):
                    approval.approve_run(self.run, self.sha, "v1.0.0", True)
            with patch.object(approval, "api", return_value={"jobs": base_jobs + [{"name": "Promote release to latest", "status": "completed", "conclusion": "success"}]}):
                approval.approve_run(self.run, self.sha, "v1.0.0", True)

    def test_candidate_requires_every_platform_prerequisite_not_skipped(self):
        self.run.update(status="in_progress", conclusion=None)
        jobs = [{"name": name, "status": "completed", "conclusion": "success"} for name in approval.required_jobs()]
        with patch.object(approval, "api", return_value={"jobs": jobs}):
            approval.approve_run(self.run, self.sha, "v1.0.0", True)
            with self.assertRaises(ValueError):
                approval.approve_run(self.run, self.sha, "v1.0.0", False)
        for broken in (jobs[:-1], [{**job, "conclusion": "skipped"} if job["name"] == "publish" else job for job in jobs]):
            with patch.object(approval, "api", return_value={"jobs": broken}), self.assertRaises(ValueError):
                approval.approve_run(self.run, self.sha, "v1.0.0", True)


if __name__ == "__main__":
    unittest.main()
