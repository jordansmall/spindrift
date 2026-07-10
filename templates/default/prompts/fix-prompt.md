# TASK

Fix box for GitHub issue #${ISSUE_NUMBER}: ${ISSUE_TITLE}

Branch `${BRANCH}` is already checked out with prior work from an earlier run
that opened a pull request against `${BASE_BRANCH}` — CI came back red on it.
This is a warm fix pass, not a fresh implementation: do not re-scout, do not
re-derive the issue from scratch.

${CI_FAILURE_STEP}# CONTEXT

Read first (run these yourself) — skip anything CI FAILURE above already answered:

- `git log -n 10 --oneline` — the prior run's commits already on this branch.
- `gh pr view --json url,statusCheckRollup` — the open PR and its current CI
  state.
- `gh run list --branch ${BRANCH} --status failure --limit 5` and
  `gh run view --log-failed <run-id>` (or the CI provider's equivalent) — the
  actual failure, not a guess.

# COMMS

Your text output is a machine-parsed log, not a conversation.

- No pleasantries, acknowledgements, praise, or apologies.
- Never restate what a subagent returned.
- One terse, data-bearing status line per step — what ran, what resulted —
  no narrative framing.

Human-quality prose is reserved exclusively for: commit messages
(Conventional Commits section), the issue comment required by IF BLOCKED, and
the `note=` field of the SPINDRIFT_OUTCOME line. Everywhere else, stay terse.

# FIX

${SKILL_PREAMBLE}No SCOUT, no implement-from-scratch. Go straight to:

1. Reproduce the CI failure locally (see CHECK below).
2. Make the smallest change that fixes it. Do not refactor, redesign, or
   touch anything the failure doesn't implicate.
3. Re-run the checks that were failing to confirm the fix.

# CHECK

Before committing, run the repo's own checks green. Use what the project
defines (package scripts, Makefile, CI config). Route bulk output to a file,
load only failures:

  go test ./... > /tmp/test.log 2>&1 || tail -50 /tmp/test.log

If the repo has a `flake.nix` devShell, prefer its pinned toolchain:

  nix develop -c <check-command>   # run any check inside the devShell
  nix flake check                  # validate the full flake

If `nix develop` is unavailable or fails, fall back to the baked toolchain and
log the fallback. Go module without a devShell:

- `test -z "$(gofmt -l .)"`
- `go vet ./...`
- `go test ./...`

# COMMIT

Strict Conventional Commits v1.0.0, hard-wrapped (subject ≤50, body ≤72). One
focused commit for the fix; add a body only when the change isn't
self-evident.

**Before pushing**, rebase onto the latest base so the branch is never pushed
from a stale base:

```
git fetch origin
git rebase origin/${BASE_BRANCH}
```

Re-run the repo's checks after rebasing, then push:

```
git push --force-with-lease
```

**If the push is rejected**, do NOT silently strand the commit. Retry exactly
once:

1. `git fetch origin`
2. `git rebase origin/${BASE_BRANCH}` — resolve any conflicts, re-run checks.
3. `git push --force-with-lease` — one retry only.

If the push still fails after the retry, follow IF BLOCKED.

# LAND THE CHANGE

Check `$CODE_FORGE` (already in your environment — run `echo $CODE_FORGE` if
unsure):

**`CODE_FORGE=git`** (push-only Code Forge — no PR, no CI-watch, no merge
gate): skip WATCH CI below entirely.

1. `git push --force-with-lease` (if not already pushed).
2. Print exactly one line as your final output and stop:
   ```
   SPINDRIFT_OUTCOME issue=${ISSUE_NUMBER} pr=${BRANCH} status=ready note=<short reason>
   ```
   The launcher applies `MERGE_MODE` after this line. Do NOT run `gh pr
   create` and do NOT attempt to merge.

**`CODE_FORGE=github`** (default): the PR already exists — do NOT open a new
one. Continue with WATCH CI below.

# WATCH CI

After pushing, capture the existing PR's URL and block until CI registers
again. Right after a push, the `statusCheckRollup` state may briefly read
stale or empty — treating that as green would merge before the new CI run
starts. Wait for the rollup to return any non-empty state for the new commit:

```
PR_URL="$(gh pr view --json url -q .url)"
# gh pr checks uses the check-runs REST endpoint which 403s under fine-grained
# PATs. Use statusCheckRollup (GraphQL) instead — it works with fine-grained
# tokens and aggregates both commit statuses and check-runs faithfully, so a
# missing check reads as not-started rather than silently green.
GQL='query($owner:String!,$repo:String!,$number:Int!){repository(owner:$owner,name:$repo){pullRequest(number:$number){commits(last:1){nodes{commit{statusCheckRollup{state}}}}}}}'
owner=$(echo "$PR_URL" | cut -d/ -f4)
repo=$(echo "$PR_URL"  | cut -d/ -f5)
num=$(echo "$PR_URL"   | cut -d/ -f7)
until gh api graphql -f query="$GQL" -f owner="$owner" -f repo="$repo" \
  -F number="$num" \
  --jq '.data.repository.pullRequest.commits.nodes[0].commit.statusCheckRollup.state // ""' \
  2>/dev/null | grep -q .; do sleep 10; done
```

Run this in the foreground and block on it yourself — never background it (`&`,
detached job, background task). Backgrounding ends your turn before CI
registers, the OUTCOME line is never printed, and the run is lost.

If no check registers within a few minutes, do NOT emit `status=ready` — follow
IF BLOCKED.

Do NOT merge. The LAUNCHER (outside this container) owns the CI-green decision,
the rebase-merge, and the complete-label swap — exactly as on the initial run.
Stop once CI has registered.

# OUTCOME

(`CODE_FORGE=github` only — `CODE_FORGE=git` already printed its outcome line
and stopped under LAND THE CHANGE above.)

Once CI has registered, print exactly one line as your final output:

```
SPINDRIFT_OUTCOME issue=${ISSUE_NUMBER} pr=<pr-url> status=ready note=<short reason>
```

This must be the literal final message — nothing after it, no prose summary, no
background task. The launcher parses this one line to learn your PR; if missing,
the PR is never merged and the run is wasted. Grammar is validated by
`cmd/launcher/internal/outcome` (`Parse`, `Line`, `LastInLog`).

`status=ready` = branch pushed, CI restarted.
Do NOT run `gh issue edit ... --add-label ${COMPLETE_LABEL}` or `gh pr merge`.

# IF BLOCKED

If you can't finish (CI stays red after repeated fixes, push still fails after
the one retry, or any other blocker):

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
2. Leave the issue in-progress — do NOT close it.
3. Comment on the issue with what's done and what remains:
   `gh issue comment ${ISSUE_NUMBER} --body "<what's done, what remains>"`.
4. Print exactly one line and stop:

```
SPINDRIFT_OUTCOME issue=${ISSUE_NUMBER} pr=<pr-url> status=blocked note=<short reason>
```
