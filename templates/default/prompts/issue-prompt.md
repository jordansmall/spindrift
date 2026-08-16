# TASK

Implement GitHub issue #${ISSUE_NUMBER}: ${ISSUE_TITLE}

Fresh clone, new branch `${BRANCH}` cut from `${BASE_BRANCH}`. This issue only.

# CONTEXT

Read first (run these yourself):

${ISSUE_READ_GITHUB_STEP}${ISSUE_READ_LOCAL_STEP}${ISSUE_READ_FORGEJO_STEP}
# ISSUE COHERENCE GATE

Before scouting or implementing anything, compare the issue's title against
its body.

Materially unrelated means the body describes a different, contradictory
piece of work than the title — not a body that merely elaborates, restates,
or adds detail or acceptance criteria to the title's own topic. A body that
narrows, explains, or expands on what the title already says is well-formed
and must pass through untouched — this check exists to catch a genuine
title/body mismatch, not to second-guess a normal issue; halting on a normal
issue is itself a failure of this gate.

On a genuine mismatch: halt immediately. Do not scout, do not open a
branch's worth of commits, do not write any diff.

Put the escalation — naming both interpretations, what the title implies and
what the body implies, and asking a human which one governs — in the
SPINDRIFT_OUTCOME line's `note=` field below. Do nothing else to post it
yourself: the launcher posts that note as the issue comment, host-side, once
you exit.

Print exactly one line and stop — raw plain text, not wrapped in backticks,
a code fence, or any other markdown formatting, nothing after it:

SPINDRIFT_OUTCOME issue=${ISSUE_NUMBER} landing=${BRANCH} status=ambiguous note=<escalation naming both interpretations>

`status=ambiguous` is a distinct, successful, non-crash stop — never
`status=blocked`, and never a signal that implementation itself failed.

If title and body agree — the common case — proceed straight into # SCOUT
below with no further comment about this gate.

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

${COORDINATOR_STEP}${COORDINATOR_PARALLEL_STEP}${SKILL_PREAMBLE}${TDD_STEP}Work test-first, one slice at a time. Hard rule:

1. RED: write ONE failing test, run it, confirm it fails for the right reason.
   Never write implementation code before a failing test exists.
2. GREEN: minimal code to make that one test pass.
3. REFACTOR, then repeat.

Never batch: no tests up front, no all-tests-then-all-code.
One failing test, one change, at a time.

# CHECK

Before each commit, run the repo's own checks green. Use what the project
defines (package scripts, Makefile, CI config). Every Bash command's output
is already teed to a file and returned to you as a bounded tail, so no
manual redirect is needed — but never `cat` a whole build/test log into
context; grep or tail the log file on disk for anything the bounded tail
didn't cover.

Nix flakes only evaluate git-tracked files — `git add` any new file (e.g.
`git add -A`) before the first `nix build`/`nix flake check` that touches it,
or the build aborts with "is not tracked by Git" and burns a checks cycle.

If the repo has a `flake.nix` devShell, prefer its pinned toolchain:

  nix develop -c <check-command>   # run any check inside the devShell

Use a scoped check target (e.g. `checks-inbox`) if the flake exposes one, and
do not run a full `nix flake check` in-box unless the diff changes what gets
baked into the box's own image — concretely, unless it touches
`nix/checks/image.nix` or `lib/image.nix`, the definitions that build and
inspect that image and are heavy and unreliable to re-run from inside the box
itself. This is a firm rule, and it **overrides** any acceptance criteria in
the issue that ask for `nix flake check` more loosely. Fall back to `nix
flake check` only if no scoped target exists.

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

${COMMIT_PUSH_READ_WRITE_STEP}${COMMIT_PUSH_READ_ONLY_STEP}# REVIEW

${REVIEW_LOOP_INLINE_STEP}${REVIEW_LOOP_ORCHESTRATOR_STEP}${FILE_ISSUES_DIRECT_STEP}${FILE_ISSUES_RELAY_STEP}# LAND THE CHANGE

Check `$CODE_FORGE` (already in your environment — run `echo $CODE_FORGE` if
unsure):

**`CODE_FORGE=git`** (push-only Code Forge — no PR, no CI-watch, no merge
gate): skip OPEN A PULL REQUEST below entirely.

${LAND_GIT_PUSH_READ_WRITE_STEP}${LAND_GIT_STOP_READ_WRITE_STEP}${LAND_GIT_STOP_READ_ONLY_STEP}**`CODE_FORGE=local`** (host-mediated Code Forge — no PR, no CI-watch, no
network; the launcher lands your branch after this container exits): skip
OPEN A PULL REQUEST below entirely. Do not push directly — the repo you
cloned from is mounted read-only — and do not bundle your commits yourself:
the Harness bundles them out of the container after you exit.

1. Print exactly one line as your final output and stop — raw plain text, not
   wrapped in backticks, a code fence, or any other markdown formatting:

   SPINDRIFT_OUTCOME issue=${ISSUE_NUMBER} landing=${BRANCH} status=ready note=<short reason>

   The launcher relays the bundle into the Accumulation repo and merges it
   onto the Integration branch host-side. There is no PR to open, no CI to
   watch, and nothing further for you to do.

