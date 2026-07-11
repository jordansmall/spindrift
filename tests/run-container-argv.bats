#!/usr/bin/env bats
# Container argv: entrypoint/prompt mount, empty-queue exit, hardening flags, resource limits, REPO_SLUG guard.

load helper

setup() {
  setup_run_env
}

@test "run invokes the baked entrypoint and baked prompt (no prompt mount)" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  ! grep -q 'entrypoint.sh:/agent' "$PODMAN_LOG"
  grep -q '/agent/entrypoint.sh' "$PODMAN_LOG"
  ! grep -q ':/agent/prompts' "$PODMAN_LOG"
}

@test "run exits 2 when there are no matching issues" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=""
  run "$RUN_CMD"
  [ "$status" -eq 2 ]
  [[ "$output" == *"nothing to do"* ]]
  [ "$(grep -c '^run ' "$PODMAN_LOG")" -eq 0 ]
}

@test "run includes security hardening flags in container argv" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tOnly issue'
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q -- '--cap-drop=all' "$PODMAN_LOG"
  grep -q -- '--security-opt=no-new-privileges' "$PODMAN_LOG"
  grep -q -- '--pids-limit=' "$PODMAN_LOG"
  grep -q -- '--memory=' "$PODMAN_LOG"
}

@test "PIDS_LIMIT and MEMORY_LIMIT override the baked defaults" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tOnly issue'
  export PIDS_LIMIT=256
  export MEMORY_LIMIT=2g
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q -- '--pids-limit=256' "$PODMAN_LOG"
  grep -q -- '--memory=2g' "$PODMAN_LOG"
  ! grep -q -- '--pids-limit=512' "$PODMAN_LOG"
  ! grep -q -- '--memory=4g' "$PODMAN_LOG"
}

@test "run fails fast when REPO_SLUG is missing" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  unset REPO_SLUG
  run "$RUN_CMD"
  [ "$status" -ne 0 ]
}

