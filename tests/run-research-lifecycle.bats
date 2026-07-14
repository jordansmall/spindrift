#!/usr/bin/env bats
# Research dispatch kind, end-to-end against a fake Box (issue #639, ADR
# 0022): agent-research -> agent-research-in-progress -> verdict label (or
# agent-research-failed on blocked/missing/crashed), one-shot — no merge, no
# CI watch. Modeled on run-label-lifecycle.bats and run-outcome-report.bats.

load helper

setup() {
  setup_run_env
}

@test "research swaps agent-research -> agent-research-in-progress on each issue" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  run "$SPINDRIFT_CMD" research
  [ "$status" -eq 0 ]
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-research-in-progress --remove-label agent-research' "$GH_LOG"
  grep -q -- 'issue edit 2 --repo owner/repo --add-label agent-research-in-progress --remove-label agent-research' "$GH_LOG"
}

@test "a recommend verdict swaps agent-research-in-progress -> agent-research-recommend" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tOnly issue'
  export FAKE_PODMAN_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 landing=https://github.com/owner/repo/issues/1#issuecomment-1 status=recommend note=grounded in code"
  run "$SPINDRIFT_CMD" research
  [ "$status" -eq 0 ]
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-research-recommend --remove-label agent-research-in-progress' "$GH_LOG"
}

@test "a reject verdict swaps agent-research-in-progress -> agent-research-reject (Complete, not Failed)" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tOnly issue'
  export FAKE_PODMAN_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 landing=https://github.com/owner/repo/issues/1#issuecomment-1 status=reject note=duplicate of #3"
  run "$SPINDRIFT_CMD" research
  [ "$status" -eq 0 ]
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-research-reject --remove-label agent-research-in-progress' "$GH_LOG"
  ! grep -q -- 'agent-research-failed' "$GH_LOG"
}

@test "an unclear verdict swaps agent-research-in-progress -> agent-research-unclear" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tOnly issue'
  export FAKE_PODMAN_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 landing=https://github.com/owner/repo/issues/1#issuecomment-1 status=unclear note=needs answers"
  run "$SPINDRIFT_CMD" research
  [ "$status" -eq 0 ]
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-research-unclear --remove-label agent-research-in-progress' "$GH_LOG"
}

@test "a blocked outcome swaps agent-research-in-progress -> agent-research-failed" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tOnly issue'
  export FAKE_PODMAN_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 landing=https://github.com/owner/repo/issues/1#issuecomment-1 status=blocked note=push rejected"
  run "$SPINDRIFT_CMD" research
  [ "$status" -eq 0 ]
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-research-failed --remove-label agent-research-in-progress' "$GH_LOG"
}

@test "a missing outcome line swaps agent-research-in-progress -> agent-research-failed" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tOnly issue'
  # No FAKE_PODMAN_OUTCOME_1 -> no SPINDRIFT_OUTCOME in log.
  run "$SPINDRIFT_CMD" research
  [ "$status" -eq 0 ]
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-research-failed --remove-label agent-research-in-progress' "$GH_LOG"
}

@test "a non-zero container exit swaps agent-research-in-progress -> agent-research-failed" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_PODMAN_RUN_EXIT=1
  export FAKE_GH_ISSUES=$'1\tOnly issue'
  run "$SPINDRIFT_CMD" research
  [ "$status" -eq 0 ]
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-research-failed --remove-label agent-research-in-progress' "$GH_LOG"
}

@test "research passes DISPATCH_KIND=research into each container" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  run "$SPINDRIFT_CMD" research
  [ "$status" -eq 0 ]
  grep -q 'DISPATCH_KIND=research' "$PODMAN_LOG"
}

@test "dispatch (work) passes DISPATCH_KIND=work into each container" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  run "$SPINDRIFT_CMD" dispatch
  [ "$status" -eq 0 ]
  grep -q 'DISPATCH_KIND=work' "$PODMAN_LOG"
}

@test "research <nums> dispatches exactly the named issue, bypassing the agent-research label" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tUnlabeled issue'
  run "$SPINDRIFT_CMD" research --yes 1
  [ "$status" -eq 0 ]
  [ "$(grep -c '^run ' "$PODMAN_LOG")" -eq 1 ]
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-research-in-progress' "$GH_LOG"
}

@test "research never touches the work label family" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  run "$SPINDRIFT_CMD" research
  [ "$status" -eq 0 ]
  ! grep -q -- '--add-label ready-for-agent' "$GH_LOG"
  ! grep -q -- '--add-label agent-in-progress' "$GH_LOG"
  ! grep -q -- '--add-label agent-complete' "$GH_LOG"
}
