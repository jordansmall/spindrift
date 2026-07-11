#!/usr/bin/env bats
# Cold-run toolchain nudge (issue #343).

load helper

setup() {
  setup_entrypoint_env
}

# --- cold-run toolchain nudge (issue #343) ------------------------------------

@test "nudge: hint emitted when no prefetch configured and go.sum present" {
  seed_lockfile "go.sum"
  unset PREFETCH
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "go mod"
  echo "$output" | grep -q "prefetch"
  echo "$output" | grep -q "packages"
}

@test "nudge: hint suppressed when prefetch is configured" {
  seed_lockfile "go.sum"
  export PREFETCH="true"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! echo "$output" | grep -q "hint:"
}

@test "nudge: hint suppressed when no recognized lockfile present" {
  # Default setup_bare_repo seeds only README.md — no lockfile.
  unset PREFETCH
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! echo "$output" | grep -q "hint:"
}
