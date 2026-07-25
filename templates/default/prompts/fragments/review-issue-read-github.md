  gh issue view ${ISSUE_NUMBER} --json body,comments --jq '.body, (.comments[-10:][] | "\(.author.login) (\(.createdAt)): \(.body)")'  # acceptance criteria, last 10 comments
