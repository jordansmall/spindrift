- `/issues/${ISSUE_NUMBER}.md` — read it directly, then follow its `## Blocked
  by`/`parent` links to any linked issues in the same folder (pull those in
  too, transitively). This is a local issue with no GitHub-side counterpart:
  do not fetch it from the tracker — for a numeric slug, a live lookup could
  silently return an unrelated real issue on the Target repo.
- `git log -n 10 --oneline` — recent history.
