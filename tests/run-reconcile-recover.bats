#!/usr/bin/env bats
# Reconcile stranded issues (issue #193) and the engage (#195) / recover (#281) subcommands.

load helper

setup() {
  setup_run_env
}

# --- Reconcile stranded issues (issue #193) -----------------------------------

@test "reconcile: stranded in-progress issue with green PR is adopted and merged without new dispatch" {
  export MERGE_MODE=immediate
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tStranded issue'
  # Pre-seed GH_STATE so issue 1 carries the in-progress label, not ready-for-agent.
  printf '1\tagent-in-progress\n' >> "$GH_LOG.state"
  export FAKE_GH_PR_LIST_1="https://github.com/owner/repo/pull/1"
  # FAKE_GH_PR_DRAFT_1 not set → defaults to "false" (non-draft)
  export FAKE_GH_GRAPHQL_ROLLUP_1="SUCCESS"
  run "$RUN_CMD"
  # After reconcile merges the stranded PR, discoverIssues finds no
  # ready-for-agent issues → launcher exits 2 (queue empty).
  [ "$status" -eq 2 ]
  [[ "$output" == *"status=adopted"* ]]
  [[ "$output" == *"status=verified-merged"* ]]
  grep -q 'pr merge' "$GH_LOG"
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-complete --remove-label agent-in-progress' "$GH_LOG"
  ! grep -q 'ISSUE_NUMBER=1' "$PODMAN_LOG"
}

@test "reconcile: stranded in-progress issue with draft PR is left untouched" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tStranded issue'
  printf '1\tagent-in-progress\n' >> "$GH_LOG.state"
  export FAKE_GH_PR_LIST_1="https://github.com/owner/repo/pull/1"
  export FAKE_GH_PR_DRAFT_1="true"
  run "$RUN_CMD"
  # Draft PR → reconcile skips; discoverIssues finds no ready-for-agent
  # issues (only in-progress) → launcher exits 2 (queue empty).
  [ "$status" -eq 2 ]
  ! grep -q 'pr merge' "$GH_LOG"
  ! grep -q 'agent-complete' "$GH_LOG"
  ! grep -q -- 'agent-failed' "$GH_LOG"
}

@test "reconcile: stranded in-progress issue with no open PR is left untouched" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tStranded issue'
  printf '1\tagent-in-progress\n' >> "$GH_LOG.state"
  # No FAKE_GH_PR_LIST_1 → no PR found
  run "$RUN_CMD"
  # No PR → reconcile skips; discoverIssues finds no ready-for-agent
  # issues (only in-progress) → launcher exits 2 (queue empty).
  [ "$status" -eq 2 ]
  ! grep -q 'pr merge' "$GH_LOG"
  ! grep -q 'agent-complete' "$GH_LOG"
  ! grep -q -- 'agent-failed' "$GH_LOG"
}

# --- engage subcommand (issue #195) ------------------------------------------

@test "recover: green PR is adopted and merged (via issue #195)" {
  export MERGE_MODE=immediate
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tStranded issue'
  printf '1\tagent-in-progress\n' >> "$GH_LOG.state"
  export FAKE_GH_PR_LIST_1="https://github.com/owner/repo/pull/1"
  export FAKE_GH_GRAPHQL_ROLLUP_1="SUCCESS"
  run "$SPINDRIFT_CMD" recover 1
  [ "$status" -eq 0 ]
  [[ "$output" == *"status=adopted"* ]]
  [[ "$output" == *"status=verified-merged"* ]]
  grep -q 'pr merge' "$GH_LOG"
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-complete --remove-label agent-in-progress' "$GH_LOG"
}

@test "recover: draft PR is skipped and exits non-zero (via issue #195)" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tStranded issue'
  printf '1\tagent-in-progress\n' >> "$GH_LOG.state"
  export FAKE_GH_PR_LIST_1="https://github.com/owner/repo/pull/1"
  export FAKE_GH_PR_DRAFT_1="true"
  run "$SPINDRIFT_CMD" recover 1
  [ "$status" -ne 0 ]
  [[ "$output" == *"status=skipped"* ]]
  ! grep -q 'pr merge' "$GH_LOG"
  ! grep -q 'agent-complete' "$GH_LOG"
  ! grep -q -- 'agent-failed' "$GH_LOG"
}

@test "recover: no open PR exits non-zero without label churn (via issue #195)" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tStranded issue'
  printf '1\tagent-in-progress\n' >> "$GH_LOG.state"
  # No FAKE_GH_PR_LIST_1 → no PR found
  run "$SPINDRIFT_CMD" recover 1
  [ "$status" -ne 0 ]
  [[ "$output" == *"status=skipped"* ]]
  ! grep -q 'pr merge' "$GH_LOG"
  ! grep -q 'agent-complete' "$GH_LOG"
  ! grep -q -- 'agent-failed' "$GH_LOG"
}

# --- recover subcommand (issue #281) -----------------------------------------

@test "recover: green PR is adopted and merged" {
  export MERGE_MODE=immediate
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tStranded issue'
  printf '1\tagent-in-progress\n' >> "$GH_LOG.state"
  export FAKE_GH_PR_LIST_1="https://github.com/owner/repo/pull/1"
  export FAKE_GH_GRAPHQL_ROLLUP_1="SUCCESS"
  run "$SPINDRIFT_CMD" recover 1
  [ "$status" -eq 0 ]
  [[ "$output" == *"status=adopted"* ]]
  [[ "$output" == *"status=verified-merged"* ]]
  grep -q 'pr merge' "$GH_LOG"
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-complete --remove-label agent-in-progress' "$GH_LOG"
}

@test "recover: draft PR is skipped and exits non-zero" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tStranded issue'
  printf '1\tagent-in-progress\n' >> "$GH_LOG.state"
  export FAKE_GH_PR_LIST_1="https://github.com/owner/repo/pull/1"
  export FAKE_GH_PR_DRAFT_1="true"
  run "$SPINDRIFT_CMD" recover 1
  [ "$status" -ne 0 ]
  [[ "$output" == *"status=skipped"* ]]
  ! grep -q 'pr merge' "$GH_LOG"
  ! grep -q 'agent-complete' "$GH_LOG"
  ! grep -q -- 'agent-failed' "$GH_LOG"
}

@test "recover: no open PR exits non-zero without label churn" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tStranded issue'
  printf '1\tagent-in-progress\n' >> "$GH_LOG.state"
  # No FAKE_GH_PR_LIST_1 → no PR found
  run "$SPINDRIFT_CMD" recover 1
  [ "$status" -ne 0 ]
  [[ "$output" == *"status=skipped"* ]]
  ! grep -q 'pr merge' "$GH_LOG"
  ! grep -q 'agent-complete' "$GH_LOG"
  ! grep -q -- 'agent-failed' "$GH_LOG"
}

