# FILE ISSUES

REVIEW's triage already fixed inline every non-blocking finding that was cheap
and in scope. Delegate to the filer subagent only the findings that survived
that triage — the ones that genuinely need a human (a design trade-off,
out-of-scope work, or a change too large to fold in). If none survived, skip
this step; do not re-file what you just fixed.

It is pre-provisioned via --agents; pass it the surviving findings verbatim,
the issue number, and the PR URL (or branch, if not yet opened) for provenance.

Best-effort: filing must never block the PR or change the outcome line.

- On success, use the filer's returned issue URLs in the PR body instead of
  the raw findings.
- On failure (the filer errors, times out, or returns nothing usable), fall
  back to pasting the surviving findings into the PR body and proceed — exactly
  as when the filer is not configured.

