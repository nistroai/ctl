import importlib.util
import contextlib
import io
import pathlib
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("dispatch", pathlib.Path(__file__).with_name("dispatch-and-wait.py"))
dispatch = importlib.util.module_from_spec(spec)
spec.loader.exec_module(dispatch)


class CorrelationTests(unittest.TestCase):
    def test_only_correlated_default_branch_dispatch_is_selected(self):
        expected = {"id": 3, "event": "workflow_dispatch", "head_branch": "master", "display_title": "Nistro validation [nonce]"}
        other = [dict(expected, id=1, head_branch="untrusted"), dict(expected, id=2, event="schedule"),
                 dict(expected, id=4, display_title="Nistro validation [other]")]
        self.assertEqual(dispatch.selected_run(other + [expected], "master", "nonce"), expected)
        self.assertIsNone(dispatch.selected_run(other, "master", "nonce"))

    def test_ambiguous_correlation_fails(self):
        run = {"event": "workflow_dispatch", "head_branch": "master", "display_title": "Nistro validation [nonce]"}
        with self.assertRaises(RuntimeError):
            dispatch.selected_run([run, dict(run)], "master", "nonce")

    def test_installation_token_is_scoped_and_renewed_before_expiry(self):
        app = dispatch.AppToken("123", "fixture-private-key")
        credentials = [{"id": 7}, {"token": "fixture-token-one", "expires_at": "2026-11-16T01:00:00Z"},
                       {"id": 7}, {"token": "fixture-token-two", "expires_at": "2026-11-16T02:00:00Z"}]
        initial = dispatch.datetime.datetime(2026, 11, 16, tzinfo=dispatch.datetime.timezone.utc).timestamp()
        signature = type("Signature", (), {"returncode": 0, "stdout": b"fixture-signature"})()
        with patch.object(dispatch, "github_request", side_effect=credentials) as request, \
                patch.object(dispatch.subprocess, "run", return_value=signature), \
                patch.object(dispatch.time, "time", return_value=initial) as clock, \
                contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(app.get(), "fixture-token-one")
            self.assertEqual(app.get(), "fixture-token-one")
            self.assertEqual(request.call_count, 2)
            self.assertEqual(request.call_args.args[2], {"repositories": ["nistro-e2e-tests"],
                             "permissions": {"actions": "write", "contents": "read"}})
            clock.return_value = initial + 3600
            self.assertEqual(app.get(), "fixture-token-two")
            self.assertEqual(request.call_count, 4)


if __name__ == "__main__":
    unittest.main()
