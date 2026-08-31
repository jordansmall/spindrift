#!/usr/bin/env bats
# Issue #2975 AC5 / the issue body's own definition of done: "the first
# end-to-end turn test: real entrypoint, real orchestrator, real driver-exec
# with a faked Driver, across an implement pass and a review pass to an
# outcome." Every other tests/*.bats suite drives the real agent/entrypoint.sh
# against tests/fakes/driver-exec and tests/fakes/orchestrator (bash stand-ins
# for the Go binaries); cmd/launcher/orchestrator/handoff_e2e_test.go proves
# the same real-binary chain end to end but in-process from Go, never through
# entrypoint.sh itself. This suite is the missing rung: the real bash
# entrypoint spawns the real orchestrator binary, which spawns the real
# driver-exec binary, which spawns only the Driver ("claude") faked --
# tests/fakes/claude, unmodified, the same fake every other suite already
# trusts.

load helper

setup() {
  setup_entrypoint_env
  : "${DRIVER_EXEC_BIN:?DRIVER_EXEC_BIN must be set (the real driver-exec Go binary)}"
  : "${ORCHESTRATOR_BIN:?ORCHESTRATOR_BIN must be set (the real orchestrator Go binary)}"
  # setup_fakes (called by setup_entrypoint_env above) already put the FAKE
  # bash driver-exec/orchestrator on $FAKE_BIN under their real names, so
  # entrypoint.sh's own PATH-based `driver-exec ...`/`"$_driver_invoker" ...`
  # invocations resolve to them -- overwrite both with the real Go binaries,
  # the same swap-after-setup_fakes pattern tests/dogfood.bats uses on
  # $FAKE_BIN/nix. tests/fakes/claude stays untouched on $FAKE_BIN, so the
  # real driver-exec's own Driver spawn still resolves to the faked Driver.
  cp -f "$DRIVER_EXEC_BIN" "$FAKE_BIN/driver-exec"
  cp -f "$ORCHESTRATOR_BIN" "$FAKE_BIN/orchestrator"
  # The real orchestrator's own --state-file flag defaults to the fixed path
  # /tmp/run-state.json (cmd/launcher/orchestrator/main.go) -- entrypoint.sh
  # never overrides it (run_driver_in_env passes no --state-file flag at
  # all), matching real production. Every other bats suite is invisible to
  # this file (their fake orchestrator never touches run-state), so this is
  # the first suite to actually read/write it -- clear any stale content a
  # prior local run of this same file left behind, or a leftover
  # state.TerminalLand=true would short-circuit this run's own implement
  # pass straight into a no-outcome stop before it ever reaches review.
  rm -f /tmp/run-state.json
}

teardown() {
  rm -f /tmp/run-state.json
}

@test "real entrypoint drives the real orchestrator and real driver-exec through an implement+review pass to an outcome" {
  export ORCHESTRATOR_ENABLED=1
  export BOX_REVIEW_LOOP_ORCHESTRATOR=1
  unset BOX_REVIEW_LOOP_INLINE
  # tests/fakes/claude's default output already carries a status=ready
  # SPINDRIFT_OUTCOME line on every call -- if the implement pass's own call
  # carried one too, the real orchestrator's implementFixTransition would
  # stop the run right there (issue #2036's own HasOutcome-wins rule), never
  # reaching a review pass. FAKE_DRIVER_NO_OUTCOME_FIRST_CALL_ONLY=1
  # suppresses only that first call's outcome, so implementFixTransition's
  # "no cap fired" fallthrough sends the run into the review pass instead.
  export FAKE_DRIVER_NO_OUTCOME_FIRST_CALL_ONLY=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  # Three real driver-exec-spawned Driver invocations, one full
  # implement -> review -> land cycle: tests/fakes/claude never emits a
  # "VERDICT: APPROVE"/"VERDICT: BLOCK" line (no bats fixture in this repo
  # drives that), so the real review pass's own scanReviewLog always reads an
  # empty verdict -- reviewTransition's own VerdictNone case ("no verdict;
  # running terminal land pass") commits the run to exactly one land pass
  # rather than looping BLOCK-triggered fix passes, so the count is pinned at
  # 3 regardless of that fake's fixed shape: call 1 implement (no outcome,
  # forced above), call 2 review (no verdict, but the loop doesn't need one),
  # call 3 land (default status=ready outcome, stops the loop).
  [ "$(grep -c '^driver invoked for issue #7' "$DRIVER_LOG")" -eq 3 ]
  # The real orchestrator's own spindrift_op stream (claude.EncodeSpindriftOp,
  # forwarded to entrypoint's stdout by run_driver_in_env's own passthrough,
  # captured in $output by bats' `run`) proves an implement pass and a review
  # pass both actually ran, not just that the Driver was called three times
  # for some other reason.
  printf '%s\n' "$output" | grep -q '"op":"pass_start".*"role":"implement"'
  printf '%s\n' "$output" | grep -q '"op":"pass_start".*"role":"review"'
  printf '%s\n' "$output" | grep -q '"op":"pass_start".*"role":"land"'
  printf '%s\n' "$output" | grep -q '^SPINDRIFT_OUTCOME .*status=ready'
}
