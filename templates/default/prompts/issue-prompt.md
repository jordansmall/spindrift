# TASK

Implement GitHub issue #${ISSUE_NUMBER}: ${ISSUE_TITLE}

Fresh clone, new branch `${BRANCH}` cut from `${BASE_BRANCH}`. This issue only.

# CONTEXT

Read first (run these yourself):

- `gh issue view ${ISSUE_NUMBER} --comments` — the issue plus any parent/linked
  issue or PRD it references (pull those in too).
- `git log -n 10 --oneline` — recent history.

# SCOUT

Delegate exploration to the `scout` subagent before reading source yourself:

> scout: map the files, seams, and existing tests relevant to this issue.
> Return paths and line refs. Do not implement.

Persist the brief to `/tmp/brief.md` (outside the repo, never commit) so it
survives compaction. Trust it — jump to the pointers, re-search only on a
wrong/missing pointer. Re-scout only if a finding shows the change belongs
elsewhere.

# IMPLEMENT

${SKILL_PREAMBLE}Work test-first, one slice at a time. Hard rule:

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

If the repo has a `flake.nix` devShell, prefer its pinned toolchain:

  nix develop -c <check-command>   # run any check inside the devShell
  nix flake check                  # validate the full flake

If `nix develop` is unavailable or fails, fall back to the baked toolchain and
log the fallback. Go module without a devShell:

- `test -z "$(gofmt -l .)"`
- `go vet ./...`
- `go test ./...`

# COMMIT

Strict Conventional Commits v1.0.0, hard-wrapped (subject ≤50, body ≤72).
Prefer several small focused commits over one big one — commit each logical
unit (domain change, then wiring, then tests) so each stands alone. Add a body
only when the change isn't self-evident.

Push after every substantive commit (a dying box then loses minutes, not the
whole run):

```
git push --force-with-lease -u origin ${BRANCH}   # first push
git push --force-with-lease                        # subsequent
```

# REVIEW

Before the PR, spawn a fresh `reviewer` subagent on the branch diff vs
`${BASE_BRANCH}`. Do NOT review inline — an inline review ends your turn at the
halfway gate; delegating returns a result to act on. The `reviewer` is
pre-provisioned via `--agents`; pass only the issue number.

Its final message starts `VERDICT: APPROVE` or `VERDICT: BLOCK`. On BLOCK:

1. Fix on this branch, run checks, recommit.
2. Re-invoke a fresh `reviewer` (not the same instance).
3. Repeat until no blocking findings remain.
4. Re-scout only if the finding shows the change is in the wrong place.

Never open the PR with a blocking finding open. Non-blocking findings may go in
the PR body.

# OPEN A PULL REQUEST

1. `git push --force-with-lease -u origin ${BRANCH}`
2. `gh pr create --base ${BASE_BRANCH} --head ${BRANCH} --title "<conventional title>" --body "<summary>"`
3. Body MUST contain `Closes #${ISSUE_NUMBER}`. Summarize what changed and flag
   anything a reviewer should know.

# WATCH CI

After opening the PR, capture the URL that `gh pr create` prints and block
until CI registers. Right after the PR is created the `statusCheckRollup`
state is absent — treating that as green would merge before CI starts. Wait
for the rollup to return any non-empty state:

```
# gh pr checks uses the check-runs REST endpoint which 403s under fine-grained
# PATs. Use statusCheckRollup (GraphQL) instead — it works with fine-grained
# tokens and aggregates both commit statuses and check-runs faithfully, so a
# missing check reads as not-started rather than silently green.
PR_URL=<the URL gh pr create printed, e.g. https://github.com/owner/repo/pull/42>
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
the rebase-merge, and the complete-label swap. Stop once CI has registered.

# OUTCOME

Once CI has registered, print exactly one line as your final output:

```
SPINDRIFT_OUTCOME issue=${ISSUE_NUMBER} pr=<pr-url> status=ready note=<short reason>
```

This must be the literal final message — nothing after it, no prose summary, no
background task. The launcher parses this one line to learn your PR; if missing,
the PR is never merged and the run is wasted. Grammar is validated by
`cmd/launcher/internal/outcome` (`Parse`, `Line`, `LastInLog`).

`status=ready` = branch pushed, PR open, CI started.
Do NOT run `gh issue edit ... --add-label ${COMPLETE_LABEL}` or `gh pr merge`.

# IF BLOCKED

If you can't finish (review never clears, CI stays red after repeated fixes, or
any other blocker):

1. Push what you have.
2. Open the PR as a draft (`--draft`).
3. Leave the issue in-progress — do NOT close it.
4. Comment on the issue with what's done and what remains:
   `gh issue comment ${ISSUE_NUMBER} --body "<what's done, what remains>"`.
5. Print exactly one line and stop:

```
SPINDRIFT_OUTCOME issue=${ISSUE_NUMBER} pr=<pr-url> status=blocked note=<short reason>
```
