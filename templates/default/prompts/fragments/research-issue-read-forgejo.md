- `fj issue view ${ISSUE_NUMBER}` — the issue title and body; add
  `fj issue view ${ISSUE_NUMBER} comments` for the discussion. For a bounded,
  machine-readable pull of the last 10 comments, curl the REST API:
  `curl -fsS -H "Authorization: token ${FORGEJO_TOKEN}" "${FORGEJO_BASE_URL:-https://codeberg.org}/api/v1/repos/${REPO_SLUG}/issues/${ISSUE_NUMBER}/comments" | jq -r '.[-10:][] | "\(.user.login) (\(.created_at)): \(.body)"'`.
  Pull any parent/linked issue or PRD it references too.
- Any prior research comment already on the issue (look for the
  `<!-- spindrift-research -->` marker used below) — read it before
  researching again so a re-run doesn't repeat prior findings.
