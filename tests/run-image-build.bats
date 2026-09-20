#!/usr/bin/env bats

load helper

setup() {
  setup_run_env
}

@test "run builds and loads the image (with a log line) when it is absent" {
  export FAKE_PODMAN_IMAGE_PRESENT=0
  export FAKE_NIX_BUILD_OK=1
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  [[ "$output" == *"building"* ]]
  grep -q 'build' "$NIX_LOG"
  grep -q "load -i $IMAGE_PATH" "$PODMAN_LOG"
}

@test "run falls back to container build when host cannot realize the image" {
  export FAKE_PODMAN_IMAGE_PRESENT=0
  export FAKE_NIX_BUILD_OK=0
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q 'build' "$NIX_LOG"
  grep -q 'spindrift-nix:/nix' "$PODMAN_LOG"
  grep -q 'ISSUE_NUMBER=1' "$PODMAN_LOG"
}

@test "run aborts with an error and does not launch containers when the build fails" {
  export FAKE_PODMAN_IMAGE_PRESENT=0
  export FAKE_NIX_BUILD_OK=0
  export FAKE_PODMAN_RUN_EXIT=1
  run "$RUN_CMD"
  [ "$status" -ne 0 ]
  [[ "$output" == *"container build failed"* ]]
  ! grep -q 'ISSUE_NUMBER' "$PODMAN_LOG"
}

@test "run does not load the image when it is already present" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  ! grep -q "load -i" "$PODMAN_LOG"
}

@test "run gates on the content-hash tag, not spindrift:latest" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  # IMAGE_PATH is /nix/store/<32-char hash>-spindrift, hence the 11/32 offsets.
  image_hash="${IMAGE_PATH:11:32}"
  grep -q "image inspect spindrift:$image_hash" "$PODMAN_LOG"
  ! grep -q 'image inspect spindrift:latest' "$PODMAN_LOG"
}

@test "run also tags the image with the content-hash tag when building" {
  export FAKE_PODMAN_IMAGE_PRESENT=0
  export FAKE_NIX_BUILD_OK=1
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  image_hash="${IMAGE_PATH:11:32}"
  grep -q "^tag spindrift:latest spindrift:$image_hash" "$PODMAN_LOG"
}

@test "run launches one container per issue" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  wait_for_log_lines "$PODMAN_LOG" '^run ' 2
  grep -q 'ISSUE_NUMBER=1' "$PODMAN_LOG"
  grep -q 'ISSUE_NUMBER=2' "$PODMAN_LOG"
}

@test "run reaps a stale same-named container before launching (interrupted prior run)" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tOnly issue'
  export FAKE_PODMAN_CONTAINER_STATE_agent_issue_1="exited"
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  # By the ID the inspect observed, never the name: a name resolves again at
  # removal time and could land on a sibling's fresh container (issue #3633).
  grep -q -- 'rm -f agent-issue-1-id' "$PODMAN_LOG"
  # The reap must precede the run, or the --name collision still fires.
  rm_line="$(grep -n -- 'rm -f agent-issue-1-id' "$PODMAN_LOG" | head -1 | cut -d: -f1)"
  run_line="$(grep -n '^run .*ISSUE_NUMBER=1' "$PODMAN_LOG" | head -1 | cut -d: -f1)"
  [ "$rm_line" -lt "$run_line" ]
}

@test "run skips stale-reap for a running container (concurrent invocation is safe)" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tOnly issue'
  export FAKE_PODMAN_CONTAINER_STATE_agent_issue_1="running"
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  ! grep -q -- 'rm -f agent-issue-1' "$PODMAN_LOG"
}

@test "run skips stale-reap for a just-created container (sibling mid-launch is safe)" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tOnly issue'
  export FAKE_PODMAN_CONTAINER_STATE_agent_issue_1="created"
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  ! grep -q -- 'rm -f agent-issue-1' "$PODMAN_LOG"
}
