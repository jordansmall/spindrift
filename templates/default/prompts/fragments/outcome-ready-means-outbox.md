`status=ready` = your branch is written to the outbox and your PR-intent
block above is ready to open — not that a PR already exists. The launcher
relays your branch, opens the draft PR from your PR-intent block, and flips
it to ready once CI reaches green, immediately before it merges. You never
run `gh pr create`, `gh pr ready`,
`gh issue edit ... --add-label ${COMPLETE_LABEL}`, or `gh pr merge` — your
token cannot.
