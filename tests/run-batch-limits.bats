#!/usr/bin/env bats
# Batch caps and issue-query ordering: MAX_JOBS, MAX_PARALLEL bounds clamp, oldest-first query cap.

load helper

setup() {
  setup_run_env
}

# --- wait_for_log_lines helper (issue #2450) --------------------------------
# Exercised directly against a plain temp file, no launcher/podman fakes
# involved, so these cases pin the helper's own polling/timeout contract in
# isolation from the assertions below that use it.

@test "wait_for_log_lines fails when the count overshoots expected after passing through it" {
  local log="$BATS_TEST_TMPDIR/probe.log"
  : >"$log"
  (
    printf 'run x\n' >>"$log"
    # Deliberately pinned to the helper's confirm window (confirm_tries=3 x
    # interval=0.05s = 150ms): this write must land inside that window for
    # the assertion below to exercise the confirm-loop overshoot path
    # rather than the (already covered) settle-then-pass path, so it can't
    # be widened for extra margin without changing what the test proves.
    sleep 0.15
    printf 'run y\n' >>"$log"
    printf 'run z\n' >>"$log"
  ) &
  local writer=$!
  run wait_for_log_lines "$log" '^run ' 1 1
  wait "$writer"
  [ "$status" -ne 0 ]
  [[ "$output" == *"overshot during confirmation"* ]]
}

@test "wait_for_log_lines fails when the count already overshoots expected before the first poll" {
  local log="$BATS_TEST_TMPDIR/probe.log"
  printf 'run x\nrun y\n' >"$log"
  run wait_for_log_lines "$log" '^run ' 1 1
  [ "$status" -ne 0 ]
  [[ "$output" == *"overshot -- "* ]]
  [[ "$output" != *"overshot during confirmation"* ]]
}

@test "wait_for_log_lines succeeds once the expected count lands" {
  local log="$BATS_TEST_TMPDIR/probe.log"
  : >"$log"
  (
    sleep 0.1
    printf 'run x\n' >>"$log"
    printf 'run y\n' >>"$log"
  ) &
  local writer=$!
  run wait_for_log_lines "$log" '^run ' 2
  wait "$writer"
  [ "$status" -eq 0 ]
}

@test "wait_for_log_lines fails within its bounded timeout when the count never lands" {
  local log="$BATS_TEST_TMPDIR/probe.log"
  printf 'run x\n' >"$log"
  run wait_for_log_lines "$log" '^run ' 2 1
  [ "$status" -ne 0 ]
}

@test "wait_for_log_lines fails cleanly when the file does not exist" {
  local log="$BATS_TEST_TMPDIR/does-not-exist.log"
  run wait_for_log_lines "$log" '^run ' 1 1
  [ "$status" -ne 0 ]
  [[ "$output" != *"integer expression expected"* ]]
}

@test "wait_for_log_lines succeeds immediately when expected is 0 and nothing ever matches" {
  local log="$BATS_TEST_TMPDIR/probe.log"
  printf 'unrelated\n' >"$log"
  run wait_for_log_lines "$log" '^run ' 0 1
  [ "$status" -eq 0 ]
}

@test "wait_for_log_lines fails when expected is 0 but a matching line appears during the wait" {
  local log="$BATS_TEST_TMPDIR/probe.log"
  : >"$log"
  (
    sleep 0.07
    printf 'run x\n' >>"$log"
  ) &
  local writer=$!
  run wait_for_log_lines "$log" '^run ' 0 1
  wait "$writer"
  [ "$status" -ne 0 ]
}

# --- MAX_JOBS batch cap (dogfood serial loop) ------------------------------

@test "MAX_JOBS=1 dispatches only the oldest ready issue" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export MAX_JOBS=1
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  wait_for_log_lines "$PODMAN_LOG" '^run ' 1
  wait_for_log_lines "$PODMAN_LOG" 'ISSUE_NUMBER=1' 1
  wait_for_log_lines "$PODMAN_LOG" 'ISSUE_NUMBER=2' 0
}

@test "MAX_JOBS=0 dispatches the whole batch (no limit)" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export MAX_JOBS=0
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  wait_for_log_lines "$PODMAN_LOG" '^run ' 2
}

# --- MAX_PARALLEL bounds clamp (issue #91) ------------------------------------

@test "MAX_PARALLEL=0 falls back to default and dispatches the whole batch" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export MAX_PARALLEL=0
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  wait_for_log_lines "$PODMAN_LOG" '^run ' 2
}

@test "MAX_PARALLEL=garbage falls back to default and dispatches the whole batch" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export MAX_PARALLEL=garbage
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  wait_for_log_lines "$PODMAN_LOG" '^run ' 2
}

# --- Issue query cap and oldest-first ordering (issue #96) -------------------

@test "full window of 100 issues emits a cap warning" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export MAX_JOBS=1
  ISSUES=""
  for i in $(seq 1 100); do
    ISSUES+="${i}"$'\t'"Issue ${i}"$'\n'
  done
  export FAKE_GH_ISSUES="$ISSUES"
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  [[ "$output" == *"WARNING"* ]]
  [[ "$output" == *"100"* ]]
}

