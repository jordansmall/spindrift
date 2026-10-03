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
