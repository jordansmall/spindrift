#!/usr/bin/env bats
# runtime=docker: fake invocation, build/load, and outcome reporting.

load helper

setup() {
  setup_run_env
}

@test "runtime=docker invokes the docker fake, never podman" {
  export FAKE_DOCKER_IMAGE_PRESENT=1
  run "$DOCKER_RUN_CMD"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^run ' "$DOCKER_LOG")" -eq 2 ]
  grep -q 'ISSUE_NUMBER=1' "$DOCKER_LOG"
  [ ! -s "$PODMAN_LOG" ]
}

@test "runtime=docker builds and loads the image via docker when absent" {
  export FAKE_DOCKER_IMAGE_PRESENT=0
  export FAKE_NIX_BUILD_OK=1
  run "$DOCKER_RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q 'load -i /nix/store/' "$DOCKER_LOG"
  [ ! -s "$PODMAN_LOG" ]
}

@test "runtime=docker outcome report lists dispatched issues" {
  export FAKE_DOCKER_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tSingle'
  export FAKE_DOCKER_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 landing=https://github.com/owner/repo/pull/1 status=merged note=ok"
  export FAKE_GH_PR_STATE_1="MERGED"
  export FAKE_GH_ISSUE_LABELS_1="agent-complete"
  run "$DOCKER_RUN_CMD"
  [ "$status" -eq 0 ]
  [[ "$output" == *"#1"* ]]
  [[ "$output" == *"status=verified-merged"* ]]
}

