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

# FIX

${SKILL_PREAMBLE}No SCOUT, no implement-from-scratch. Go straight to:

1. Reproduce the CI failure locally (see CHECK below).
2. Make the smallest change that fixes it. Do not refactor, redesign, or
   touch anything the failure doesn't implicate.
3. Re-run the checks that were failing to confirm the fix.

Everything from COMMS onward below is the shared contract every box runs on
(issue #455) — this warm fix pass overrides two things in it:

- One focused commit for the fix, not several — COMMIT's "several small
  commits" guidance below describes a fresh implementation's multiple
  logical units, which a warm fix rarely has.
- The PR for this issue is already open (see TASK above) and there is no
  REVIEW step on a fix pass. Where the shared flow below reaches REVIEW,
  OPEN A PULL REQUEST, or IF BLOCKED's "open the PR as a draft" step, skip
  them — do not run `gh pr create`, and if blocked, leave the existing PR
  as-is. Go straight from COMMIT to LAND THE CHANGE's `$CODE_FORGE` branch,
  then WATCH CI.
