# FILE ISSUES

${FILE_ISSUES_TRIAGE_RECAP_STEP}It is pre-provisioned; pass it the surviving findings verbatim,
the issue number, and the branch (or PR URL, once one is open) for provenance.

Best-effort: filing must never block the PR or change the outcome line.

- On success, use the filer's returned issue URLs in the PR body instead of
  the raw findings.
- On failure (the filer errors, times out, or returns nothing usable), fall
  back to pasting the surviving findings into the PR body and proceed — exactly
  as when the filer is not configured.

