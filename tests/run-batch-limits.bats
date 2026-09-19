#!/usr/bin/env bats

load helper

setup() {
  setup_run_env
}

# The cases below run against a plain temp file, with no launcher or podman
# fakes, so they pin the helper's own polling and timeout contract in isolation
# from the assertions further down that use it (issue #2450).

@test "wait_for_log_lines fails when the count overshoots expected after passing through it" {
  local log="$BATS_TEST_TMPDIR/probe.log"
  printf 'run x\n' >"$log"
  # expected=1 already matches at the first poll, so the helper's first sleep
  # is its confirm loop's. Shadowing sleep to write from there puts the
  # overshoot inside the confirm window by construction rather than by
  # wall-clock luck (issue #3123, same flakiness category as #2649/#2760).
  # shellcheck disable=SC2329 # invoked indirectly, from the helper's confirm loop
  sleep() {
    printf 'run y\n' >>"$log"
    printf 'run z\n' >>"$log"
  }
  run wait_for_log_lines "$log" '^run ' 1 1
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
  # Same confirm-window construction as the sibling test above (issue #3123):
  # expected=0 already matches at the first poll, so shadowing sleep lands this
  # write inside the confirm loop deterministically.
  # shellcheck disable=SC2329 # invoked indirectly, from the helper's confirm loop
  sleep() {
    printf 'run x\n' >>"$log"
  }
  run wait_for_log_lines "$log" '^run ' 0 1
  [ "$status" -ne 0 ]
  [[ "$output" == *"overshot during confirmation"* ]]
}

@test "wait_for_log_lines picks up WAIT_FOR_LOG_LINES_TIMEOUT as the default" {
  local log="$BATS_TEST_TMPDIR/probe.log"
  printf 'unrelated\n' >"$log"
  export WAIT_FOR_LOG_LINES_TIMEOUT=1
  run wait_for_log_lines "$log" '^run ' 1
  [ "$status" -eq 1 ]
  [[ "$output" == *"timed out after 1s"* ]]
}

@test "wait_for_log_lines lets an explicit 4th arg win over WAIT_FOR_LOG_LINES_TIMEOUT" {
  local log="$BATS_TEST_TMPDIR/probe.log"
  printf 'unrelated\n' >"$log"
  export WAIT_FOR_LOG_LINES_TIMEOUT=5
  run wait_for_log_lines "$log" '^run ' 1 1
  [ "$status" -eq 1 ]
  [[ "$output" == *"timed out after 1s"* ]]
}

@test "wait_for_log_lines lets WAIT_FOR_LOG_LINES_TIMEOUT widen past the 2s default" {
  local log="$BATS_TEST_TMPDIR/probe.log"
  local call_count="$BATS_TEST_TMPDIR/count_matches_calls"
  echo 0 >"$call_count"

  local polls_per_second=20 # 1/interval (0.05s) in helper.bash; keep in sync
  local old_calls=$((2 * polls_per_second + 1)) # calls the un-widened 2s default allows
  local new_calls=$((3 * polls_per_second + 1))
  local hit_call=$((old_calls + 5))             # margin above old_calls, still <= new_calls
  # The two assertions guard the invariant this test rests on, so a later edit
  # to the constants above cannot silently break it: hit_call must sit past
  # old_calls, and at or under new_calls.
  [ "$old_calls" -lt "$hit_call" ]
  [ "$hit_call" -le "$new_calls" ]

  # Shadowing sleep as a no-op and staging _count_matches to miss until call
  # hit_call means only a widened timeout's larger poll budget lets the match
  # land, never a real elapsed-time race (issue #2760, same flakiness category
  # as #2649). Loop bounds are call counts, not "tries": the loop runs
  # tries+1 times.
  sleep() { return 0; }
  # The call count goes through a tmpfile because _count_matches runs via
  # command substitution, forking a fresh subshell per call that a
  # plain-variable increment would not survive.
  _count_matches() {
    local n
    n="$(<"$call_count")"
    n=$((n + 1))
    echo "$n" >"$call_count"
    if [ "$n" -ge "$hit_call" ]; then
      echo 1
    else
      echo 0
    fi
  }

  # Set the timeout explicitly: the 2s default is only helper.bash's shell
  # fallback, and nix/checks/bats.nix bakes a wider one for CI (issue #2649).
  export WAIT_FOR_LOG_LINES_TIMEOUT=3
  run wait_for_log_lines "$log" '^run ' 1
  [ "$status" -eq 0 ]

  # Check the old-ceiling half of the claim above rather than trust it: with
  # the same shadowed sleep and _count_matches, an explicit timeout=2 must not
  # reach hit_call.
  echo 0 >"$call_count"
  run wait_for_log_lines "$log" '^run ' 1 2
  [ "$status" -eq 1 ]
  [[ "$output" == *"timed out after 2s"* ]]
}

