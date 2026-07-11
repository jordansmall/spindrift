#!/usr/bin/env bats
# devShell detection probe.

load helper

setup() {
  setup_entrypoint_env
}

@test "entrypoint detects devShell and logs when flake.nix has a devShell" {
  seed_flake_repo
  export FAKE_NIX_DEV_SHELL_OK=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "devShell"
  echo "$output" | grep -q "nix develop"
}

@test "entrypoint logs fallback when flake.nix has no devShell" {
  seed_flake_repo
  # FAKE_NIX_DEV_SHELL_OK defaults to 0 — nix develop will fail
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "flake.nix"
  echo "$output" | grep -q "baked toolchain"
}

@test "entrypoint times out the devShell probe and falls back to baked toolchain" {
  seed_flake_repo
  export FAKE_NIX_DEV_SHELL_HANG=1
  export DEV_SHELL_PROBE_TIMEOUT=1
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "timed out"
  echo "$output" | grep -q "baked toolchain"
}

@test "entrypoint skips devShell probe when repo has no flake.nix" {
  # standard setup_bare_repo has no flake.nix
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! echo "$output" | grep -q "devShell"
}

@test "entrypoint skips the prefetch hook when it is empty" {
  export PREFETCH_LOG="$BATS_TEST_TMPDIR/prefetch.log"
  {
    printf '#!%s\n' "$(command -v bash)"
    cat <<'FAKE'
echo ran >>"$PREFETCH_LOG"
FAKE
  } >"$FAKE_BIN/warm-cache"
  chmod +x "$FAKE_BIN/warm-cache"
  export PREFETCH=""
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ ! -f "$PREFETCH_LOG" ]
}

