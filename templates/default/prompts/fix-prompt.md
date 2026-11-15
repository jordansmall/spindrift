# TASK

Fix box for issue #${ISSUE_NUMBER}: ${ISSUE_TITLE}

Branch `${BRANCH}` is already checked out with prior work from an earlier run
that opened a pull request against `${BASE_BRANCH}` — CI came back red on it.
This is a warm fix pass, not a fresh implementation: do not re-scout, do not
re-derive the issue from scratch.

${CI_FAILURE_STEP}# CONTEXT

The issue's body and its last-10-comment snapshot are already in the
# ISSUE TEXT section after the template body — read there, do not fetch
it from the tracker.

Read first (run these yourself):

- `git log -n 10 --oneline` — the prior run's commits already on this branch.
${FIX_CI_READ_STEP}# FIX

${SKILL_PREAMBLE}A comment earns its place only by carrying something the code cannot state
itself: the non-obvious why, a constraint, or a gotcha. Never write a
comment that just restates what the code already says. Keep comment volume
proportional to the size of the change — a one-line fix does not earn a
paragraph of commentary.

${PRINCIPLE_FIX_ROOT_CAUSES_STEP}No SCOUT, no implement-from-scratch. Go straight to:

1. Reproduce the CI failure locally (see CHECK below). If it will not
   reproduce in the Box:
   - Compare the platform legs CI FAILURE lists against each other: a
     check that fails the same way on every leg is deterministic, not a
     flake — never write it off as timing or as one platform.
   - Compare the error text in CI's log with your in-Box run; the line
     that differs points at the cause.
   - Suspect the Box's known divergences from CI first, before timing or
     resource theories: the Box builds with the Nix sandbox off, so a
     build can see store paths it never declared, and it runs under the
     Box's own nix config (experimental features CI does not enable, for
     one).
   - Never stress-run tests in parallel copies; that can exhaust the
     Box's pids or memory limits and kill the pass.
2. Make the smallest change that fixes it. Do not refactor, redesign, or
   touch anything the failure doesn't implicate.
3. Re-run the checks that were failing to confirm the fix.

Everything from COMMS onward below is the shared contract every box runs on
(issue #455) — this warm fix pass overrides two things in it:

- One focused commit for the fix, not several. Unless the fix is a
  reasonably different change, fold it into the prior-run commit it
  logically belongs to (`git commit --amend` or an autosquash fixup)
  rather than adding a follow-up commit; a new commit only when the fix is
  truly a separate file or scope. Rewriting the branch's own unmerged
  history is expected.
- The PR for this issue is already open (see TASK above) and there is no
  REVIEW step on a fix pass. Where the shared flow below reaches REVIEW or
  OPEN A PULL REQUEST, skip them — do not run `gh pr create`. Go straight
  from COMMIT to LAND THE CHANGE's `$CODE_FORGE` branch, then OUTCOME.
