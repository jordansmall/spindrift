#!/usr/bin/env bats
# Behaviour of the nix-generated `run` command (fans out one Box per issue).

load helper

setup() {
  setup_fakes
  set_run_env
  cd "$BATS_TEST_TMPDIR"
  export FAKE_GH_ISSUES=$'1\tFirst issue\n2\tSecond issue'
}

@test "run auto-loads the image (with a log line) when it is absent" {
  export FAKE_PODMAN_IMAGE_PRESENT=0
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  [[ "$output" == *"loading"* ]]
  grep -q "load -i $IMAGE_PATH" "$PODMAN_LOG"
}

@test "run does not load the image when it is already present" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  ! grep -q "load -i" "$PODMAN_LOG"
}

@test "run fans out one container per issue" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  # One `podman run` per issue (2), plus the `image exists` probe.
  [ "$(grep -c '^run ' "$PODMAN_LOG")" -eq 2 ]
  grep -q 'ISSUE_NUMBER=1' "$PODMAN_LOG"
  grep -q 'ISSUE_NUMBER=2' "$PODMAN_LOG"
}

@test "run reads config from \$PWD/harness.env" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  unset REPO_SLUG
  cat >"$BATS_TEST_TMPDIR/harness.env" <<EOF
REPO_SLUG=from-file/repo
LABEL=from-file-label
EOF
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q 'REPO_SLUG=from-file/repo' "$PODMAN_LOG"
  grep -q -- '--label from-file-label' "$GH_LOG"
}

@test "environment overrides the built-in default when no file is present" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export LABEL=env-label
  export BRANCH_PREFIX=custom/
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q -- '--label env-label' "$GH_LOG"
  grep -q 'BRANCH_PREFIX=custom/' "$PODMAN_LOG"
}

@test "harness.env overrides the environment (parity with old bin/run)" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  cat >"$BATS_TEST_TMPDIR/harness.env" <<EOF
LABEL=file-label
EOF
  export LABEL=env-label
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q -- '--label file-label' "$GH_LOG"
}

@test "run applies default LABEL and BASE_BRANCH when unset" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q -- '--label ready-for-agent' "$GH_LOG"
  grep -q 'BASE_BRANCH=main' "$PODMAN_LOG"
}

@test "a non-default baked label changes which issues run queries" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  run "$CUSTOM_RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q -- '--label custom-label' "$GH_LOG"
}

@test "env var overrides a non-default baked default" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export LABEL=env-label
  run "$CUSTOM_RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q -- '--label env-label' "$GH_LOG"
  ! grep -q -- '--label custom-label' "$GH_LOG"
}

@test "baked defaults flow through to the container env" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  run "$CUSTOM_RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q 'BASE_BRANCH=develop' "$PODMAN_LOG"
  grep -q 'BRANCH_PREFIX=bot/' "$PODMAN_LOG"
}

@test "runtime=docker invokes the docker fake, never podman" {
  export FAKE_DOCKER_IMAGE_PRESENT=1
  run "$DOCKER_RUN_CMD"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^run ' "$DOCKER_LOG")" -eq 2 ]
  grep -q 'ISSUE_NUMBER=1' "$DOCKER_LOG"
  [ ! -s "$PODMAN_LOG" ]
}

@test "runtime=docker auto-loads the image via docker" {
  export FAKE_DOCKER_IMAGE_PRESENT=0
  run "$DOCKER_RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q 'load -i /nix/store/' "$DOCKER_LOG"
  [ ! -s "$PODMAN_LOG" ]
}

@test "run invokes the baked entrypoint but mounts the prompt (not baked)" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  # entrypoint stays baked into the image (never bind-mounted) ...
  ! grep -q 'entrypoint.sh:/agent' "$PODMAN_LOG"
  grep -q '/agent/entrypoint.sh' "$PODMAN_LOG"
  # ... while the prompt is a runtime mount at /agent/prompts.
  grep -q ':/agent/prompts' "$PODMAN_LOG"
}

@test "run exits cleanly when there are no matching issues" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=""
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^run ' "$PODMAN_LOG")" -eq 0 ]
}

@test "run fails fast when REPO_SLUG is missing" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  unset REPO_SLUG
  run "$RUN_CMD"
  [ "$status" -ne 0 ]
}
