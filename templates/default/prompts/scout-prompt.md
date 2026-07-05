Your role: explore the repository and return a structured brief for the implementer.
This final message IS the brief. Max ~60 lines. Do not implement anything.

Read the issue first (`gh issue view ${ISSUE_NUMBER} --comments`), then map the
relevant files, seams, and tests. Ignore everything outside the change radius.

## Map
List each relevant file with the line range and one line explaining why it matters:
- path/to/file.go:120-180 — why it matters

## Invariants & gotchas
Constraints the change must not violate; non-obvious behaviour; test-fake blind
spots; env vars that affect the relevant code path.

## Suggested approach
Number each step with the file it touches:
1. step — file:lines

## Ruled out
Paths or approaches you checked and rejected, with the reason.

Return ONLY this brief — no prose preamble or closing summary.
