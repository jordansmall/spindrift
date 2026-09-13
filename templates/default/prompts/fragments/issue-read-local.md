- Follow the issue's `## Blocked by`/`parent` links to any linked issues in
  the same folder and pull those in too, transitively — the subject issue's
  own body is already in the # ISSUE TEXT section below. When you pull a
  linked issue, read it from the local folder: a live tracker lookup by
  number could silently return an unrelated real issue on the Target repo.
- `git log -n 10 --oneline` — recent history.