@test "wait_for_log_lines rejects malformed, unsafe, or zero timeout values" {
  local log="$BATS_TEST_TMPDIR/probe.log"
  local marker="$BATS_TEST_TMPDIR/PWNED_MARKER"
  local -a ids=(
    "non-integer"
    "negative"
    "leading-zero"
    "injection"
    "oversized"
    "zero"
  )
  local -a payloads=(
    "0.5"
    "-1"
    "08"
    "x[\$(touch \"$marker\")]"
    "500000000000000000"
    "0"
  )
  # Every case logs 'unrelated' except "zero", which needs content the pattern
  # already matches, to prove timeout=0 does not silently succeed by finding
  # that match during the confirm window.
  local -a log_contents=(
    "unrelated"
    "unrelated"
    "unrelated"
    "unrelated"
    "unrelated"
    "run x"
  )
  local -a absent_substrings=(
    "syntax error"
    "unbound variable"
    "value too great for base"
    ""
    "unbound variable"
    "timed out after 0s"
  )
  local -a extra_absent_substrings=(
    "integer expression expected"
    ""
    ""
    ""
    ""
    ""
  )
  # The columns are indexed in lockstep by "${!ids[@]}", so a row appended to
  # one column but not another would silently drop or mis-pair a case.
  local -i column_length
  for column_length in "${#payloads[@]}" "${#log_contents[@]}" \
    "${#absent_substrings[@]}" "${#extra_absent_substrings[@]}"; do
    if [ "$column_length" -ne "${#ids[@]}" ]; then
      echo "case table columns disagree: ${#ids[@]} ids vs $column_length entries" >&2
      return 1
    fi
  done
  local -i fail=0
  local i id payload
  for i in "${!ids[@]}"; do
    id="${ids[$i]}"
    payload="${payloads[$i]}"
    printf '%s\n' "${log_contents[$i]}" >"$log"
    if ! assert_timeout_rejected "$log" "$payload" "${absent_substrings[$i]}" "${extra_absent_substrings[$i]}"; then
      echo "$id: assert_timeout_rejected failed for payload [$payload]" >&2
      fail=1
    fi
    # No row's payload may be evaluated. Only "injection" is shaped to create
    # the marker, but checking every row names whichever one did. The marker is
    # removed again so one evaluated payload does not flag every row after it.
    if [ -e "$marker" ]; then
      echo "$id: marker file [$marker] unexpectedly created: payload [$payload] was evaluated" >&2
      rm -f "$marker"
      fail=1
    fi
  done
  [ "$fail" -eq 0 ]
}

@test "wait_for_log_lines rejects WAIT_FOR_LOG_LINES_TIMEOUT=0 the same as an explicit zero" {
  local log="$BATS_TEST_TMPDIR/probe.log"
  printf 'unrelated\n' >"$log"
  export WAIT_FOR_LOG_LINES_TIMEOUT=0
  run wait_for_log_lines "$log" '^run ' 1
  [ "$status" -eq 1 ]
  [[ "$output" == *"timeout must be a positive integer"* ]]
}

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

# MAX_PARALLEL bounds clamp (issue #91).

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

# Issue query cap and oldest-first ordering (issue #96).

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

