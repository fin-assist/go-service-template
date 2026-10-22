"""Pure FIN-211 policy; callers own GitHub reads, locking and durable writes.

``decide(snapshot, state)`` consumes a fresh, normalized snapshot:

* ``pr_open``, ``author``, ``branch``, ``commit_authors`` (GitHub logins);
* ``head_sha``, ``base_ref``, ``expected_base_ref``;
* ``event`` and ``current_run`` with ``id``, ``attempt``, ``sha``;
* ``current_run`` additionally has ``status`` and ``conclusion``;
* ``checks_complete`` proves that all expected checks/status contexts were
  fetched, with pagination, for the current SHA and current CI attempt;
* ``checks`` lists their normalized ``status`` and ``conclusion``.

Only the trusted caller can establish completeness and bot identity. Never use
PR-authored JSON/comments as proof. Persist the returned retry state in a
trusted bot comment BEFORE requesting rerun-failed-jobs, under a per-PR lock.
If that API call fails, reconcile its outcome; do not clear the claim and retry
blindly. Re-read the PR/run immediately before each mutation. ``close`` also
requires installing and verifying the exact-version Dashboard quarantine first.
"""

RENOVATE = "renovate[bot]"


def _wait(reason):
    return {"action": "wait", "reason": reason}


def _positive_integer(value):
    return isinstance(value, int) and not isinstance(value, bool) and value > 0


def _run_identity(run):
    return (
        isinstance(run, dict)
        and _positive_integer(run.get("id"))
        and _positive_integer(run.get("attempt"))
        and isinstance(run.get("sha"), str)
        and bool(run["sha"])
    )


def decide(snapshot, state=None):
    """Return ``retry`` (with a claim), ``close``, or a non-mutating ``wait``.

    State is ``{sha, run_id, attempt}``, recording the failed attempt for which
    an automatic retry was claimed. A different run ID on the same SHA never
    consumes another retry or proves the outcome of the claimed retry.
    """
    if not isinstance(snapshot, dict):
        return _wait("invalid snapshot")
    authors = snapshot.get("commit_authors")
    if (
        snapshot.get("pr_open") is not True
        or snapshot.get("author") != RENOVATE
        or not isinstance(snapshot.get("branch"), str)
        or not snapshot["branch"].startswith("renovate/")
        or not isinstance(authors, list)
        or not authors
        or any(author != RENOVATE for author in authors)
    ):
        return _wait("not an untouched Renovate PR")
    if (
        not snapshot.get("expected_base_ref")
        or snapshot.get("base_ref") != snapshot["expected_base_ref"]
    ):
        return _wait("target branch is no longer current")
    event, current = snapshot.get("event"), snapshot.get("current_run")
    if not _run_identity(event) or not _run_identity(current):
        return _wait("missing run identity")
    if (
        event["id"] != current["id"]
        or event["attempt"] != current["attempt"]
        or event["sha"] != current["sha"]
        or current["sha"] != snapshot.get("head_sha")
    ):
        return _wait("stale event or changed head")
    if current.get("status") != "completed" or current.get("conclusion") != "failure":
        return _wait("CI has no completed failure")
    checks = snapshot.get("checks")
    if snapshot.get("checks_complete") is not True or not isinstance(checks, list) or not checks:
        return _wait("missing checks")
    if any(
        not isinstance(check, dict)
        or check.get("status") != "completed"
        or check.get("conclusion") not in ("success", "failure")
        for check in checks
    ):
        return _wait("checks are pending or inconclusive")
    if not any(check["conclusion"] == "failure" for check in checks):
        return _wait("no failed check")
    if state is not None:
        if (
            not isinstance(state, dict)
            or not isinstance(state.get("sha"), str)
            or not state["sha"]
            or not _positive_integer(state.get("run_id"))
            or not _positive_integer(state.get("attempt"))
        ):
            return _wait("invalid retry claim")
        if state["sha"] == current["sha"]:
            if state["run_id"] != current["id"]:
                return _wait("different run cannot prove retry outcome")
            if current["attempt"] != state["attempt"] + 1:
                return _wait("retry already claimed or attempt changed")
            return {"action": "close", "reason": "automatic retry failed on the same head"}
    return {
        "action": "retry",
        "reason": "first completed CI failure on this head",
        "state": {"sha": current["sha"], "run_id": current["id"], "attempt": current["attempt"]},
    }
