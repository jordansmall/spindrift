#!/usr/bin/env bats
# Cold-run toolchain nudge (issue #343).

load helper

setup() {
  setup_entrypoint_env
}

# --- cold-run toolchain nudge (issue #343) ------------------------------------

@test "nudge: hint emitted when no prefetch configured and go.sum present" {
  seed_dependency_manifest "go.sum"
  unset PREFETCH
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "go mod"
  echo "$output" | grep -q "prefetch"
  echo "$output" | grep -q "packages"
}

@test "nudge: hint emitted when no prefetch configured and build.gradle.kts present" {
  # Gradle projects don't always commit a lockfile, so a build/settings file
  # alone must be sufficient to trigger the hint.
  seed_dependency_manifest "build.gradle.kts"
  unset PREFETCH
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "gradle"
  echo "$output" | grep -q "prefetch"
  echo "$output" | grep -q "packages"
}

@test "nudge: hint emitted when no prefetch configured and build.gradle present" {
  seed_dependency_manifest "build.gradle"
  unset PREFETCH
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "gradle"
}

@test "nudge: hint emitted when no prefetch configured and settings.gradle present" {
  seed_dependency_manifest "settings.gradle"
  unset PREFETCH
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "gradle"
}

@test "nudge: hint emitted when no prefetch configured and settings.gradle.kts present" {
  seed_dependency_manifest "settings.gradle.kts"
  unset PREFETCH
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "gradle"
}

@test "nudge: hint emitted when no prefetch configured and gradle.lockfile present" {
  seed_dependency_manifest "gradle.lockfile"
  unset PREFETCH
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "gradle"
}

@test "nudge: hint suppressed when prefetch is configured" {
  seed_dependency_manifest "go.sum"
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
