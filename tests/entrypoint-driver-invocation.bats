#!/usr/bin/env bats

load helper

setup() {
  setup_entrypoint_env
}

@test "entrypoint invokes claude headlessly with skip-permissions" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "driver invoked for issue #7" "$DRIVER_LOG"
  grep -q -- "--dangerously-skip-permissions" "$DRIVER_LOG"
}

@test "entrypoint passes MODEL env var to claude" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q -- "--model claude-test-model" "$DRIVER_LOG"
}

@test "MODEL env overrides the baked default model at runtime" {
  export MODEL="claude-sonnet-4-6"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q -- "--model claude-sonnet-4-6" "$DRIVER_LOG"
  ! grep -q -- "--model claude-test-model" "$DRIVER_LOG"
}

# Issue #113: text --print emits nothing until the end, so the box looks dead
# under `podman logs -f`. stream-json is the only --print mode that emits
# events in realtime.
@test "entrypoint runs claude in stream-json mode so activity streams live" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q -- "--output-format stream-json" "$DRIVER_LOG"
  grep -q -- "--verbose" "$DRIVER_LOG"
}

# Issue #1609: the claude Driver's flagsCommon strips the harness's
# re-invocation-promising tools. This test reads them from
# DRIVER_PREAMBLE_FILE, the same registry-rendered bytes the image bakes
# (issue #433), not a hand-copied literal.
@test "entrypoint invokes claude with --disallowedTools blocking loop/background affordances" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q -- "--disallowedTools" "$DRIVER_LOG"
  grep -q -- "ScheduleWakeup" "$DRIVER_LOG"
  grep -q -- "CronCreate" "$DRIVER_LOG"
  grep -q -- "CronDelete" "$DRIVER_LOG"
  grep -q -- "CronList" "$DRIVER_LOG"
  grep -q -- "RemoteTrigger" "$DRIVER_LOG"
  grep -q -- "Monitor" "$DRIVER_LOG"
}

# The entrypoint delegates the Driver run to driver-exec, which filters
# heartbeats in-process (#183, absorbed into driver-exec by #626) so a human
# can `tail -f /tmp/heartbeat.log` inside the box. Raw stream-json still
# reaches stdout unchanged for the launcher's byte-exact capture.

@test "entrypoint writes coarse heartbeat log at /tmp/heartbeat.log" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -f /tmp/heartbeat.log ]
}

@test "heartbeat log contains status lines, not raw NDJSON" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  # Heartbeat lines start with the issue number, so a leading "#" tells them
  # apart from raw JSON objects.
  grep -q '^#' /tmp/heartbeat.log
  ! grep -q '"type":' /tmp/heartbeat.log
}

# Regression (#123): logs/issue-<n>.log is the sole input to outcome.Classify
# and outcome.LastInLog. A lossy formatter collapsed each event to a summary
# and stripped the raw JSON, including the rate_limit_error and resetsAt
# markers, so retryable rate-limit exits were misread as terminal. The raw
# stream-json must reach stdout verbatim.
@test "entrypoint streams the raw stream-json to stdout for failure classification" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  printf '%s\n' "$output" | grep -q '"type":"result"'
  printf '%s\n' "$output" | grep -q '"type":"assistant"'
}

# The launcher greps '^SPINDRIFT_OUTCOME ' from the container log. Under
# stream-json the outcome sits inside a JSON result event, so the entrypoint
# must re-emit it as a bare line to keep that contract.
@test "entrypoint re-emits the agent's SPINDRIFT_OUTCOME as a bare line" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  printf '%s\n' "$output" | grep -q '^SPINDRIFT_OUTCOME .*status=ready'
}

# Regression (#1611): the #1582 dogfood run wrapped the outcome line in inline
# backticks inside claude's result text, so the extractor's
# `^SPINDRIFT_OUTCOME ` anchor missed it and the backstop fired a synthetic
# status=blocked over a green PR.
@test "entrypoint strips a backtick-wrapped SPINDRIFT_OUTCOME line" {
  export FAKE_DRIVER_WRAP_OUTCOME=backticks
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(printf '%s\n' "$output" | grep -c '^SPINDRIFT_OUTCOME ')" -eq 1 ]
  printf '%s\n' "$output" | grep -q '^SPINDRIFT_OUTCOME issue=7 .*status=ready'
}

@test "entrypoint strips a bold-marker-wrapped SPINDRIFT_OUTCOME line" {
  export FAKE_DRIVER_WRAP_OUTCOME=bold
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(printf '%s\n' "$output" | grep -c '^SPINDRIFT_OUTCOME ')" -eq 1 ]
  printf '%s\n' "$output" | grep -q '^SPINDRIFT_OUTCOME issue=7 .*status=ready'
}

@test "entrypoint strips a whitespace-padded SPINDRIFT_OUTCOME line" {
  export FAKE_DRIVER_WRAP_OUTCOME=whitespace
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(printf '%s\n' "$output" | grep -c '^SPINDRIFT_OUTCOME ')" -eq 1 ]
  printf '%s\n' "$output" | grep -q '^SPINDRIFT_OUTCOME issue=7 .*status=ready'
}

@test "entrypoint keeps the last outcome line across multiple result events" {
  export FAKE_DRIVER_MULTI_RESULT=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  # The stale event still appears verbatim in the passed-through stream-json,
  # so these assertions count bare lines only.
  [ "$(printf '%s\n' "$output" | grep -c '^SPINDRIFT_OUTCOME ')" -eq 1 ]
  printf '%s\n' "$output" | grep '^SPINDRIFT_OUTCOME ' | grep -q 'status=ready note=fake$'
}

@test "entrypoint runs the configured prefetch hook inside the work tree" {
  export PREFETCH_LOG="$BATS_TEST_TMPDIR/prefetch.log"
  {
    printf '#!%s\n' "$(command -v bash)"
    cat <<'FAKE'
echo "warmed $PWD for #${ISSUE_NUMBER:-?}" >>"$PREFETCH_LOG"
FAKE
  } >"$FAKE_BIN/warm-cache"
  chmod +x "$FAKE_BIN/warm-cache"
  export PREFETCH="warm-cache"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "warmed" "$PREFETCH_LOG"
  grep -q "$WORK_DIR" "$PREFETCH_LOG"
}

# mkHarness bakes NIX_STORE_WRITABLE into the image Env from its
# nixStoreWritable knob (ADR 0018, issue #469). Self-test mode trades
# hermeticity for in-box `nix flake check` feedback, so the warning must be
# loud when enabled and absent by default.
@test "entrypoint prints a WARNING when NIX_STORE_WRITABLE=true" {
  export NIX_STORE_WRITABLE=true
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [[ "$output" == *"==> WARNING"*"/nix/store is writable"* ]]
}

@test "entrypoint prints no store-writable warning by default" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [[ "$output" != *"/nix/store is writable"* ]]
}
