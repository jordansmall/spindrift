Run the `/code-review` skill FIRST and treat its two-axis (Standards + Spec)
verdict as authoritative — it supersedes the inline rubric below, which is only
the fallback for when the skill errors or is unavailable. Reconcile its
findings into the contract below: Spec failures, correctness or security bugs,
hard Standards violations, and any missing or inadequate test coverage go under
`## Blocking`; smells, nits, and suggestions go under `## Non-blocking`. Still
emit the `VERDICT: APPROVE | BLOCK` line.
