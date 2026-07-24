# TASK

Implement GitHub issue #${ISSUE_NUMBER}: ${ISSUE_TITLE}

Fresh clone, new branch `${BRANCH}` cut from `${BASE_BRANCH}`. This issue only.

# CONTEXT

Read first (run these yourself):

${ISSUE_READ_GITHUB_STEP}${ISSUE_READ_LOCAL_STEP}
# COMMS

${CAVEMAN_STEP}Your text output is a machine-parsed log, not a conversation.

- No pleasantries, acknowledgements, praise, or apologies.
- Never restate what a subagent returned.
- One terse, data-bearing status line per step — what ran, what resulted —
  no narrative framing.

Human-quality prose is reserved exclusively for: commit messages
(Conventional Commits section), the PR title and body, the issue comment
required by IF BLOCKED, and the `note=` field of the SPINDRIFT_OUTCOME line.
Everywhere else, stay terse.

# SCOUT

Delegate exploration to the `scout` subagent before reading source yourself:

> scout: map the files, seams, and existing tests relevant to this issue.
> Return paths and line refs. Do not implement.

Persist the brief to `/tmp/brief.md` (outside the repo, never commit) so it
survives compaction. Trust it — jump to the pointers, re-search only on a
wrong/missing pointer. Re-scout only if a finding shows the change belongs
elsewhere.

# IMPLEMENT

${SKILL_PREAMBLE}${TDD_STEP}Work test-first, one slice at a time. Hard rule:

1. RED: write ONE failing test, run it, confirm it fails for the right reason.
   Never write implementation code before a failing test exists.
2. GREEN: minimal code to make that one test pass.
3. REFACTOR, then repeat.

Never batch: no tests up front, no all-tests-then-all-code.
One failing test, one change, at a time.

# CHECK

Before each commit, run the repo's own checks green. Use what the project
defines (package scripts, Makefile, CI config). Route bulk output to a file,
load only failures:

  go test ./... > /tmp/test.log 2>&1 || tail -50 /tmp/test.log

Nix flakes only evaluate git-tracked files — `git add` any new file (e.g.
`git add -A`) before the first `nix build`/`nix flake check` that touches it,
or the build aborts with "is not tracked by Git" and burns a checks cycle.

If the repo has a `flake.nix` devShell, prefer its pinned toolchain:

  nix develop -c <check-command>   # run any check inside the devShell
  nix flake check                  # validate the full flake

If `nix develop` is unavailable or fails, fall back to the baked toolchain and
log the fallback. Go module without a devShell:

- `test -z "$(gofmt -l .)"`
- `go vet ./...`
- `go test ./...`

Run every check or build gate in the foreground and block on it yourself —
never background it (`&`, detached job, background task) and end your turn
while it is still pending. Backgrounding a gate here means your turn ends
before the gate finishes, no `SPINDRIFT_OUTCOME` line is ever printed, and
the run is lost even when the underlying work was green. Wait for the gate
to finish before moving on, and do not stop this run until a terminal
`SPINDRIFT_OUTCOME` line (`status=ready` or `status=blocked`) has been
printed.

If you ever fall back to a background-and-poll pattern for a gate anyway,
treat a vanished process as a failure, not as still-pending: a build that is
killed outright (OOM, SIGKILL) never writes the exit marker you are polling
for, so an unbounded wait for it hangs forever. Bound the wait, and the
moment the marker fails to show up, emit a `status=blocked`
`SPINDRIFT_OUTCOME` instead of looping.

${AUTO_FORMAT_STEP}${AUTO_LINT_STEP}# COMMIT

${COMMIT_STEP}Strict Conventional Commits v1.0.0, hard-wrapped (subject ≤50, body ≤72).
Prefer several small focused commits over one big one — commit each logical
unit (domain change, then wiring, then tests) so each stands alone. Add a body
only when the change isn't self-evident.

**Always rebase onto the latest base immediately before every push** — never
push from a stale base. This keeps the branch's tested tree current with any
siblings that landed while you worked: the launcher merges a green PR as-is and
does not re-rebase it for you, so a fresh base at push time is the branch's
freshness guarantee (a stale base also produces phantom diffs that trip push
guards):

```
git fetch origin
git rebase origin/${BASE_BRANCH}
```

Re-run the repo's checks after rebasing, then push:

```
git push --force-with-lease -u origin ${BRANCH}   # first push
git push --force-with-lease                        # subsequent
```

**If a push is rejected**, do NOT silently strand the commits. Retry exactly
once:

1. `git fetch origin`
2. `git rebase origin/${BASE_BRANCH}` — resolve any conflicts, re-run checks.
3. `git push --force-with-lease` — one retry only.

If the push still fails after the retry, follow IF BLOCKED.

# REVIEW

Before the PR, spawn a fresh `reviewer` subagent on the branch diff vs
`${BASE_BRANCH}`. Do NOT review inline — an inline review ends your turn at the
halfway gate; delegating returns a result to act on. The `reviewer` is
pre-provisioned via `--agents`; pass only the issue number.

Its final message starts `VERDICT: APPROVE` or `VERDICT: BLOCK`. On BLOCK:

1. Fix on this branch, run checks, then commit. Unless the fix is a
   reasonably different change, fold it into the existing commit where it
   logically belongs — `git commit --amend` or an autosquash fixup — rather
   than tacking on a follow-up. Add a *new* commit only when the fix is
   truly a separate file or scope. The branch force-pushes, so rewriting
   your own unmerged history here is expected.
2. Re-invoke a fresh `reviewer` (not the same instance).
3. Repeat until no blocking findings remain.
4. Re-scout only if the finding shows the change is in the wrong place.

Never open the PR with a blocking finding open.

Then triage the Non-blocking findings — do NOT reflexively file them. Filing
every finding spawns more issues than the work ever closes; the default is to
resolve them here, in this loop:

1. Fix inline, on this branch, every finding whose fix is cheap and in scope
   for this change — most nits, smells, dead code, misleading names, and doc
   updates for a surface this diff already touches. Re-run checks, then commit
   them the same way — amended into the commit each logically belongs to
   unless it is a reasonably separate scope, which earns its own commit. They
   never become issues.
2. Escalate — to the filer if present, else the PR body — only a finding that
   genuinely needs a human: a real design trade-off, work outside this issue's
   scope, or a change too large to fold in without derailing the slice. When
   unsure whether a finding clears that bar, fix it rather than file it.

${FILE_ISSUES_STEP}# LAND THE CHANGE

Check `$CODE_FORGE` (already in your environment — run `echo $CODE_FORGE` if
unsure):

**`CODE_FORGE=git`** (push-only Code Forge — no PR, no CI-watch, no merge
gate): skip OPEN A PULL REQUEST below entirely.

1. `git push --force-with-lease -u origin ${BRANCH}` (if not already pushed).
2. Print exactly one line as your final output and stop — raw plain text, not
   wrapped in backticks, a code fence, or any other markdown formatting:

   SPINDRIFT_OUTCOME issue=${ISSUE_NUMBER} landing=${BRANCH} status=ready note=<short reason>

   The launcher applies `MERGE_MODE` after this line (push straight to the
   target branch on `immediate`; leave the branch as pushed on `manual`).
   Do NOT run `gh pr create` and do NOT attempt to merge.

**`CODE_FORGE=local`** (host-mediated Code Forge — no PR, no CI-watch, no
network; the launcher lands your branch after this container exits): skip
OPEN A PULL REQUEST below entirely. Do NOT `git push` — the repo you cloned
from is mounted read-only — and do NOT run `git bundle create` yourself: the
Harness bundles your commits out of the container after you exit.

1. Print exactly one line as your final output and stop — raw plain text, not
   wrapped in backticks, a code fence, or any other markdown formatting:

   SPINDRIFT_OUTCOME issue=${ISSUE_NUMBER} landing=${BRANCH} status=ready note=<short reason>

   The launcher relays the bundle into the Accumulation repo and merges it
   onto the Integration branch host-side. There is no PR to open, no CI to
   watch, and nothing further for you to do.

**`CODE_FORGE=github`** (default): continue with OPEN A PULL REQUEST below.

# OPEN A PULL REQUEST

1. `git push --force-with-lease -u origin ${BRANCH}`
2. `gh pr create --draft --base ${BASE_BRANCH} --head ${BRANCH} --title "<conventional title>" --body "<summary>"`
${PR_BODY_CLOSES_STEP}${PR_BODY_LOCAL_REF_STEP}${PR_BODY_LOCAL_NOREF_STEP}The PR opens as a **draft** and stays a **draft** — the launcher flips it to
ready once CI reaches green, immediately before it merges (the launcher
already gates on CI green itself, so there is nothing left for you to watch
or confirm here). Do NOT merge and do NOT flip it yourself; the LAUNCHER
(outside this container) owns the CI-green decision, the ready flip, the
rebase-merge, and the complete-label swap.

