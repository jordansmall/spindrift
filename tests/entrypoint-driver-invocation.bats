#!/usr/bin/env bats
# Driver invocation: stream-json, heartbeat, prefetch hook, nix-store-writable warning.

load helper

setup() {
  setup_entrypoint_env
}

@test "entrypoint invokes claude headlessly with skip-permissions" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "claude invoked for issue #7" "$CLAUDE_LOG"
  grep -q -- "--dangerously-skip-permissions" "$CLAUDE_LOG"
}

@test "entrypoint passes MODEL env var to claude" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q -- "--model claude-opus-4-8" "$CLAUDE_LOG"
}

@test "MODEL env overrides the baked default model at runtime" {
  export MODEL="claude-sonnet-4-6"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q -- "--model claude-sonnet-4-6" "$CLAUDE_LOG"
  ! grep -q -- "--model claude-opus-4-8" "$CLAUDE_LOG"
}

# Observability (#113): text --print emits nothing until the end, so the box
# looks dead under `podman logs -f`. stream-json is the only --print mode that
# emits events in realtime.
@test "entrypoint runs claude in stream-json mode so activity streams live" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q -- "--output-format stream-json" "$CLAUDE_LOG"
  grep -q -- "--verbose" "$CLAUDE_LOG"
}

# Issue #1609: the claude Driver's flagsCommon strips the harness's
# re-invocation-promising tools from the Driver's tool surface -- exercised
# here through DRIVER_PREAMBLE_FILE (the same registry-rendered bytes the
# image bakes, issue #433), not a hand-copied literal.
@test "entrypoint invokes claude with --disallowedTools blocking loop/background affordances" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q -- "--disallowedTools" "$CLAUDE_LOG"
  grep -q -- "ScheduleWakeup" "$CLAUDE_LOG"
  grep -q -- "CronCreate" "$CLAUDE_LOG"
  grep -q -- "CronDelete" "$CLAUDE_LOG"
  grep -q -- "CronList" "$CLAUDE_LOG"
  grep -q -- "RemoteTrigger" "$CLAUDE_LOG"
  grep -q -- "Monitor" "$CLAUDE_LOG"
}

# In-box heartbeat view (#183, absorbed into driver-exec by #626): the
# entrypoint delegates the Driver run to driver-exec, which filters heartbeats
# in-process so a human can `tail -f /tmp/heartbeat.log` inside the box and
# see coarse status lines instead of raw NDJSON. Raw stream-json still reaches
# stdout unchanged for the launcher's byte-exact capture.

@test "entrypoint writes coarse heartbeat log at /tmp/heartbeat.log" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -f /tmp/heartbeat.log ]
}

@test "heartbeat log contains status lines, not raw NDJSON" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  # Heartbeat lines look like "#7 · …", not raw JSON objects.
  grep -q '^#' /tmp/heartbeat.log
  ! grep -q '"type":' /tmp/heartbeat.log
}

# Regression (#123): logs/issue-<n>.log is the sole input to outcome.Classify
# (transient-vs-terminal retry) and outcome.LastInLog. #123 routed the console
# through a lossy formatter that collapsed each event to a summary, stripping the
# raw JSON — including rate_limit_error / resetsAt markers — so retryable
# rate-limit exits were misread as terminal. The raw stream-json must reach
# stdout verbatim; human-readable rendering is a host-side viewer over the log.
@test "entrypoint streams the raw stream-json to stdout for failure classification" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  printf '%s\n' "$output" | grep -q '"type":"result"'
  printf '%s\n' "$output" | grep -q '"type":"assistant"'
}

# The launcher greps '^SPINDRIFT_OUTCOME ' from the container log. Under
# stream-json the outcome is buried in a JSON result event, so the entrypoint
# must surface it as a bare line to keep that contract.
@test "entrypoint re-emits the agent's SPINDRIFT_OUTCOME as a bare line" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  printf '%s\n' "$output" | grep -q '^SPINDRIFT_OUTCOME .*status=ready'
}

# Regression (#1611): the #1582 dogfood run wrapped the outcome line in
# inline backticks inside claude's stream-json result text. The extractor's
# `^SPINDRIFT_OUTCOME ` anchor failed to match, so the launcher never saw a
# real outcome and the entrypoint's own backstop fired a synthetic
# status=blocked over a PR that was actually green.
@test "entrypoint strips a backtick-wrapped SPINDRIFT_OUTCOME line" {
  export FAKE_CLAUDE_WRAP_OUTCOME=backticks
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(printf '%s\n' "$output" | grep -c '^SPINDRIFT_OUTCOME ')" -eq 1 ]
  printf '%s\n' "$output" | grep -q '^SPINDRIFT_OUTCOME issue=7 .*status=ready'
}

@test "entrypoint strips a bold-marker-wrapped SPINDRIFT_OUTCOME line" {
  export FAKE_CLAUDE_WRAP_OUTCOME=bold
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(printf '%s\n' "$output" | grep -c '^SPINDRIFT_OUTCOME ')" -eq 1 ]
  printf '%s\n' "$output" | grep -q '^SPINDRIFT_OUTCOME issue=7 .*status=ready'
}

@test "entrypoint strips a whitespace-padded SPINDRIFT_OUTCOME line" {
  export FAKE_CLAUDE_WRAP_OUTCOME=whitespace
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ "$(printf '%s\n' "$output" | grep -c '^SPINDRIFT_OUTCOME ')" -eq 1 ]
  printf '%s\n' "$output" | grep -q '^SPINDRIFT_OUTCOME issue=7 .*status=ready'
}

@test "entrypoint keeps the last outcome line across multiple result events" {
  export FAKE_CLAUDE_MULTI_RESULT=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  # Two result events each carry an outcome line, but only the last is
  # re-emitted as a bare SPINDRIFT_OUTCOME line -- the raw stale event still
  # appears verbatim in the raw stream-json passed through to stdout, so the
  # assertion below is scoped to bare lines only.
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

# NIX_STORE_WRITABLE: baked into the image Env by mkHarness's nixStoreWritable
# knob (ADR 0018, issue #469) — self-test mode trades hermeticity for in-box
# `nix flake check` feedback, so the warning must be loud when enabled and
# absent by default.
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

# issue #624: DRIVER_BIN/DRIVER_FLAGS_COMMON/DRIVER_SKILLS_DIR are baked by
# the nix-rendered Driver preamble, never hand-copied fallback literals. If
# that preamble never ran, the Box must die with a clear message instead of
# silently impersonating the claude Driver.
@test "entrypoint fails fast naming DRIVER_BIN when the Driver preamble never ran" {
  run env -u DRIVER_BIN -u DRIVER_FLAGS_COMMON -u DRIVER_SKILLS_DIR bash "$ENTRYPOINT_SRC"
  [ "$status" -ne 0 ]
  [[ "$output" == *"DRIVER_BIN"* ]]
}

@test "entrypoint fails fast naming DRIVER_FLAGS_COMMON when only it is unset" {
  run env -u DRIVER_FLAGS_COMMON -u DRIVER_SKILLS_DIR DRIVER_BIN=claude bash "$ENTRYPOINT_SRC"
  [ "$status" -ne 0 ]
  [[ "$output" == *"DRIVER_FLAGS_COMMON"* ]]
}

@test "entrypoint fails fast naming DRIVER_SKILLS_DIR when only it is unset" {
  run env -u DRIVER_SKILLS_DIR DRIVER_BIN=claude DRIVER_FLAGS_COMMON=--verbose bash "$ENTRYPOINT_SRC"
  [ "$status" -ne 0 ]
  [[ "$output" == *"DRIVER_SKILLS_DIR"* ]]
}
