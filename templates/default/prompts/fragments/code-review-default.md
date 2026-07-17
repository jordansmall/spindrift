Run the `/code-review` skill FIRST and treat its two-axis (Standards + Spec)
verdict as authoritative for sorting findings — the inline dimensions below
render either way and still name the ground to hunt; reconcile the skill's
findings into the contract below rather than skipping straight to a verdict:
Spec failures, correctness or security bugs, hard Standards violations, and
any missing or inadequate test coverage go under `## Blocking`; smells,
nits, and suggestions go under `## Non-blocking`. Still emit the
`VERDICT: APPROVE | BLOCK` line.
