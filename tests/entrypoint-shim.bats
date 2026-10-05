#!/usr/bin/env bats
# The Box entrypoint is a generated shim (issue #4302, ADR 0058): lib/image.nix
# prepends the Driver/agent-path/default preambles, and the shim's one job is to
# hand off to `box`. Everything box does is covered by Go (cmd/launcher/box), so
# the only thing left to pin here is the handoff itself: the shim execs box,
# the exported environment reaches box, and the preamble's
# non-exported values reach it only as flags.

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
