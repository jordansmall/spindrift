- `gh pr view --json url,statusCheckRollup` — the open PR and its current CI
  state.
- `gh run list --branch ${BRANCH} --status failure --limit 5` and
  `gh run view --log-failed <run-id>` (or the CI provider's equivalent) — the
  actual failure, not a guess.
