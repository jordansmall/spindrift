#!/usr/bin/env bats
# ORCHESTRATOR_ENABLED handoff (issue #1996): the boolean knob (default off)
# that swaps run_driver_in_env's invocation target from driver-exec to the
# in-box orchestrator, without changing any flag it's called with.

load helper

setup() {
  setup_entrypoint_env
}

@test "entrypoint takes the direct driver-exec path when ORCHESTRATOR_ENABLED is unset" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ ! -s "$ORCHESTRATOR_LOG" ]
  grep -q "claude invoked for issue #7" "$CLAUDE_LOG"
}

@test "entrypoint hands the pass off to the orchestrator when ORCHESTRATOR_ENABLED is set" {
  export ORCHESTRATOR_ENABLED=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -s "$ORCHESTRATOR_LOG" ]
  grep -q -- "--driver-bin claude" "$ORCHESTRATOR_LOG"
  # Behaviour stays identical to the direct path: the orchestrator's own
  # single pass still reaches the Driver and re-emits its outcome line.
  grep -q "claude invoked for issue #7" "$CLAUDE_LOG"
  printf '%s\n' "$output" | grep -q '^SPINDRIFT_OUTCOME .*status=ready'
}

# Issue #2011: reject-background-bash.sh's PreToolUse deny is Bash-only and
# fires after the fact; CLAUDE_CODE_DISABLE_BACKGROUND_TASKS instead makes
# claude itself omit run_in_background from the Bash/Agent/Task/PowerShell
# tools' own schema, so a Driver can never request async in the first place --
# the fix for a Driver that backgrounds work or parks on a subagent and never
# comes back. It reaches the Driver as an `export` in the same driverPreamble
# text ORCHESTRATOR_ENABLED just swaps the invoker under (agent/entrypoint.sh's
# run_driver_in_env), so both invocation paths must see it identically -- a
# structural guarantee this pair of tests pins directly, rather than trusting
# it by inspection.
@test "direct driver-exec path exports CLAUDE_CODE_DISABLE_BACKGROUND_TASKS to the Driver" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q '^env: CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=1$' "$CLAUDE_LOG"
}

@test "orchestrator path exports CLAUDE_CODE_DISABLE_BACKGROUND_TASKS to the Driver identically" {
  export ORCHESTRATOR_ENABLED=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q '^env: CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=1$' "$CLAUDE_LOG"
}

# The code-owned review pass (issue #2037): entrypoint.sh renders
# review-prompt.md and threads it to the orchestrator's own
# --review-prompt-file, only on this fresh-issue work-dispatch path.
@test "orchestrator path forwards --review-prompt-file carrying a real path" {
  export ORCHESTRATOR_ENABLED=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q -- '--review-prompt-file' "$ORCHESTRATOR_LOG"
  local review_prompt_file
  review_prompt_file="$(grep -oE -- '--review-prompt-file [^ ]+' "$ORCHESTRATOR_LOG" | awk '{print $2}')"
  # run_driver_in_env removes its own temp files once the pass returns, so the
  # path itself no longer exists by the time bats inspects it here -- assert
  # it was a real, non-empty flag value (not an omitted/empty flag), which is
  # what proves entrypoint.sh actually rendered and threaded review-prompt.md
  # through, rather than skipping the flag or passing it empty.
  [ -n "$review_prompt_file" ]
}

# Issue #2065: the corrective resume the SPINDRIFT_OUTCOME required-marker gate
# fires (issue #2044) deliberately omits run_driver_in_env's 4th arg
# (review_prompt), so under the orchestrator that nudge-and-retry stays a
# narrow single pass rather than re-entering the full implement/review/fix loop
# a second time from whatever cap or park stopped the first attempt. The design
# decision recorded there is "keep the single-pass fallback": each
# run_driver_in_env call spawns a fresh orchestrator whose
# --max-review-rounds/--max-slices budgets reset to their defaults, so
# re-attaching --review-prompt-file would hand the last-resort nudge a
# brand-new full review budget and re-trigger the exact
# bounded-but-large loop the original run just exhausted. This test locks that
# downgrade: the first pass forwards --review-prompt-file (asserted above), the
# resume must not.
@test "orchestrator path omits --review-prompt-file on the corrective resume" {
  export ORCHESTRATOR_ENABLED=1
  # First pass forgets its outcome; the SPINDRIFT_OUTCOME gate resumes once,
  # so the orchestrator is invoked exactly twice -- one line per invocation in
  # ORCHESTRATOR_LOG (the fake echoes its argv there, issue #1996).
  export FAKE_CLAUDE_NO_OUTCOME_FIRST_CALL_ONLY=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(grep -c . "$ORCHESTRATOR_LOG")" -eq 2 ]
  # The initial pass carries the review-prompt flag ...
  head -1 "$ORCHESTRATOR_LOG" | grep -q -- '--review-prompt-file'
  # ... and the corrective resume (the second, last line) does not.
  ! tail -1 "$ORCHESTRATOR_LOG" | grep -q -- '--review-prompt-file'
}
