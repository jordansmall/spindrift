#!/usr/bin/env bats
# Reconcile stranded issues (issue #193) and the engage (#195) / recover (#281) subcommands.

load helper

setup() {
  setup_run_env
}

@test "reconcile: stranded in-progress issue with green open PR is left untouched (#600)" {
  export MERGE_MODE=immediate
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tStranded issue'
  # Pre-seed GH_STATE so issue 1 carries the in-progress label, not ready-for-agent.
  printf '1\tagent-in-progress\n' >> "$GH_LOG.state"
  export FAKE_GH_PR_LIST_1="https://github.com/owner/repo/pull/1"
  # FAKE_GH_PR_DRAFT_1 is unset, so the fake reports a non-draft PR.
  export FAKE_GH_GRAPHQL_ROLLUP_1="SUCCESS"
  run "$RUN_CMD"
  # A bare agent-in-progress issue is indistinguishable from one a live runner
  # is still working, so dispatch must never adopt it on the label alone, green
  # PR or not (#600). discoverIssues finds no ready-for-agent issue, so the
  # launcher exits 2 on an empty queue.
  [ "$status" -eq 2 ]
  [[ "$output" != *"status=adopted"* ]]
  ! grep -q 'pr merge' "$GH_LOG"
  ! grep -q -- 'agent-complete' "$GH_LOG"
  ! grep -q 'ISSUE_NUMBER=1' "$PODMAN_LOG"
}

@test "reconcile: stranded in-progress issue with draft PR is left untouched" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tStranded issue'
  printf '1\tagent-in-progress\n' >> "$GH_LOG.state"
  export FAKE_GH_PR_LIST_1="https://github.com/owner/repo/pull/1"
  export FAKE_GH_PR_DRAFT_1="true"
  run "$RUN_CMD"
  # A draft PR makes reconcile skip, and discoverIssues finds no ready-for-agent
  # issue, so the launcher exits 2 on an empty queue.
  [ "$status" -eq 2 ]
  ! grep -q 'pr merge' "$GH_LOG"
  ! grep -q 'agent-complete' "$GH_LOG"
  ! grep -q -- 'agent-failed' "$GH_LOG"
}

@test "reconcile: stranded in-progress issue with no open PR is left untouched" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tStranded issue'
  printf '1\tagent-in-progress\n' >> "$GH_LOG.state"
  # FAKE_GH_PR_LIST_1 is unset, so the fake finds no PR.
  run "$RUN_CMD"
  # With no PR reconcile skips, and discoverIssues finds no ready-for-agent
  # issue, so the launcher exits 2 on an empty queue.
  [ "$status" -eq 2 ]
  ! grep -q 'pr merge' "$GH_LOG"
  ! grep -q 'agent-complete' "$GH_LOG"
  ! grep -q -- 'agent-failed' "$GH_LOG"
}

@test "recover: green PR is adopted and merged (via issue #195)" {
  export MERGE_MODE=immediate
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tStranded issue'
  printf '1\tagent-in-progress\n' >> "$GH_LOG.state"
  export FAKE_GH_PR_LIST_1="https://github.com/owner/repo/pull/1"
  # The gate (issue #1652) will not trust an immediate SUCCESS until a non-terminal
  # state proves this run's checks registered, so lead with PENDING. The bounded
  # poll stops a broken fake real-sleeping out the baked MERGE_POLL_TIMEOUT (3600s).
  export MERGE_POLL_INTERVAL=0
  export MERGE_POLL_TIMEOUT=100
  export FAKE_GH_GRAPHQL_ROLLUP_SEQ_1="PENDING,SUCCESS,SUCCESS"
  run "$SPINDRIFT_CMD" recover 1
  [ "$status" -eq 0 ]
  [[ "$output" == *"status=adopted"* ]]
  [[ "$output" == *"status=verified-merged"* ]]
  grep -q 'pr merge' "$GH_LOG"
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-complete --remove-label agent-in-progress' "$GH_LOG"
}

@test "recover: draft PR is adopted and merged (via issue #195)" {
  export MERGE_MODE=immediate
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tStranded issue'
  printf '1\tagent-in-progress\n' >> "$GH_LOG.state"
  export FAKE_GH_PR_LIST_1="https://github.com/owner/repo/pull/1"
  export FAKE_GH_PR_DRAFT_1="true"
  # The gate (issue #1652) will not trust an immediate SUCCESS until a non-terminal
  # state proves this run's checks registered, so lead with PENDING. The bounded
  # poll stops a broken fake real-sleeping out the baked MERGE_POLL_TIMEOUT (3600s).
  export MERGE_POLL_INTERVAL=0
  export MERGE_POLL_TIMEOUT=100
  export FAKE_GH_GRAPHQL_ROLLUP_SEQ_1="PENDING,SUCCESS,SUCCESS"
  run "$SPINDRIFT_CMD" recover 1
  [ "$status" -eq 0 ]
  [[ "$output" == *"status=adopted"* ]]
  [[ "$output" == *"status=verified-merged"* ]]
  grep -q 'pr ready https://github.com/owner/repo/pull/1' "$GH_LOG"
  grep -q 'pr merge' "$GH_LOG"
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-complete --remove-label agent-in-progress' "$GH_LOG"
  ! grep -q -- 'agent-failed' "$GH_LOG"
}

@test "recover: no open PR exits non-zero without label churn (via issue #195)" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tStranded issue'
  printf '1\tagent-in-progress\n' >> "$GH_LOG.state"
  # FAKE_GH_PR_LIST_1 is unset, so the fake finds no PR.
  run "$SPINDRIFT_CMD" recover 1
  [ "$status" -ne 0 ]
  [[ "$output" == *"status=skipped"* ]]
  ! grep -q 'pr merge' "$GH_LOG"
  ! grep -q 'agent-complete' "$GH_LOG"
  ! grep -q -- 'agent-failed' "$GH_LOG"
}

