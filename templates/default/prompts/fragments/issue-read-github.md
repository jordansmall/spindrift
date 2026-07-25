- `gh issue view ${ISSUE_NUMBER} --json body,comments --jq '.body, (.comments[-10:][] | "\(.author.login) (\(.createdAt)): \(.body)")'`
  — the issue body plus only the last 10 comments, each attributed to its
  author; long threads carry stale design discussion that doesn't need
  re-reading every turn. Pull any parent/linked issue or PRD it references
  too.
- `git log -n 10 --oneline` — recent history.
