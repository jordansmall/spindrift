# TASK

Implement GitHub issue #${ISSUE_NUMBER}: ${ISSUE_TITLE}

You are in a fresh clone of the repository, on a new branch `${BRANCH}` cut from
`${BASE_BRANCH}`. Work on ONLY this one issue.

# CONTEXT

Start by reading the issue and recent history (run these yourself):

- `gh issue view ${ISSUE_NUMBER} --comments` — the issue, plus any parent or
  linked issue / PRD it references (pull those in too).
- `git log -n 10 --oneline` — recent history, for orientation.

# SCOUT

Before reading any source files yourself, delegate exploration to the `scout`
subagent (if available in this session; otherwise explore inline):

> Use the `scout` subagent: map the files, seams, and existing tests relevant
> to this issue. Return file paths and line references — do not implement
> anything.

Use the scout's map to load only the relevant parts into your own context.
Re-scout only if a later finding reveals the change sits in the wrong place.

# IMPLEMENT

Using the scout's map, work test-first in a tight red→green→refactor loop:

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

# REVIEW

Before opening the PR, spawn a fresh `reviewer` subagent to evaluate the branch
diff against `${BASE_BRANCH}` with this rubric.

**Do not review inline.** An inline review writes the findings as your
turn-ending message — the review looks like the finish line when it is only the
halfway gate. Delegating to the `reviewer` subagent returns a result you act on,
not a conclusion you end on. The `reviewer` is pre-provisioned via `--agents`
when `REVIEW_MODEL` is set; fall back to inline review only when the reviewer
subagent is not available (tier models genuinely unset).

**SPEC** — Does the diff do exactly what issue #${ISSUE_NUMBER} asked and
nothing more? Are all acceptance criteria satisfied?

**STANDARDS** — Does the code follow the repo's documented coding standards,
test conventions, and commit style?

The reviewer must be a fresh subagent with clean context — not the same context
that did the building.

If the reviewer surfaces a BLOCKING finding:

1. Fix the code on this branch, run checks, recommit.
2. Re-invoke the `reviewer` subagent (fresh instance, not the same one).
3. Repeat until no blocking findings remain.
4. Re-scout only if the finding reveals the change is in the wrong place.

Never advance to opening the PR with a blocking finding open.
Non-blocking findings (style nits, suggestions) may be noted in the PR body.

# OPEN A PULL REQUEST

1. `git push -u origin ${BRANCH}`
2. Open a PR into `${BASE_BRANCH}`:
   `gh pr create --base ${BASE_BRANCH} --head ${BRANCH} --title "<conventional title>" --body "<summary>"`
3. The PR body MUST contain `Closes #${ISSUE_NUMBER}` so merging it closes the
   issue. Summarize what changed and flag anything a reviewer should know.

# WATCH CI

After opening the PR, wait for CI to **register** before you trust it. Run right
after `gh pr create`, `gh pr checks --watch` finds no checks yet, prints "no
checks" and exits 0 — and treating that as green merges before CI even starts.
So first block until at least one check appears, then watch it to completion:

```
# "no checks" yet means not-started, NOT green — wait for a check to register
until gh pr checks <pr-number> 2>/dev/null | grep -q .; do sleep 10; done
gh pr checks <pr-number> --watch
```

If no check ever registers within a few minutes, do NOT merge — treat it as a
blocker and follow IF BLOCKED.

**Never merge on red, and never merge on "no checks".** If any check fails:

1. Fix the code on the branch, run local checks, commit, and push.
2. Re-watch: `gh pr checks <pr-number> --watch`.
3. Repeat until all checks are green.

# MERGE

Once all checks are green, merge with rebase and delete the branch:

```
gh pr merge <pr-number> --rebase --delete-branch
```

Do not use auto-merge. Hold the thread open for the entire fix-on-red loop.

# COMPLETE

After merging, swap the lifecycle label:

```
gh issue edit ${ISSUE_NUMBER} --remove-label ${IN_PROGRESS_LABEL} --add-label ${COMPLETE_LABEL}
```

# OUTCOME

As your **final action**, print exactly one machine-readable line:

```
SPINDRIFT_OUTCOME issue=${ISSUE_NUMBER} pr=<pr-url> status=merged note=<short reason>
```

# IF BLOCKED

If you cannot finish (review never clears, CI stays red after repeated fixes, or
any other blocker):

1. Push what you have.
2. Open the PR as a draft (add `--draft`).
3. Leave the issue in-progress — do NOT close it.
4. Comment on the issue: `gh issue comment ${ISSUE_NUMBER} --body "<what's done, what remains>"`.
5. Print exactly one outcome line and stop:

```
SPINDRIFT_OUTCOME issue=${ISSUE_NUMBER} pr=<pr-url> status=blocked note=<short reason>
```
