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

  local polls_per_second=20 # tests/helper.bash's wait_for_log_lines "tries" calc -- 1/interval (0.05s); keep in sync
  local old_calls=$((2 * polls_per_second + 1)) # calls made under the un-widened 2s default
  local new_calls=$((3 * polls_per_second + 1)) # calls made under the widened 3s timeout
  local hit_call=$((old_calls + 5))             # margin above old_calls, still <= new_calls
  # Sanity-check the invariant the assertions below rely on, so a future
  # edit to the constants above can't silently break it: hit_call must sit
  # strictly past old_calls (so the un-widened default can't reach it) and
  # at/under new_calls (so the widened timeout can).
  [ "$old_calls" -lt "$hit_call" ]
  [ "$hit_call" -le "$new_calls" ]

  # Poll-count assertion (issue #2760, same flakiness category as #2649):
  # shadow `sleep` as a no-op and stage `_count_matches` to report a miss
  # until call hit_call, then a hit -- so only a widened timeout's larger
  # poll budget lets the match land, never a real elapsed-time race. Loop
  # bounds are call counts, not "tries" (the loop runs tries+1 times):
  # old_calls = 2*20+1 = 41 (< hit_call, so the un-widened default times
  # out first) and new_calls = 3*20+1 = 61 (>= hit_call, so the widened
  # timeout reaches it comfortably). The "2s default" below is only
  # helper.bash's shell-level fallback (see that file's doc comment above
  # wait_for_log_lines for the full rationale, including why CI's bats
  # derivation bakes a different default) -- this test explicitly exports 3
  # rather than relying on either default.
  # The call count is threaded through a tmpfile because `_count_matches`
  # runs via command substitution, forking a fresh subshell per call that
  # a plain-variable increment wouldn't survive.
  sleep() { return 0; }
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

  export WAIT_FOR_LOG_LINES_TIMEOUT=3
  run wait_for_log_lines "$log" '^run ' 1
  [ "$status" -eq 0 ]

  # Self-verify the OLD-ceiling half of the claim above, not just leave it
  # as a comment: with the same shadowed sleep/_count_matches, an explicit
  # timeout=2 (the un-widened default) must NOT reach hit_call and must
  # time out.
  echo 0 >"$call_count"
  run wait_for_log_lines "$log" '^run ' 1 2
  [ "$status" -eq 1 ]
  [[ "$output" == *"timed out after 2s"* ]]
}

@test "wait_for_log_lines rejects malformed, unsafe, or zero timeout values" {
  local log="$BATS_TEST_TMPDIR/probe.log"
  # Per-case data used only by the "injection" row's payload substitution
  # below -- the other 5 rows' payloads don't contain the MARKER
  # placeholder, so this is a no-op for them.
  local marker="$BATS_TEST_TMPDIR/PWNED_MARKER"
  # Every case shares 'unrelated' log content except "zero": that one needs
  # a pattern that already matches (not 'unrelated' like the siblings) to
  # prove timeout=0 doesn't silently succeed by finding the match already
  # present during the confirm window.
  # Payload uses a MARKER placeholder rather than interpolating $marker
  # directly: $marker derives from $BATS_TEST_TMPDIR, and a real path
  # containing '|' would otherwise mis-split this pipe-delimited row.
  local -a cases=(
    "non-integer|0.5|unrelated|syntax error"
    "negative|-1|unrelated|unbound variable"
    "leading-zero|08|unrelated|value too great for base"
    "injection|x[\$(touch \"MARKER\")]|unrelated|"
    "oversized|500000000000000000|unrelated|unbound variable"
    "zero|0|run x|timed out after 0s"
  )
  local tc id payload log_content absent_substring
  local -i fail=0
  for tc in "${cases[@]}"; do
    IFS='|' read -r id payload log_content absent_substring <<<"$tc"
    payload="${payload/MARKER/$marker}"
    printf '%s\n' "$log_content" >"$log"
    if ! assert_timeout_rejected "$log" "$payload" "$absent_substring"; then
      echo "$id: assert_timeout_rejected failed for payload [$payload]" >&2
      fail=1
    fi
    case "$id" in
    non-integer)
      if [[ "$output" == *"integer expression expected"* ]]; then
        echo "$id: output [$output] unexpectedly contains [integer expression expected]" >&2
        fail=1
      fi
      ;;
    injection)
      if [ -e "$marker" ]; then
        echo "$id: marker file [$marker] unexpectedly created" >&2
        fail=1
      fi
      ;;
    esac
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

