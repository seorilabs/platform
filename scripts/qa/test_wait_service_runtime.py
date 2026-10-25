import json
import unittest
from unittest.mock import patch
from wait_service_runtime import successful_run, wait, WORKFLOW


class CompletedRuntime(unittest.TestCase):
    def setUp(self):
        self.sha = "a" * 40
        self.run = dict(head_sha=self.sha, path=WORKFLOW, head_branch="main",
                        event="push", status="completed", conclusion="success", html_url="synthetic")

    def test_only_completed_matching_runtime_can_unlock_deployment(self):
        for key, value in [("head_sha", "b" * 40), ("path", ".github/workflows/deploy.yml"),
                           ("head_branch", "feature"), ("event", "pull_request"),
                           ("status", "in_progress"), ("conclusion", "failure")]:
            with self.subTest(key=key):
                self.assertIsNone(successful_run([dict(self.run, **{key: value})], self.sha))
        self.assertEqual(successful_run([self.run], self.sha), self.run)

    def test_bounded_wait_cannot_accept_missing_or_failed_execution(self):
        with patch("subprocess.check_output", return_value=json.dumps({"workflow_runs": []}).encode()):
            with self.assertRaises(TimeoutError):
                wait(self.sha, timeout=0)

    def test_malformed_candidate_never_reaches_github(self):
        with patch("subprocess.check_output") as command:
            with self.assertRaises(ValueError):
                wait("invalid")
            command.assert_not_called()
