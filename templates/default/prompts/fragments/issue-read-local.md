- `cat /issue-snapshot.md` — the issue body, including its full inline
  `## Comments` history as unattributed entries, captured once at box
  start. Then follow its `## Blocked by`/`parent` links to any linked issues,
  reading those directly from `/issues/${ISSUE_NUMBER}.md`-style paths in the
  same folder (pull those in too, transitively). This is a local issue with
  no GitHub-side counterpart: do not fetch it, or any linked issue, from the
  tracker — for a numeric slug, a live lookup could silently return an
  unrelated real issue on the Target repo.
- `git log -n 10 --oneline` — recent history.
