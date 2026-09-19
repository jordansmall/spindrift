#!/usr/bin/env bats
# devShell lifecycle wrapping (issue #341): prefetch and the Driver run inside
# `nix develop`, so the agent gets the Target's pinned toolchain, not the baked one.

load helper

setup() {
  setup_entrypoint_env
}

@test "devShell-present Driver: claude is launched inside nix develop when devShell is found" {
  seed_flake_repo
  export FAKE_NIX_DEV_SHELL_OK=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  # driver-exec's own `--command <driver-bin>` must show up beyond the probe's
  # `--command true` (issue #626: the entrypoint no longer renders a bash
  # wrapper for the Driver run).
  grep -v -- '--command true$' "$NIX_LOG" | grep -q -- '--command'
  grep -q "driver invoked for issue #7" "$DRIVER_LOG"
}

@test "DEV_SHELL_NAME default: nix develop targets .#default when name is default" {
  seed_flake_repo
  export FAKE_NIX_DEV_SHELL_OK=1
  # setup() sets DEV_SHELL_NAME=default, so probe and wrappers target .#default.
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q 'develop .#default' "$NIX_LOG"
}

@test "DEV_SHELL_NAME selector: nix develop uses the configured devShell name" {
  seed_flake_repo
  export FAKE_NIX_DEV_SHELL_OK=1
  export DEV_SHELL_NAME=ci
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q 'develop .#ci' "$NIX_LOG"
}

# Relaunching once in the baked env after a launch failure now lives in
# driver-exec (issue #626), covered by the Go test
# TestRunRelaunchesInBakedEnvOnEmptyStreamLaunchFailure. A bats double here
# would only reimplement driver-exec's branching.

@test "devShell-present prefetch: prefetch runs inside nix develop when devShell is found" {
  seed_flake_repo
  export FAKE_NIX_DEV_SHELL_OK=1
  export PREFETCH_LOG="$BATS_TEST_TMPDIR/prefetch.log"
  {
    printf '#!%s\n' "$(command -v bash)"
    cat <<'FAKE'
echo "warmed $PWD for #${ISSUE_NUMBER:-?}" >>"$PREFETCH_LOG"
FAKE
  } >"$FAKE_BIN/warm-cache"
  chmod +x "$FAKE_BIN/warm-cache"
  # setup() already exports a PREFETCH; override it with the fake above.
  export PREFETCH="warm-cache"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "warmed" "$PREFETCH_LOG"
  # Prefetch still renders its own bash wrapper; issue #626 changed only the
  # Driver run's invocation.
  [ "$(grep -c 'develop.*--command bash' "$NIX_LOG")" -eq 1 ]
}

@test "devShell-present Driver: MODEL is forwarded into nix develop wrapper" {
  seed_flake_repo
  export FAKE_NIX_DEV_SHELL_OK=1
  export MODEL=claude-test-model
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  # The fake claude logs "model=<value>" when MODEL reaches the wrapper.
  grep -q 'model=claude-test-model' "$DRIVER_LOG"
}

