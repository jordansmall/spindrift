#!/usr/bin/env bats
# Covers dispatch --no-build (issue #276): it skips the build and requires a prebuilt image.

load helper

setup() {
  setup_run_env
}

@test "dispatch --no-build fails fast with a clear message when image is absent" {
  export FAKE_PODMAN_IMAGE_PRESENT=0
  export FAKE_GH_ISSUES=$'1\tFirst issue'
  run "$SPINDRIFT_CMD" dispatch --no-build
  [ "$status" -ne 0 ]
  [[ "$output" == *"spindrift build"* ]]
  ! grep -q 'build' "$NIX_LOG"
  ! grep -q '^run ' "$PODMAN_LOG"
}

@test "dispatch --no-build runs normally when image is present" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tFirst issue'
  run "$SPINDRIFT_CMD" dispatch --no-build
  [ "$status" -eq 0 ]
  ! grep -q 'build' "$NIX_LOG"
  ! grep -q "load -i" "$PODMAN_LOG"
  grep -q 'ISSUE_NUMBER=1' "$PODMAN_LOG"
}

@test "dispatch --no-build accepts an issue number as a positional arg" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'42\tTarget issue'
  export FAKE_GH_ISSUE_LABELS_42="ready-for-agent"
  run "$SPINDRIFT_CMD" dispatch --no-build 42
  [ "$status" -eq 0 ]
  grep -q 'ISSUE_NUMBER=42' "$PODMAN_LOG"
}
