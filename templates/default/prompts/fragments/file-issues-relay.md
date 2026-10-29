# FILE ISSUES

${FILE_ISSUES_TRIAGE_RECAP_STEP}It is pre-provisioned; pass it the surviving findings verbatim,
the issue number, and the branch for provenance.
Your token is read-only, so the filer cannot file issues itself — it emits
`SPINDRIFT_ISSUE_INTENT` lines instead, and the launcher files each one
host-side once you exit, so no issue URL is known yet.

Best-effort: filing must never block the PR or change the outcome line.

- On success (the filer reports `QUEUED`), just note in the PR body that the
  finding(s) were queued for filing — never paste the raw findings
  themselves, they're already tracked.
- On failure (the filer errors, times out, or returns nothing usable), fall
  back to pasting the surviving findings into the PR body and proceed — exactly
  as when the filer is not configured.
