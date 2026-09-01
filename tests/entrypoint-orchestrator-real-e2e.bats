#!/usr/bin/env bats
# The first end-to-end turn test: real entrypoint, real orchestrator, real
# driver-exec, with only the Driver faked, across an implement pass and a review
# pass to an outcome (issue #2975 AC5). Every other tests/*.bats suite drives
# agent/entrypoint.sh against the bash stand-ins for the Go binaries, and
# cmd/launcher/orchestrator/handoff_e2e_test.go proves the real-binary chain
# in-process from Go but never through entrypoint.sh. This suite is the missing
# rung between them.

load helper

setup() {
  setup_entrypoint_env
  : "${DRIVER_EXEC_BIN:?DRIVER_EXEC_BIN must be set (the real driver-exec Go binary)}"
  : "${ORCHESTRATOR_BIN:?ORCHESTRATOR_BIN must be set (the real orchestrator Go binary)}"
  # setup_fakes already put the FAKE bash driver-exec/orchestrator on $FAKE_BIN
  # under their real names -- overwrite both with the real Go binaries, the same
  # swap-after-setup_fakes pattern tests/dogfood.bats uses on $FAKE_BIN/nix.
  # tests/fakes/claude stays untouched, so the real driver-exec's own Driver
  # spawn still resolves to the faked Driver.
  cp -f "$DRIVER_EXEC_BIN" "$FAKE_BIN/driver-exec"
  cp -f "$ORCHESTRATOR_BIN" "$FAKE_BIN/orchestrator"
  # The real orchestrator's --state-file defaults to the fixed /tmp/run-state.json
  # and entrypoint.sh never overrides it, matching production. This is the first
  # suite to actually read/write it, so clear any stale content a prior local run
  # left behind: a leftover state.TerminalLand=true would short-circuit this run's
  # implement pass into a no-outcome stop before it ever reaches review.
  rm -f /tmp/run-state.json
}

teardown() {
  rm -f /tmp/run-state.json
}

@test "real entrypoint drives the real orchestrator and real driver-exec through an implement+review pass to an outcome" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  # tests/fakes/claude emits a status=ready SPINDRIFT_OUTCOME line on every call
  # by default -- if the implement pass carried one, implementFixTransition would
  # stop the run right there (HasOutcome wins), never reaching a review pass.
  # FAKE_DRIVER_NO_OUTCOME_FIRST_CALL_ONLY=1 suppresses only that first call's
  # outcome, so the "no cap fired" fallthrough sends the run into review.
  export FAKE_DRIVER_NO_OUTCOME_FIRST_CALL_ONLY=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  # Three Driver invocations, one full implement -> review -> land cycle:
  # tests/fakes/claude never emits a VERDICT line, so the review pass reads an
  # empty verdict and reviewTransition's VerdictNone case commits the run to
  # exactly one land pass rather than looping fix passes. Call 1 implement (no
  # outcome, forced above), call 2 review, call 3 land (default status=ready,
  # stopping the loop).
  [ "$(grep -c '^driver invoked for issue #7' "$DRIVER_LOG")" -eq 3 ]
  # The real orchestrator's spindrift_op stream, forwarded to entrypoint's stdout
  # and captured in $output, proves an implement pass and a review pass both
  # actually ran -- not just that the Driver was called three times.
  printf '%s\n' "$output" | grep -q '"op":"pass_start".*"role":"implement"'
  printf '%s\n' "$output" | grep -q '"op":"pass_start".*"role":"review"'
  printf '%s\n' "$output" | grep -q '"op":"pass_start".*"role":"land"'
  printf '%s\n' "$output" | grep -q '^SPINDRIFT_OUTCOME .*status=ready'
}
