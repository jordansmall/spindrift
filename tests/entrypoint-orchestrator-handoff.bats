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
