Run the `/code-review` skill FIRST and treat its two-axis (Standards + Spec)
verdict as authoritative for sorting findings — the inline dimensions below
render either way and still name the ground to hunt; reconcile the skill's
findings into the contract below rather than skipping straight to a verdict:
Spec failures, correctness or security bugs, hard Standards violations, and
missing or inadequate test coverage for the new logic go under
`## Blocking`. Smells, nits, suggestions, and missing or inadequate tests
for a pure relocation, refactor, or comment/doc change whose behaviour is
already covered under test go under `## Non-blocking`. Still emit the
`VERDICT: APPROVE | BLOCK` line.
