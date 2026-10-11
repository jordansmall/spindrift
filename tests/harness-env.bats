#!/usr/bin/env bats

# These pin the shell wrapper's harness.env sourcing (lib/mkHarness.nix
# runShellBody, shared by the spindrift CLI), which is shell no in-process Go
# test can reach.

load helper

setup() { setup_dispatch_env; }

@test "dispatch reads config from \$PWD/harness.env" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  unset REPO_SLUG
  cat >"$BATS_TEST_TMPDIR/harness.env" <<EOF
REPO_SLUG=from-file/repo
LABEL=from-file-label
EOF
  run "$SPINDRIFT_CMD" dispatch
  [ "$status" -eq 0 ]
  grep -q 'REPO_SLUG=from-file/repo' "$PODMAN_LOG"
  grep -q -- '--label from-file-label' "$GH_LOG"
}

@test "harness.env overrides the environment (parity with old bin/run)" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  cat >"$BATS_TEST_TMPDIR/harness.env" <<EOF
LABEL=file-label
EOF
  export LABEL=env-label
  run "$SPINDRIFT_CMD" dispatch
  [ "$status" -eq 0 ]
  grep -q -- '--label file-label' "$GH_LOG"
}

# Issue #5050: the baked Driver (claude for the bats harness) always wins over
# a DRIVER in harness.env; the mismatch warns exactly once.
@test "harness.env DRIVER differing from the baked Driver warns exactly once" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  echo 'DRIVER=opencode' >"$BATS_TEST_TMPDIR/harness.env"
  run "$SPINDRIFT_CMD" dispatch
  [ "$status" -eq 0 ]
  [[ "$output" == *'DRIVER=opencode set in environment'* ]]
  [ "$(grep -c 'using the baked Driver' <<<"$output")" -eq 1 ]
}

@test "harness.env DRIVER equal to the baked Driver does not warn" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  echo 'DRIVER=claude' >"$BATS_TEST_TMPDIR/harness.env"
  run "$SPINDRIFT_CMD" dispatch
  [ "$status" -eq 0 ]
  [[ "$output" != *'using the baked Driver'* ]]
}