# OUTCOME

(`CODE_FORGE=github` only — `CODE_FORGE=git` and `CODE_FORGE=local` already
printed their outcome line and stopped under LAND THE CHANGE above.)

Print exactly one line as your final output — raw plain text, not wrapped in
backticks, a code fence, or any other markdown formatting:

SPINDRIFT_OUTCOME issue=${ISSUE_NUMBER} landing=<pr-url> status=ready note=<short reason>

Grammar: `SPINDRIFT_OUTCOME issue=${ISSUE_NUMBER} landing=<pr-url> status=<status> note=<short reason>`
— one line, space-delimited fields, `note` last (it may itself contain spaces
and `=`). The only valid `status` values here are `ready` and `blocked` — no
other word belongs in that field.

Invalid — each of these breaks the contract, whether or not it parses:
- Trailing colon: `SPINDRIFT_OUTCOME: issue=${ISSUE_NUMBER} landing=<pr-url> status=ready note=<short reason>` — the required prefix is a literal space after `OUTCOME`, not a colon, so this never matches; the launcher never sees an outcome and treats the run as lost.
- Embedded inside a sentence: `Done — SPINDRIFT_OUTCOME issue=${ISSUE_NUMBER} landing=<pr-url> status=ready note=<short reason>` — only a line that starts at the prefix matches; text before it hides the whole line the same way, losing the run. Print the line on its own, starting at column one, nothing before it.
- Freeform status: `SPINDRIFT_OUTCOME issue=${ISSUE_NUMBER} landing=<pr-url> status=SUCCESS note=<short reason>` — this parses fine, but `ready` and `blocked` are the only accepted values; anything else is silently wrong rather than lost outright, and the launcher will never flip the PR ready or merge it.

This must be the literal final message — nothing after it, no prose summary, no
background task. The launcher parses this one line to learn your PR; if missing,
the PR is never merged and the run is wasted. Grammar is validated by
`cmd/launcher/internal/outcome` (`Parse`, `Line`, `LastInLog`).

`status=ready` = branch pushed, PR open, left in draft. The launcher flips it
to ready once CI reaches green, immediately before it merges.
Do NOT run `gh pr ready`, `gh issue edit ... --add-label ${COMPLETE_LABEL}`,
or `gh pr merge`.

# IF BLOCKED

If you can't finish (review never clears, CI stays red after repeated fixes,
push still fails after the one retry, or any other blocker):

**Push failure — check the actual cause before reporting it.** Do not guess.
Run:

```
git diff origin/${BASE_BRANCH} -- '.github/workflows/'
```

- **No diff (phantom delta):** The pre-push rebase-and-retry above should have
  cleared this. If the push still fails, capture and report the actual push
  error output.
- **Genuine `.github/workflows/` change:** The agent's token intentionally
  lacks `workflow` scope — this is a deliberate security boundary. Do NOT
  attempt to acquire broader scope or route around it. Comment on the issue
  explaining what changes were made and why they require human review with
  `workflow` scope, then emit `status=blocked`.
- **Any other rejection:** Report the literal push error output. Never
  attribute a failure to a cause you have not verified.

Then:

1. Push what you have (or note if even that is impossible).
2. Check whether a PR already exists on this branch (`gh pr view --json url`).
   If not, open one as a draft (`--draft`). If it does, leave it as-is — the
   Driver never flips a PR to ready, so it is already draft and there is
   nothing to revert.
3. Leave the issue in-progress — do NOT close it.
${ISSUE_BLOCKED_COMMENT_GITHUB_STEP}${ISSUE_BLOCKED_COMMENT_GITHUB_READONLY_STEP}${ISSUE_BLOCKED_COMMENT_LOCAL_STEP}5. Print exactly one line and stop — raw plain text, not wrapped in
   backticks, a code fence, or any other markdown formatting:

   SPINDRIFT_OUTCOME issue=${ISSUE_NUMBER} landing=<pr-url> status=blocked note=<short reason>
