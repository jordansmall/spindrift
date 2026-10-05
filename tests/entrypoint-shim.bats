#!/usr/bin/env bats
# The Box entrypoint is a generated shim (issue #4302, ADR 0058): lib/image.nix
# prepends the Driver/agent-path/default preambles, and the shim's one job is to
# hand off to `box`. Everything box does is covered by Go (cmd/launcher/box), so
# the only thing left to pin here is the handoff itself: the shim execs box,
# the exported environment reaches box, and the preamble's
# non-exported values reach it only as flags, carrying exactly the values the
# preamble resolved.

load helper

setup() {
  setup_entrypoint_env
  # Replace the real box setup_entrypoint_env copied in with a recorder that
  # exits with a code nothing else in the shim could produce.
  rm -f "$FAKE_BIN/box"
  {
    # No /usr/bin/env in the nix build sandbox, so name the interpreter directly.
    printf '#!%s\n' "$BASH"
    cat <<'FAKE'
printf '%s\n' "$@" >"$BOX_ARGV_LOG"
env >"$BOX_ENV_LOG"
exit 42
FAKE
  } >"$FAKE_BIN/box"
  chmod +x "$FAKE_BIN/box"
  export BOX_ARGV_LOG="$BATS_TEST_TMPDIR/box.argv"
  export BOX_ENV_LOG="$BATS_TEST_TMPDIR/box.env"
}

@test "the shim execs box and propagates its exit status" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 42 ]
  [ -s "$BOX_ARGV_LOG" ]
}

@test "the environment the shim was started with reaches box" {
  # The generated exports (AGENTS_JSON_TEMPLATE, the Driver mount dirs) are
  # prepended in the image but not in this suite's wrapper, so a sentinel
  # stands in for them.
  export SHIM_SENTINEL="exported-before-exec"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 42 ]
  grep -qx 'SHIM_SENTINEL=exported-before-exec' "$BOX_ENV_LOG"
}

# Exporting the preamble's other values would change the Driver's environment,
# so box gets them as flag arguments only.
@test "a non-exported preamble value reaches box as a flag, not in its environment" {
  # Cleared so the value below can only come from the preamble's own default.
  unset DRIVER_ARGV_ORDER
  run bash "$ENTRYPOINT"
  [ "$status" -eq 42 ]
  local value
  value="$(grep -xA1 -- '--argv-order' "$BOX_ARGV_LOG" | tail -n 1)"
  [ -n "$value" ]
  [ "$value" != "--argv-order" ]
  ! grep -q '^DRIVER_ARGV_ORDER=' "$BOX_ENV_LOG"
}

# Must match the orchestrator's --state-file default, else the backstop
# silently loses the reviewer's BLOCK verdict.
@test "the shim hands box the orchestrator's run-state path" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 42 ]
  local value
  value="$(grep -xA1 -- '--run-state-file' "$BOX_ARGV_LOG" | tail -n 1)"
  [ "$value" = "/tmp/run-state.json" ]
}

# box defaults both flags to empty, so a shim line that drops or misspells one
# fails silently: the Driver just never gets its Bash-timeout env vars.
@test "the shim hands box the Driver Bash-timeout flags" {
  # The Driver preamble assigns DRIVER_BASH_TIMEOUT_ENV unconditionally, which
  # would clobber an exported value, so assign both just ahead of the shim body,
  # after the preamble has run.
  local wrapped="$BATS_TEST_TMPDIR/entrypoint-timeout.sh"
  awk -v ms='DRIVER_BASH_TIMEOUT_MS=1800000' \
    -v env="DRIVER_BASH_TIMEOUT_ENV='SHIM_A SHIM_B'" \
    '/^exec box/ { print ms; print env } 1' "$ENTRYPOINT" >"$wrapped"
  # Fails here, not at the flag checks, if the shim's exec line changed shape.
  grep -qx 'DRIVER_BASH_TIMEOUT_MS=1800000' "$wrapped"
  run bash "$wrapped"
  [ "$status" -eq 42 ]
  local value
  value="$(grep -xA1 -- '--driver-bash-timeout-ms' "$BOX_ARGV_LOG" | tail -n 1)"
  [ "$value" = "1800000" ]
  value="$(grep -xA1 -- '--driver-bash-timeout-env' "$BOX_ARGV_LOG" | tail -n 1)"
  [ "$value" = "SHIM_A SHIM_B" ]
}
