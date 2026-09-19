#!/usr/bin/env bats
# Cold-run toolchain nudge (issue #343).

load helper

setup() {
  setup_entrypoint_env
}

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

@test "nudge: hint emitted when no prefetch configured and package-lock.json present" {
  seed_dependency_manifest "package-lock.json"
  unset PREFETCH
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "npm/pnpm/yarn"
}

@test "nudge: hint emitted when no prefetch configured and yarn.lock present" {
  seed_dependency_manifest "yarn.lock"
  unset PREFETCH
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "npm/pnpm/yarn"
}

@test "nudge: hint emitted when no prefetch configured and pnpm-lock.yaml present" {
  seed_dependency_manifest "pnpm-lock.yaml"
  unset PREFETCH
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "npm/pnpm/yarn"
}

@test "nudge: hint suppressed when prefetch is configured" {
  seed_dependency_manifest "go.sum"
  export PREFETCH="true"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! echo "$output" | grep -q "hint:"
}

@test "nudge: hint suppressed when no recognized lockfile present" {
  # Default setup_bare_repo seeds only README.md, so no lockfile exists.
  unset PREFETCH
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! echo "$output" | grep -q "hint:"
}

@test "nudge: driver-exec bind-registry failure warns but does not abort the run" {
  # A non-zero driver-exec exit here must not take the whole box run down
  # under set -euo pipefail, because this phase only emits a cosmetic hint.
  # The fake fails the bind-registry verb alone and delegates every other
  # verb to the real fake, so the rest of the run still proceeds.
  seed_dependency_manifest "go.sum"
  unset PREFETCH
  stub_failing_bind_registry
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "driver-exec bind-registry failed"
  ! echo "$output" | grep -q "hint:"
  grep -q "driver invoked for issue #7" "$DRIVER_LOG"
}

@test "nudge: malformed bind-registry env output does not crash the run or leak the tempfile" {
  # A bind-registry exit of 0 with an unterminated quote in the env file makes
  # `source` itself fail. Under set -euo pipefail an unguarded source failure
  # aborts the entrypoint before the `rm -f "$_nudge_env_out"` that follows,
  # leaking the tempfile and crashing a run that this hint-only phase must
  # never crash.
  seed_dependency_manifest "go.sum"
  unset PREFETCH
  nudge_path_log="$BATS_TEST_TMPDIR/nudge_env_path"
  {
    printf '#!%s\n' "$(command -v bash)"
    cat <<FAKE
if [ "\$1" = "bind-registry" ]; then
  shift
  out=""
  while [ \$# -gt 0 ]; do
    case "\$1" in
      --ecosystem-env-output) out="\$2"; shift 2 ;;
      *) shift ;;
    esac
  done
  printf '%s\n' "\$out" >"$nudge_path_log"
  printf 'NUDGE_ECOSYSTEM="unterminated\n' >"\$out"
  exit 0
fi
exec "$FAKES_DIR/driver-exec" "\$@"
FAKE
  } >"$FAKE_BIN/driver-exec"
  chmod +x "$FAKE_BIN/driver-exec"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! echo "$output" | grep -q "hint:"
  grep -q "driver invoked for issue #7" "$DRIVER_LOG"
  [ -f "$nudge_path_log" ]
  nudge_env_path="$(cat "$nudge_path_log")"
  [ ! -e "$nudge_env_path" ]
}

@test "nudge: driver-exec bind-registry failure stays silent when prefetch is configured" {
  # With PREFETCH set the old lockfile-chain code emitted nothing, so the
  # WARNING must stay gated on the same PREFETCH check the hint itself uses.
  seed_dependency_manifest "go.sum"
  export PREFETCH="true"
  stub_failing_bind_registry
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! echo "$output" | grep -q "WARNING"
  grep -q "driver invoked for issue #7" "$DRIVER_LOG"
}
