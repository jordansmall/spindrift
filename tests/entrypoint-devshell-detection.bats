#!/usr/bin/env bats

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
  # FAKE_NIX_DEV_SHELL_OK defaults to 0, so nix develop fails.
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
  # setup_bare_repo leaves no flake.nix, so this test skips seed_flake_repo.
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

