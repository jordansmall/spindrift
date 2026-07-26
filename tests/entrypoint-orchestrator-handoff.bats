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
