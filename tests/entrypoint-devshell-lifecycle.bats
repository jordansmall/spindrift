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
  # nix develop --command bash <wrapper> must appear in NIX_LOG (beyond the probe)
  grep -q 'develop.*--command bash' "$NIX_LOG"
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

@test "launch-failure relaunch: entrypoint relaunches in baked env when nix develop cannot exec Driver" {
  seed_flake_repo
  export FAKE_NIX_DEV_SHELL_OK=1
  export FAKE_NIX_DEV_SHELL_LAUNCH_FAIL=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  # nix develop was attempted for the Driver
  grep -q 'develop.*--command bash' "$NIX_LOG"
  # Claude was still invoked (in baked env as fallback)
  grep -q "claude invoked for issue" "$CLAUDE_LOG"
}

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
  # Both prefetch and Driver wrappers use --command bash (probe uses --command true).
  [ "$(grep -c 'develop.*--command bash' "$NIX_LOG")" -ge 2 ]
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

