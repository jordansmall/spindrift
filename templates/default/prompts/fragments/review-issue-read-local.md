  cat /issue-snapshot.md                          # issue body, full inline `## Comments` history (uncapped), captured at box start
  # then follow its `## Blocked by`/`parent` links into any linked issue,
  # reading it directly from /issues/${ISSUE_NUMBER}.md-style paths in the
  # same folder (pull those in too, transitively) -- this is a local issue
  # with no GitHub-side counterpart, so never fetch it, or any linked issue,
  # from a live tracker by number