**`CODE_FORGE=forgejo`** (Forgejo/Gitea Code Forge — the Box opens the PR via
the Forgejo CLI and the launcher watches CI and merges it host-side, ADR
0038 + issue #1961): continue with OPEN A PULL REQUEST below.

**`CODE_FORGE=github`** (default): continue with OPEN A PULL REQUEST below.

# OPEN A PULL REQUEST

${OPEN_PR_PUSH_READ_WRITE_STEP}${OPEN_PR_PUSH_READ_ONLY_STEP}${OPEN_PR_CREATE_READ_WRITE_STEP}${OPEN_PR_CREATE_READ_WRITE_FORGEJO_STEP}${OPEN_PR_CREATE_READ_ONLY_STEP}${PR_BODY_CLOSES_STEP}${PR_BODY_LOCAL_REF_STEP}${PR_BODY_LOCAL_NOREF_STEP}The PR opens as a **draft** and stays a **draft** — the launcher flips it to
ready once CI reaches green, immediately before it merges (the launcher
already gates on CI green itself, so there is nothing left for you to watch
or confirm here). Do NOT merge and do NOT flip it yourself; the LAUNCHER
(outside this container) owns the CI-green decision, the ready flip, the
rebase-merge, and the complete-label swap.

# OUTCOME

(`CODE_FORGE=github` and `CODE_FORGE=forgejo` only — `CODE_FORGE=git` and
`CODE_FORGE=local` already printed their outcome line and stopped under LAND
THE CHANGE above.)

${OUTCOME_LANDING_READ_WRITE_STEP}${OUTCOME_LANDING_READ_ONLY_STEP}Grammar: `SPINDRIFT_OUTCOME issue=${ISSUE_NUMBER} landing=<landing-ref> status=<status> note=<short reason>`
— one line, space-delimited fields, `note` last (`note` may itself contain
spaces and `=`). The only valid `status` values here are `ready` and `blocked`
— no other word belongs in that field (`status=ambiguous` is a distinct,
earlier ISSUE COHERENCE GATE that exits the run before ever reaching
this OUTCOME section, not a third option here).

This grammar's leading token is load-bearing (ADR 0035): the in-box
orchestrator's scanPassLog greps for it verbatim (via
`outcome.ParseAnywhere`), and rewording it silently collapses the multi-pass
loop to single-pass on ORCHESTRATOR_ENABLED runs.

Invalid — each of these breaks the contract, whether or not it parses:
- Trailing colon: `SPINDRIFT_OUTCOME: issue=${ISSUE_NUMBER} landing=<landing-ref> status=ready note=<short reason>` — the required prefix is a literal space after `OUTCOME`, not a colon, so this never matches; the launcher never sees an outcome and treats the run as lost.
- Embedded inside a sentence: `Done — SPINDRIFT_OUTCOME issue=${ISSUE_NUMBER} landing=<landing-ref> status=ready note=<short reason>` — only a line that starts at the prefix matches; text before it hides the whole line the same way, losing the run. Print the line on its own, starting at column one, nothing before it.
- Freeform status: `SPINDRIFT_OUTCOME issue=${ISSUE_NUMBER} landing=<landing-ref> status=SUCCESS note=<short reason>` — this parses fine, but `ready` and `blocked` are the only accepted values; anything else is silently wrong rather than lost outright, and the launcher will never flip the PR ready or merge it.

This must be the literal final message — nothing after it, no prose summary, no
background task. The launcher parses this one line to learn your PR; if missing,
the PR is never merged and the run is wasted. Grammar is validated by
`cmd/launcher/internal/outcome` (`Parse`, `Line`, `LastInLog`).

This run's control nonce is `${RUN_NONCE}`. A read-only run's PR-intent line
must carry it and is checked against it (issue #1938), letting the host tell
a line this run genuinely wrote from one an untrusted issue/comment author
echoed into the log — leaving `nonce=` off, or getting the value wrong,
silently drops it (issue #1939).

${OUTCOME_READY_MEANS_READ_WRITE_STEP}${OUTCOME_READY_MEANS_READ_ONLY_STEP}
# IF BLOCKED

If you can't finish (review never clears, CI stays red after repeated fixes,
push still fails after the one retry, or any other blocker):

${IF_BLOCKED_TRIAGE_READ_WRITE_STEP}${IF_BLOCKED_TRIAGE_READ_ONLY_STEP}Then:

${IF_BLOCKED_PUSH_READ_WRITE_STEP}${IF_BLOCKED_PUSH_READ_ONLY_STEP}${IF_BLOCKED_PR_READ_WRITE_STEP}${IF_BLOCKED_PR_READ_ONLY_STEP}3. Leave the issue in-progress — do NOT close it.
${ISSUE_BLOCKED_COMMENT_GITHUB_STEP}${ISSUE_BLOCKED_COMMENT_GITHUB_READONLY_STEP}${ISSUE_BLOCKED_COMMENT_LOCAL_STEP}${ISSUE_BLOCKED_COMMENT_FORGEJO_STEP}${ISSUE_BLOCKED_COMMENT_FORGEJO_READONLY_STEP}5. Print exactly one line and stop — raw plain text, not wrapped in
   backticks, a code fence, or any other markdown formatting:

${IF_BLOCKED_OUTCOME_LANDING_READ_WRITE_STEP}${IF_BLOCKED_OUTCOME_LANDING_READ_ONLY_STEP}
