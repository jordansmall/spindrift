# TASK

Implement GitHub issue #${ISSUE_NUMBER}: ${ISSUE_TITLE}

You are in a fresh clone of the repository, on a new branch `${BRANCH}` cut from
`${BASE_BRANCH}`. Work on ONLY this one issue.

# CONTEXT

Start by reading the issue and recent history (run these yourself):

- `gh issue view ${ISSUE_NUMBER} --comments` — the issue, plus any parent or
  linked issue / PRD it references (pull those in too).
- `git log -n 10 --oneline` — recent history, for orientation.

# EXPLORATION

Explore the repo and load the parts relevant to this issue into context. Pay
special attention to existing tests that touch the code you'll change, and to
any `CLAUDE.md` or coding-standards files in the repo — follow them exactly.

# IMPLEMENT

Prefer a tight red→green→refactor loop where it fits:

1. RED: write one failing test for the next slice of behaviour.
2. GREEN: write the minimal code to make it pass.
3. REPEAT until the issue is satisfied, then REFACTOR.

# CHECK

Before each commit, run the repo's own checks and make them pass. Use whatever
the project actually defines (package scripts, a Makefile, CI config). For a
Rust workspace that is:

- `cargo fmt --all --check`
- `cargo clippy --all-targets --all-features -- -D warnings`
- `cargo test`

# COMMIT

If the repo provides a `commit` skill, use it. Otherwise write strict
Conventional Commits v1.0.0 messages, hard-wrapped.

Prefer several small, focused commits over one big commit — commit each logical
unit (e.g. a domain change, then the wiring, then tests) so each stands alone
and is reviewable in isolation. Add a body explaining the what/why only when the
change isn't self-evident.

# OPEN A PULL REQUEST

1. `git push -u origin ${BRANCH}`
2. Open a PR into `${BASE_BRANCH}`:
   `gh pr create --base ${BASE_BRANCH} --head ${BRANCH} --title "<conventional title>" --body "<summary>"`
3. The PR body MUST contain `Closes #${ISSUE_NUMBER}` so merging it closes the
   issue. Summarize what changed and flag anything a reviewer should know.

Do NOT merge the PR and do NOT close the issue — a human reviews and merges.

# IF BLOCKED

If you can't finish, push what you have, open the PR as a draft (add `--draft`),
and comment on the issue describing what's done and what remains. Never close
the issue.

When the PR is open, you're done — stop.
