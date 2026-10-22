"""Behavioral tests of the retry/close authorization boundary."""
import copy
import unittest

from renovate_retry import decide


def snapshot():
    return {
        "pr_open": True, "author": "renovate[bot]", "branch": "renovate/example-1.x",
        "commit_authors": ["renovate[bot]", "renovate[bot]"],
        "head_sha": "abc", "base_ref": "main", "expected_base_ref": "main",
        "event": {"id": 12, "attempt": 1, "sha": "abc"},
        "current_run": {"id": 12, "attempt": 1, "sha": "abc", "status": "completed", "conclusion": "failure"},
        "checks_complete": True,
        "checks": [{"status": "completed", "conclusion": "success"}, {"status": "completed", "conclusion": "failure"}],
    }


class RetryPolicyTests(unittest.TestCase):
    def test_one_retry_then_close(self):
        data = snapshot()
        first = decide(data)
        self.assertEqual(first["action"], "retry")
        self.assertEqual(first["state"], {"sha": "abc", "run_id": 12, "attempt": 1})
        self.assertEqual(decide(data, first["state"])["action"], "wait")
        data["event"]["attempt"] = data["current_run"]["attempt"] = 2
        self.assertEqual(decide(data, first["state"])["action"], "close")
        data["pr_open"] = False
        self.assertEqual(decide(data, first["state"])["action"], "wait")

    def test_manual_or_later_run_is_not_proof_of_retry(self):
        for run_id, attempt in [(13, 2), (12, 3), (12, 1)]:
            with self.subTest(run_id=run_id, attempt=attempt):
                data = snapshot()
                for key in ("event", "current_run"):
                    data[key].update(id=run_id, attempt=attempt)
                self.assertEqual(decide(data, {"sha": "abc", "run_id": 12, "attempt": 1})["action"], "wait")

    def test_new_bot_head_gets_its_own_retry(self):
        self.assertEqual(decide(snapshot(), {"sha": "old", "run_id": 11, "attempt": 1})["action"], "retry")

    def test_human_or_unattributed_commits_are_never_touched(self):
        for authors in [[], None, ["renovate[bot]", "owner"], [None], ["github-actions[bot]"]]:
            with self.subTest(authors=authors):
                data = snapshot()
                data["commit_authors"] = authors
                self.assertEqual(decide(data)["action"], "wait")

    def test_pr_and_branch_identity(self):
        for key, value in [("pr_open", False), ("author", "owner"), ("branch", "feature/foo"), ("branch", None), ("base_ref", "old"), ("expected_base_ref", "")]:
            with self.subTest(key=key, value=value):
                data = snapshot()
                data[key] = value
                self.assertEqual(decide(data)["action"], "wait")

    def test_stale_event_or_run_does_not_act(self):
        for key, value in [("id", 11), ("attempt", 2), ("sha", "old")]:
            with self.subTest(key=key):
                data = snapshot()
                data["event"][key] = value
                self.assertEqual(decide(data)["action"], "wait")
        data = snapshot()
        data["head_sha"] = "new"
        self.assertEqual(decide(data)["action"], "wait")

    def test_incomplete_checks_block_retry_and_close(self):
        for status, conclusion in [("queued", None), ("in_progress", None), ("completed", "skipped"), ("completed", "cancelled"), ("completed", "neutral"), ("completed", "timed_out"), ("completed", None)]:
            for retried in [False, True]:
                with self.subTest(status=status, conclusion=conclusion, retried=retried):
                    data = snapshot()
                    state = None
                    if retried:
                        state = {"sha": "abc", "run_id": 12, "attempt": 1}
                        data["event"]["attempt"] = data["current_run"]["attempt"] = 2
                    data["checks"][0] = {"status": status, "conclusion": conclusion}
                    self.assertEqual(decide(data, state)["action"], "wait")

    def test_missing_and_nonfailed_checks(self):
        for checks, complete in [([], True), (None, True), ([{}], True), ([{"status": "completed", "conclusion": "success"}], True), (snapshot()["checks"], False)]:
            with self.subTest(checks=checks, complete=complete):
                data = snapshot()
                data.update(checks=checks, checks_complete=complete)
                self.assertEqual(decide(data)["action"], "wait")

    def test_no_close_for_successful_or_incomplete_ci(self):
        for status, conclusion in [("in_progress", None), ("completed", "success"), ("completed", "cancelled"), ("completed", "skipped"), ("completed", "timed_out")]:
            with self.subTest(status=status, conclusion=conclusion):
                data = snapshot()
                data["current_run"].update(status=status, conclusion=conclusion)
                self.assertEqual(decide(data)["action"], "wait")

    def test_invalid_state_never_reauthorizes_retry(self):
        for state in [{}, [], {"sha": "abc", "run_id": True, "attempt": 1}, {"sha": "abc", "run_id": 12, "attempt": 0}]:
            with self.subTest(state=state):
                self.assertEqual(decide(snapshot(), state)["action"], "wait")

    def test_missing_or_invalid_run_identity(self):
        for value in [None, {}, {"id": 12, "attempt": True, "sha": "abc"}, {"id": 0, "attempt": 1, "sha": "abc"}]:
            with self.subTest(value=value):
                data = snapshot()
                data["event"] = value
                self.assertEqual(decide(data)["action"], "wait")
        self.assertEqual(decide(None)["action"], "wait")

    def test_policy_does_not_mutate_snapshot_or_state(self):
        data, state = snapshot(), {"sha": "abc", "run_id": 12, "attempt": 1}
        before = copy.deepcopy((data, state))
        decide(data, state)
        self.assertEqual((data, state), before)


if __name__ == "__main__":
    unittest.main()
