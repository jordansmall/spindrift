#!/usr/bin/env bats
# Issue #2975 AC5: the real bash entrypoint spawns the real orchestrator, which
# spawns the real driver-exec, with only the Driver faked (tests/fakes/claude).
# Other tests/*.bats suites fake driver-exec and the orchestrator too, and
# handoff_e2e_test.go drives the real binaries from Go, not through entrypoint.sh.

load helper

setup() {
  setup_entrypoint_env
  : "${DRIVER_EXEC_BIN:?DRIVER_EXEC_BIN must be set (the real driver-exec Go binary)}"
  : "${ORCHESTRATOR_BIN:?ORCHESTRATOR_BIN must be set (the real orchestrator Go binary)}"
  # setup_entrypoint_env's setup_fakes put the fake bash driver-exec and
  # orchestrator on $FAKE_BIN under their real names, so overwrite both with the
  # real Go binaries. tests/fakes/claude stays, so the real driver-exec's own
  # Driver spawn still resolves to the fake.
  cp -f "$DRIVER_EXEC_BIN" "$FAKE_BIN/driver-exec"
  cp -f "$ORCHESTRATOR_BIN" "$FAKE_BIN/orchestrator"
  # entrypoint.sh passes no --state-file, so the real orchestrator uses its
  # default /tmp/run-state.json. A leftover state.TerminalLand=true from a prior
  # local run of this file would short-circuit the implement pass into a
  # no-outcome stop before it reaches review.
  rm -f /tmp/run-state.json
}

teardown() {
  rm -f /tmp/run-state.json
}

@test "real entrypoint drives the real orchestrator and real driver-exec through an implement+review pass to an outcome" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  # tests/fakes/claude emits a status=ready SPINDRIFT_OUTCOME on every call. If
  # the implement pass carried one, implementFixTransition would stop the run
  # right there (issue #2036's HasOutcome-wins rule) and never reach review, so
  # suppress it for that first call only.
  export FAKE_DRIVER_NO_OUTCOME_FIRST_CALL_ONLY=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  # The three calls are one cycle of implement, review, and land.
  # tests/fakes/claude never emits a VERDICT line, so scanReviewLog reads an
  # empty verdict and reviewTransition's VerdictNone case commits to exactly one
  # land pass rather than looping BLOCK-triggered fix passes, pinning the count.
  [ "$(grep -c '^driver invoked for issue #7' "$DRIVER_LOG")" -eq 3 ]
  # The orchestrator's spindrift_op stream reaches $output through
  # run_driver_in_env's stdout passthrough, so these prove each pass really ran
  # rather than the Driver being called three times for some other reason.
  printf '%s\n' "$output" | grep -q '"op":"pass_start".*"role":"implement"'
  printf '%s\n' "$output" | grep -q '"op":"pass_start".*"role":"review"'
  printf '%s\n' "$output" | grep -q '"op":"pass_start".*"role":"land"'
  printf '%s\n' "$output" | grep -q '^SPINDRIFT_OUTCOME .*status=ready'
}
