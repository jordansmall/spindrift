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

@test "entrypoint survives a failing prefetch hook with no devShell (issue #3943)" {
  # No seed_flake_repo -- exercises the no-devShell branch of phase_prefetch.
  export PREFETCH="false"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "driver invoked for issue" "$DRIVER_LOG"
}

@test "entrypoint survives a prefetch hook that exits 0 with no devShell (issue #3943)" {
  # No seed_flake_repo -- exercises the no-devShell branch of phase_prefetch.
  # Under the old `eval`, `exit 0` ended the whole Box before the Driver ran.
  export PREFETCH="exit 0"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "driver invoked for issue" "$DRIVER_LOG"
}

@test "entrypoint's prefetch hook cannot leak a cwd change into the Driver (issue #3943)" {
  # No seed_flake_repo -- exercises the no-devShell branch of phase_prefetch.
  # Wraps driver-exec to record its cwd before delegating to the real fake, so
  # a `cd` in PREFETCH leaking into later phases is observable.
  PWD_LOG="$BATS_TEST_TMPDIR/driver-exec-pwd.log"
  export PWD_LOG
  {
    printf '#!%s\n' "$(command -v bash)"
    cat <<'FAKE'
pwd >>"$PWD_LOG"
exec "$FAKES_DIR/driver-exec" "$@"
FAKE
  } >"$FAKE_BIN/driver-exec"
  chmod +x "$FAKE_BIN/driver-exec"
  export PREFETCH="mkdir -p sub && cd sub"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  # driver-exec runs more than once (e.g. the registry-proxy bind-registry
  # pass ahead of clone_repo), so check the last invocation -- the Driver run
  # itself -- rather than every line in the log.
  [ "$(tail -n1 "$PWD_LOG")" = "$WORK_DIR" ]
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

