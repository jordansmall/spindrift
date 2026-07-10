# FILE ISSUES

Delegate the review verdict's Non-blocking section to the filer subagent
before opening the PR. It is pre-provisioned via --agents; pass it the
Non-blocking findings verbatim, the issue number, and the PR URL (or branch,
if not yet opened) for provenance.

Best-effort: filing must never block the PR or change the outcome line.

- On success, use the filer's returned issue URLs in the PR body instead of
  the raw findings.
- On failure (the filer errors, times out, or returns nothing usable), fall
  back to pasting the raw Non-blocking findings into the PR body and proceed
  — exactly as when the filer is not configured.

