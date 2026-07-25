Read the issue first (`gh issue view ${ISSUE_NUMBER} --json body,comments --jq '.body, (.comments[-10:][] | "\(.author.login) (\(.createdAt)): \(.body)")'`
— body plus only the last 10 comments, each attributed to its author).
