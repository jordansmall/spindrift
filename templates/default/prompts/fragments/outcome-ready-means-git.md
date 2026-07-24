`status=ready` = branch pushed, PR open, left in draft. The launcher flips it
to ready once CI reaches green, immediately before it merges.
Do NOT run `gh pr ready`, `gh issue edit ... --add-label ${COMPLETE_LABEL}`,
or `gh pr merge`.
