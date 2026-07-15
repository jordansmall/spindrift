#!/usr/bin/env bats
# devShell lifecycle wrapping: prefetch and Driver run inside nix develop (issue #341).

load helper

setup() {
  setup_entrypoint_env
}

# --- devShell lifecycle wrapping (issue #341) ----------------------------------
# When the Target repo has a usable devShell, the prefetch hook and Driver
# (claude invocation) must run inside `nix develop` so the agent operates in
# the Target's exact pinned environment — not just the baked toolchain.

@test "devShell-present Driver: claude is launched inside nix develop when devShell is found" {
  seed_flake_repo
  export FAKE_NIX_DEV_SHELL_OK=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  # driver-exec's own nix develop --command <driver-bin> invocation must
  # appear in NIX_LOG beyond the probe's `--command true` (issue #626: the
  # entrypoint no longer renders its own bash wrapper for this).
  grep -v -- '--command true$' "$NIX_LOG" | grep -q -- '--command'
  grep -q "claude invoked for issue #7" "$CLAUDE_LOG"
}

@test "DEV_SHELL_NAME default: nix develop targets .#default when name is default" {
  seed_flake_repo
  export FAKE_NIX_DEV_SHELL_OK=1
  # DEV_SHELL_NAME=default is set in setup(); probe and wrappers must target .#default
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

# The launch-failure relaunch-once-in-the-baked-env policy (formerly this
# entrypoint's own bash fallback) moved wholesale into driver-exec (issue
# #626); it is covered there by a Go unit test
# (TestRunRelaunchesInBakedEnvOnEmptyStreamLaunchFailure) using a fake nix on
# PATH, not by a bats double reimplementing driver-exec's own branching.

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
  # Override the inherited PREFETCH so the prefetch test uses our command.
  export PREFETCH="warm-cache"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "warmed" "$PREFETCH_LOG"
  # Prefetch still renders its own bash wrapper (issue #626 only changed the
  # Driver run's own invocation, via driver-exec's --command <driver-bin>).
  [ "$(grep -c 'develop.*--command bash' "$NIX_LOG")" -eq 1 ]
}

@test "devShell-present Driver: MODEL is forwarded into nix develop wrapper" {
  seed_flake_repo
  export FAKE_NIX_DEV_SHELL_OK=1
  export MODEL=claude-test-model
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  # fake claude logs "model=<value>" — verify MODEL reached the wrapper
  grep -q 'model=claude-test-model' "$CLAUDE_LOG"
}

