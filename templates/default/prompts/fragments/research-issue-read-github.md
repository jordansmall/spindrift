- `gh issue view ${ISSUE_NUMBER} --json body,comments --jq '.body, (.comments[-10:][] | "\(.author.login) (\(.createdAt)): \(.body)")'`
  — the issue body plus only the last 10 comments, each attributed to its
  author; pull any parent/linked issue or PRD it references too.
- Any prior research comment already on the issue (look for the
  `<!-- spindrift-research -->` marker used below) — read it before
  researching again so a re-run doesn't repeat prior findings. If it isn't
  among the last 10 comments above, fetch it directly: `gh issue view ${ISSUE_NUMBER} --json comments --jq '.comments[] | select(.body | contains("spindrift-research")) | .body'`.