@test "recover: green PR is adopted and merged" {
  export MERGE_MODE=immediate
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tStranded issue'
  printf '1\tagent-in-progress\n' >> "$GH_LOG.state"
  export FAKE_GH_PR_LIST_1="https://github.com/owner/repo/pull/1"
  # The gate (issue #1652) will not trust an immediate SUCCESS until a non-terminal
  # state proves this run's checks registered, so lead with PENDING. The bounded
  # poll stops a broken fake real-sleeping out the baked MERGE_POLL_TIMEOUT (3600s).
  export MERGE_POLL_INTERVAL=0
  export MERGE_POLL_TIMEOUT=100
  export FAKE_GH_GRAPHQL_ROLLUP_SEQ_1="PENDING,SUCCESS,SUCCESS"
  run "$SPINDRIFT_CMD" recover 1
  [ "$status" -eq 0 ]
  [[ "$output" == *"status=adopted"* ]]
  [[ "$output" == *"status=verified-merged"* ]]
  grep -q 'pr merge' "$GH_LOG"
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-complete --remove-label agent-in-progress' "$GH_LOG"
}

@test "recover: settled-green PR (never re-registers) is adopted and merged" {
  # issue #2475: a PR whose checks settled to SUCCESS before this run started
  # watching used to burn the full MERGE_POLL_TIMEOUT waiting for the
  # non-terminal state issue #1652's guard demands, then park the issue
  # agent-failed. gateToGreen now bounds that wait to registrationWindowPolls,
  # so an always-SUCCESS rollup reaches green once the window elapses.
  export MERGE_MODE=immediate
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tStranded issue'
  printf '1\tagent-in-progress\n' >> "$GH_LOG.state"
  export FAKE_GH_PR_LIST_1="https://github.com/owner/repo/pull/1"
  export MERGE_POLL_INTERVAL=0
  export MERGE_POLL_TIMEOUT=100
  export FAKE_GH_GRAPHQL_ROLLUP_SEQ_1="SUCCESS"
  run "$SPINDRIFT_CMD" recover 1
  [ "$status" -eq 0 ]
  [[ "$output" == *"status=adopted"* ]]
  [[ "$output" == *"status=verified-merged"* ]]
  grep -q 'pr merge' "$GH_LOG"
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-complete --remove-label agent-in-progress' "$GH_LOG"
  ! grep -q -- 'agent-failed' "$GH_LOG"
}

@test "recover: draft PR that never re-registers is adopted, readied, and merged" {
  # issue #2475 AC5: the only case pairing the all-SUCCESS (never re-registers)
  # sequence with a draft PR, so it is the one test proving gateToGreen's
  # registration-window bound still holds when recover must first flip the PR
  # out of draft with `pr ready`.
  export MERGE_MODE=immediate
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tStranded issue'
  printf '1\tagent-in-progress\n' >> "$GH_LOG.state"
  export FAKE_GH_PR_LIST_1="https://github.com/owner/repo/pull/1"
  export FAKE_GH_PR_DRAFT_1="true"
  export MERGE_POLL_INTERVAL=0
  export MERGE_POLL_TIMEOUT=100
  export FAKE_GH_GRAPHQL_ROLLUP_SEQ_1="SUCCESS"
  run "$SPINDRIFT_CMD" recover 1
  [ "$status" -eq 0 ]
  [[ "$output" == *"status=adopted"* ]]
  [[ "$output" == *"status=verified-merged"* ]]
  grep -q 'pr ready https://github.com/owner/repo/pull/1' "$GH_LOG"
  grep -q 'pr merge' "$GH_LOG"
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-complete --remove-label agent-in-progress' "$GH_LOG"
  ! grep -q -- 'agent-failed' "$GH_LOG"
}

@test "recover: draft PR is adopted and merged" {
  export MERGE_MODE=immediate
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tStranded issue'
  printf '1\tagent-in-progress\n' >> "$GH_LOG.state"
  export FAKE_GH_PR_LIST_1="https://github.com/owner/repo/pull/1"
  export FAKE_GH_PR_DRAFT_1="true"
  # The gate (issue #1652) will not trust an immediate SUCCESS until a non-terminal
  # state proves this run's checks registered, so lead with PENDING. The bounded
  # poll stops a broken fake real-sleeping out the baked MERGE_POLL_TIMEOUT (3600s).
  export MERGE_POLL_INTERVAL=0
  export MERGE_POLL_TIMEOUT=100
  export FAKE_GH_GRAPHQL_ROLLUP_SEQ_1="PENDING,SUCCESS,SUCCESS"
  run "$SPINDRIFT_CMD" recover 1
  [ "$status" -eq 0 ]
  [[ "$output" == *"status=adopted"* ]]
  [[ "$output" == *"status=verified-merged"* ]]
  grep -q 'pr ready https://github.com/owner/repo/pull/1' "$GH_LOG"
  grep -q 'pr merge' "$GH_LOG"
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-complete --remove-label agent-in-progress' "$GH_LOG"
  ! grep -q -- 'agent-failed' "$GH_LOG"
}

@test "recover: no open PR exits non-zero without label churn" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tStranded issue'
  printf '1\tagent-in-progress\n' >> "$GH_LOG.state"
  # FAKE_GH_PR_LIST_1 is unset, so the fake finds no PR.
  run "$SPINDRIFT_CMD" recover 1
  [ "$status" -ne 0 ]
  [[ "$output" == *"status=skipped"* ]]
  ! grep -q 'pr merge' "$GH_LOG"
  ! grep -q 'agent-complete' "$GH_LOG"
  ! grep -q -- 'agent-failed' "$GH_LOG"
}

