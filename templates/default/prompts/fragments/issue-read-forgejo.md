- `fj issue view ${ISSUE_NUMBER}` — the issue title and body; add the
  `comments` subcommand (`fj issue view ${ISSUE_NUMBER} comments`) for the
  discussion. fj has no last-N cap, so on a long thread read only the most
  recent comments. For a bounded, machine-readable pull, curl the REST API
  instead: `curl -fsS -H "Authorization: token ${FORGEJO_TOKEN}" "${FORGEJO_BASE_URL:-https://codeberg.org}/api/v1/repos/${REPO_SLUG}/issues/${ISSUE_NUMBER}/comments" | jq -r '.[-10:][] | "\(.user.login) (\(.created_at)): \(.body)"'`
  — the last 10 comments, each attributed to its author. Pull any
  parent/linked issue or PRD it references too.
- `git log -n 10 --oneline` — recent history.
