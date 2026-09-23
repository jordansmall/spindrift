# FILE ISSUES

REVIEW's triage has three outcomes, and two of them are already settled by
the time the filer runs: what was cheap and in scope was fixed inline, and
what was trivial and out of scope was dropped without filing. Escalation is
the third, and it is this step. Delegate to the filer subagent only the
findings that survived that triage — the ones that genuinely need a human
(a design trade-off, out-of-scope work, or a change too large to fold in),
plus, from the second review round on and only when fixing it would widen
the diff, an ambiguous finding REVIEW's own round-aware tiebreak deferred
rather than fixed — one whose fix is small and stays inside what the branch
already touches is still fixed inline, at every round. If none survived,
skip this step; do not re-file what you just fixed or dropped.

It is pre-provisioned via --agents; pass it the surviving findings verbatim,
the issue number, and the branch (or PR URL, once one is open) for provenance.
Your token is read-only, so the filer cannot file issues itself — it sends
each one over the Signal socket via `driver-exec signal issue-intent`
instead, and the launcher files each one host-side once you exit, so no
issue URL is known yet.

Best-effort: filing must never block the PR or change the outcome line.

- On success (the filer reports `QUEUED`), just note in the PR body that the
  finding(s) were queued for filing — never paste the raw findings
  themselves, they're already tracked.
- On failure (the filer errors, times out, or returns nothing usable), fall
  back to pasting the surviving findings into the PR body and proceed — exactly
  as when the filer is not configured.
