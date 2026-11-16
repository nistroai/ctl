import importlib.util
import contextlib
import io
import pathlib
import sys
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("dispatch", pathlib.Path(__file__).with_name("dispatch-and-wait.py"))
dispatch = importlib.util.module_from_spec(spec)
spec.loader.exec_module(dispatch)


class CorrelationTests(unittest.TestCase):
    def test_only_correlated_default_branch_dispatch_is_selected(self):
        expected = {"id": 3, "event": "workflow_dispatch", "head_branch": "main", "display_title": "Nistro validation [nonce]"}
        other = [dict(expected, id=1, head_branch="untrusted"), dict(expected, id=2, event="schedule"),
                 dict(expected, id=4, display_title="Nistro validation [other]")]
        self.assertEqual(dispatch.selected_run(other + [expected], "main", "nonce"), expected)
        self.assertIsNone(dispatch.selected_run(other, "main", "nonce"))

    def test_ambiguous_correlation_fails(self):
        run = {"event": "workflow_dispatch", "head_branch": "main", "display_title": "Nistro validation [nonce]"}
        with self.assertRaises(RuntimeError):
            dispatch.selected_run([run, dict(run)], "main", "nonce")


class SmokeDispatchTests(unittest.TestCase):
    args = ["dispatch-and-wait.py", "--environment", "dev", "--version", "v1.0.0-rc.1", "--source-run-id", "123"]

    def test_fixed_public_target_awaits_correlated_success(self):
        completed = {"id": 7, "html_url": "https://github.com/nistroai/ctl/actions/runs/7",
                     "status": "completed", "conclusion": "success"}
        with patch.object(sys, "argv", self.args), \
                patch.object(dispatch, "api", side_effect=[None, {"workflow_runs": []}]) as api, \
                patch.object(dispatch, "selected_run", return_value=completed), \
                contextlib.redirect_stdout(io.StringIO()):
            dispatch.main()
        path, payload = api.call_args_list[0].args
        self.assertEqual(path, "repos/nistroai/ctl/actions/workflows/smoke-tests.yml/dispatches")
        self.assertEqual(payload["ref"], "main")
        self.assertEqual(payload["inputs"]["cli-version"], "v1.0.0-rc.1")
        self.assertNotIn("suite", payload["inputs"])

    def test_terminal_failure_is_propagated_without_retry(self):
        completed = {"id": 7, "html_url": "https://github.com/nistroai/ctl/actions/runs/7",
                     "status": "completed", "conclusion": "failure"}
        with patch.object(sys, "argv", self.args), \
                patch.object(dispatch, "api", side_effect=[None, {"workflow_runs": []}]) as api, \
                patch.object(dispatch, "selected_run", return_value=completed), \
                contextlib.redirect_stdout(io.StringIO()), self.assertRaisesRegex(RuntimeError, "failure"):
            dispatch.main()
        self.assertEqual(api.call_count, 2)

    def test_repository_workflow_and_suite_cannot_be_overridden(self):
        for option, value in (("--repository", "omnistrate/nistro-e2e-tests"),
                              ("--workflow", "service-canary.yml"), ("--suite", "lifecycle")):
            with self.subTest(option=option), patch.object(sys, "argv", self.args + [option, value]), \
                    patch.object(dispatch, "api") as api, contextlib.redirect_stderr(io.StringIO()), \
                    self.assertRaises(SystemExit) as error:
                dispatch.main()
            self.assertEqual(error.exception.code, 2)
            api.assert_not_called()


if __name__ == "__main__":
    unittest.main()
