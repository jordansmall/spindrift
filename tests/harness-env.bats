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

# Issue #5053 (ADR 0064): in a multi-Driver flake harness.<driver>.env layers
# over harness.env for the Harness's own baked Driver (claude here); a
# single-Driver Harness ignores it.
@test "multi-Driver: harness.claude.env overrides harness.env" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  echo 'LABEL=shared-label' >"$BATS_TEST_TMPDIR/harness.env"
  echo 'LABEL=claude-label' >"$BATS_TEST_TMPDIR/harness.claude.env"
  run "$SPINDRIFT_MULTI_DRIVER_CMD" dispatch
  [ "$status" -eq 0 ]
  grep -q -- '--label claude-label' "$GH_LOG"
}

@test "multi-Driver: harness.opencode.env is not read by the claude app" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  echo 'LABEL=shared-label' >"$BATS_TEST_TMPDIR/harness.env"
  echo 'LABEL=opencode-label' >"$BATS_TEST_TMPDIR/harness.opencode.env"
  run "$SPINDRIFT_MULTI_DRIVER_CMD" dispatch
  [ "$status" -eq 0 ]
  grep -q -- '--label shared-label' "$GH_LOG"
}

@test "multi-Driver: a shared knob in harness.env does not warn" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  printf 'LABEL=shared-label\nREPO_SLUG=owner/repo\n' >"$BATS_TEST_TMPDIR/harness.env"
  run "$SPINDRIFT_MULTI_DRIVER_CMD" dispatch
  [ "$status" -eq 0 ]
  [[ "$output" != *'Driver-valued'* ]]
}

@test "multi-Driver: a Driver-valued knob in harness.env warns exactly once" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  printf 'LABEL=shared-label\nexport MODEL=some-model\n' >"$BATS_TEST_TMPDIR/harness.env"
  run "$SPINDRIFT_MULTI_DRIVER_CMD" dispatch
  [ "$status" -eq 0 ]
  [ "$(grep -c 'harness.env sets MODEL' <<<"$output")" -eq 1 ]
  [[ "$output" == *'harness.claude.env'* ]]
}

@test "multi-Driver: a Driver-valued knob assigned twice in harness.env warns once" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  printf 'MODEL=first\nexport MODEL=second\n' >"$BATS_TEST_TMPDIR/harness.env"
  run "$SPINDRIFT_MULTI_DRIVER_CMD" dispatch
  [ "$status" -eq 0 ]
  [ "$(grep -c 'harness.env sets MODEL' <<<"$output")" -eq 1 ]
}

@test "multi-Driver: readonly, declare -x, and += assignments of a Driver-valued knob warn" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  printf 'readonly MODEL=a\ndeclare -x EFFORT=high\nREVIEW_MODEL+=b\n' >"$BATS_TEST_TMPDIR/harness.env"
  run "$SPINDRIFT_MULTI_DRIVER_CMD" dispatch
  [ "$status" -eq 0 ]
  [[ "$output" == *'harness.env sets MODEL,'* ]]
  [[ "$output" == *'harness.env sets EFFORT,'* ]]
  [[ "$output" == *'harness.env sets REVIEW_MODEL,'* ]]
}

@test "multi-Driver: a dispatch-time flag beats harness.claude.env" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  echo 'LABEL=claude-label' >"$BATS_TEST_TMPDIR/harness.claude.env"
  run "$SPINDRIFT_MULTI_DRIVER_CMD" dispatch --dispatch-label flag-label
  [ "$status" -eq 0 ]
  grep -q -- '--label flag-label' "$GH_LOG"
}

@test "multi-Driver: harness.claude.env overrides the environment" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  echo 'LABEL=claude-label' >"$BATS_TEST_TMPDIR/harness.claude.env"
  export LABEL=env-label
  run "$SPINDRIFT_MULTI_DRIVER_CMD" dispatch
  [ "$status" -eq 0 ]
  grep -q -- '--label claude-label' "$GH_LOG"
}

@test "multi-Driver: a Driver-valued knob only in harness.claude.env does not warn" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  echo 'LABEL=shared-label' >"$BATS_TEST_TMPDIR/harness.env"
  echo 'MODEL=some-model' >"$BATS_TEST_TMPDIR/harness.claude.env"
  run "$SPINDRIFT_MULTI_DRIVER_CMD" dispatch
  [ "$status" -eq 0 ]
  [[ "$output" != *'Driver-valued'* ]]
}

@test "single-Driver: harness.claude.env is ignored and a Driver-valued knob does not warn" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  printf 'LABEL=shared-label\nMODEL=some-model\n' >"$BATS_TEST_TMPDIR/harness.env"
  echo 'LABEL=claude-label' >"$BATS_TEST_TMPDIR/harness.claude.env"
  run "$SPINDRIFT_CMD" dispatch
  [ "$status" -eq 0 ]
  grep -q -- '--label shared-label' "$GH_LOG"
  [[ "$output" != *'Driver-valued'* ]]
}
