#!/usr/bin/env bats
# Outcome verification (issue #51): PR merge state and complete-label cross-checks.

load helper

setup() {
  setup_run_env
}

# --- Outcome verification (issue #51) ----------------------------------------

@test "outcome report flags as failed when PR is not MERGED on GitHub" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tFirst issue'
  export FAKE_PODMAN_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 landing=https://github.com/owner/repo/pull/1 status=merged note=ok"
  export FAKE_GH_PR_STATE_1="OPEN"
  export FAKE_GH_ISSUE_LABELS_1="agent-complete"
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  [[ "$output" == *"!!"* ]]
  [[ "$output" == *"status=failed"* ]]
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-failed' "$GH_LOG"
}

@test "outcome report flags as failed when issue lacks the complete label" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tFirst issue'
  export FAKE_PODMAN_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 landing=https://github.com/owner/repo/pull/1 status=merged note=ok"
  export FAKE_GH_PR_STATE_1="MERGED"
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  [[ "$output" == *"!!"* ]]
  [[ "$output" == *"status=failed"* ]]
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-failed' "$GH_LOG"
}

@test "outcome report reports verified-merged when PR is MERGED and issue has complete label" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tFirst issue'
  export FAKE_PODMAN_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 landing=https://github.com/owner/repo/pull/1 status=merged note=ok"
  export FAKE_GH_PR_STATE_1="MERGED"
  export FAKE_GH_ISSUE_LABELS_1="agent-complete"
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  [[ "$output" == *"status=verified-merged"* ]]
  ! grep -q -- 'agent-failed' "$GH_LOG"
}

