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

@test "run passes the baked default MODEL into the container" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q 'MODEL=claude-opus-4-8' "$PODMAN_LOG"
}

@test "MODEL env overrides the baked default into the container" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export MODEL=claude-sonnet-4-6
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q 'MODEL=claude-sonnet-4-6' "$PODMAN_LOG"
  ! grep -q 'MODEL=claude-opus-4-8' "$PODMAN_LOG"
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
  ! grep -q 'entrypoint.sh:/agent' "$PODMAN_LOG"
  grep -q '/agent/entrypoint.sh' "$PODMAN_LOG"
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

# --- Label lifecycle (issue #15) ------------------------------------------

@test "dispatch swaps ready-for-agent -> agent-in-progress on each issue" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-in-progress --remove-label ready-for-agent' "$GH_LOG"
  grep -q -- 'issue edit 2 --repo owner/repo --add-label agent-in-progress --remove-label ready-for-agent' "$GH_LOG"
}

@test "re-running run mid-flight dispatches nothing new for in-progress issues" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^run ' "$PODMAN_LOG")" -eq 2 ]
  # Second invocation: both issues now carry agent-in-progress, so the
  # ready-for-agent query returns nothing and no new container starts.
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  [[ "$output" == *"nothing to do"* ]]
  [ "$(grep -c '^run ' "$PODMAN_LOG")" -eq 2 ]
}

@test "a non-zero container exit swaps agent-in-progress -> agent-failed" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_PODMAN_RUN_EXIT=1
  export FAKE_GH_ISSUES=$'1\tOnly issue'
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-failed --remove-label agent-in-progress' "$GH_LOG"
}

@test "a successful run never escalates to agent-failed" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q -- '--add-label agent-in-progress' "$GH_LOG"
  ! grep -q -- 'agent-failed' "$GH_LOG"
}

@test "IN_PROGRESS_LABEL and FAILED_LABEL env vars override the baked defaults" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_PODMAN_RUN_EXIT=1
  export FAKE_GH_ISSUES=$'1\tOnly issue'
  export IN_PROGRESS_LABEL=wip
  export FAILED_LABEL=broken
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q -- 'issue edit 1 --repo owner/repo --add-label wip --remove-label ready-for-agent' "$GH_LOG"
  grep -q -- 'issue edit 1 --repo owner/repo --add-label broken --remove-label wip' "$GH_LOG"
}

# --- Model tiers and complete label (issue #36) ----------------------------

@test "run passes IN_PROGRESS_LABEL into each container" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q 'IN_PROGRESS_LABEL=agent-in-progress' "$PODMAN_LOG"
}

@test "run passes the baked default COMPLETE_LABEL into each container" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q 'COMPLETE_LABEL=agent-complete' "$PODMAN_LOG"
}

@test "COMPLETE_LABEL env overrides the baked default into the container" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export COMPLETE_LABEL=done
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q 'COMPLETE_LABEL=done' "$PODMAN_LOG"
  ! grep -q 'COMPLETE_LABEL=agent-complete' "$PODMAN_LOG"
}

@test "run passes SCOUT_MODEL and REVIEW_MODEL into each container" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export SCOUT_MODEL=claude-haiku-3-5
  export REVIEW_MODEL=claude-opus-4-5
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q 'SCOUT_MODEL=claude-haiku-3-5' "$PODMAN_LOG"
  grep -q 'REVIEW_MODEL=claude-opus-4-5' "$PODMAN_LOG"
}

# --- Dependency-wave ordering (issue #39) ----------------------------------

@test "run dispatches an issue whose external blocker already carries COMPLETE_LABEL" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'2\tDependent'
  export FAKE_GH_ISSUE_BODY_2="Depends on #1"
  export FAKE_GH_ISSUE_LABELS_1="agent-complete"
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q 'ISSUE_NUMBER=2' "$PODMAN_LOG"
}

@test "run parses 'blocked by' syntax as a blocker reference" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'2\tDependent'
  export FAKE_GH_ISSUE_BODY_2="blocked by #1"
  export FAKE_GH_ISSUE_LABELS_1="agent-complete"
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q 'ISSUE_NUMBER=2' "$PODMAN_LOG"
}

@test "run errors on a dependency cycle in the ready batch" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tA\n2\tB'
  export FAKE_GH_ISSUE_BODY_1="depends on #2"
  export FAKE_GH_ISSUE_BODY_2="depends on #1"
  run "$RUN_CMD"
  [ "$status" -ne 0 ]
  [[ "$output" == *"cycle"* ]]
}

@test "run surfaces a never-completing blocker instead of deadlocking" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tDependent'
  export FAKE_GH_ISSUE_BODY_1="depends on #99"
  export DEPS_WAIT_SECS=0
  run "$RUN_CMD"
  [ "$status" -ne 0 ]
  [[ "$output" == *"deadlock"* || "$output" == *"ERROR"* ]]
}

@test "run dispatches the blocker before the dependent (wave ordering)" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tBlocker\n2\tDependent'
  export FAKE_GH_ISSUE_BODY_2="depends on #1"
  export FAKE_PODMAN_AUTO_COMPLETE=1
  export GH_STATE="$GH_LOG.state"
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q 'ISSUE_NUMBER=1' "$PODMAN_LOG"
  grep -q 'ISSUE_NUMBER=2' "$PODMAN_LOG"
  line1="$(grep -n 'ISSUE_NUMBER=1' "$PODMAN_LOG" | cut -d: -f1 | head -1)"
  line2="$(grep -n 'ISSUE_NUMBER=2' "$PODMAN_LOG" | cut -d: -f1 | head -1)"
  [ "$line1" -lt "$line2" ]
}

@test "run preserves single-wave behaviour when no dependencies are declared" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^run ' "$PODMAN_LOG")" -eq 2 ]
}
