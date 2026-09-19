#!/usr/bin/env bats
# Launcher merge gate (issue #135): rollup states, self-heal fix passes, merge-conflict rebase retry.

load helper

setup() {
  setup_run_env
}

@test "rollup SUCCESS → merges PR and reports verified-merged" {
  export MERGE_MODE=immediate
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tFirst issue'
  export FAKE_PODMAN_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 landing=https://github.com/owner/repo/pull/1 status=ready note=ci-pending"
  export FAKE_GH_GRAPHQL_ROLLUP_1="SUCCESS"
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q 'pr merge' "$GH_LOG"
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-complete --remove-label agent-in-progress' "$GH_LOG"
  [[ "$output" == *"status=verified-merged"* ]]
}

@test "rollup FAILURE → does NOT merge, swaps to agent-failed" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tFirst issue'
  export FAKE_PODMAN_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 landing=https://github.com/owner/repo/pull/1 status=ready note=ci-pending"
  export FAKE_GH_GRAPHQL_ROLLUP_1="FAILURE"
  export MAX_FIX_ATTEMPTS=0  # Bare gate test: self-heal disabled.
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  ! grep -q 'pr merge' "$GH_LOG"
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-failed --remove-label agent-in-progress' "$GH_LOG"
  [[ "$output" == *"status=failed"* ]]
}

# R1 regression guard: ERROR state (e.g. cancelled run) must not trigger a merge.
@test "rollup ERROR → does NOT merge, swaps to agent-failed" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tFirst issue'
  export FAKE_PODMAN_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 landing=https://github.com/owner/repo/pull/1 status=ready note=ci-pending"
  export FAKE_GH_GRAPHQL_ROLLUP_1="ERROR"
  export MAX_FIX_ATTEMPTS=0  # Bare gate test: self-heal disabled.
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  ! grep -q 'pr merge' "$GH_LOG"
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-failed --remove-label agent-in-progress' "$GH_LOG"
  [[ "$output" == *"status=failed"* ]]
}

@test "rollup PENDING (timeout) → does NOT merge, swaps to agent-failed" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tFirst issue'
  export FAKE_PODMAN_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 landing=https://github.com/owner/repo/pull/1 status=ready note=ci-pending"
  export FAKE_GH_GRAPHQL_ROLLUP_1="PENDING"
  export MERGE_POLL_INTERVAL=0
  export MERGE_POLL_TIMEOUT=0
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  ! grep -q 'pr merge' "$GH_LOG"
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-failed --remove-label agent-in-progress' "$GH_LOG"
  [[ "$output" == *"status=failed"* ]]
}

@test "rollup null/no checks (timeout) → does NOT merge, swaps to agent-failed" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tFirst issue'
  export FAKE_PODMAN_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 landing=https://github.com/owner/repo/pull/1 status=ready note=ci-pending"
  # FAKE_GH_GRAPHQL_ROLLUP_1 is left unset, so the rollup is empty: no checks
  # registered yet.
  export MERGE_POLL_INTERVAL=0
  export MERGE_POLL_TIMEOUT=0
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  ! grep -q 'pr merge' "$GH_LOG"
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-failed --remove-label agent-in-progress' "$GH_LOG"
  [[ "$output" == *"status=failed"* ]]
}

@test "rollup PENDING then SUCCESS → waits and eventually merges" {
  export MERGE_MODE=immediate
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tFirst issue'
  export FAKE_PODMAN_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 landing=https://github.com/owner/repo/pull/1 status=ready note=ci-pending"
  export FAKE_GH_GRAPHQL_ROLLUP_SEQ_1="PENDING,SUCCESS"
  export MERGE_POLL_INTERVAL=0
  export MERGE_POLL_TIMEOUT=3
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q 'pr merge' "$GH_LOG"
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-complete --remove-label agent-in-progress' "$GH_LOG"
  [[ "$output" == *"status=verified-merged"* ]]
}

# Sequence exhaustion (issue #2650): a SEQ shorter than the number of polls must
# stick on its last entry instead of running off the end. This SEQ has one entry
# and MERGE_POLL_TIMEOUT=4 needs 5 calls before the deadline fires (elapsed
# increments by 1 per call when MERGE_POLL_INTERVAL=0, see
# cmd/launcher/internal/settle/watch.go), so calls 2-5 read past that entry.
@test "rollup PENDING sequence exhausted before timeout → sticks on last entry, times out cleanly" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tFirst issue'
  export FAKE_PODMAN_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 landing=https://github.com/owner/repo/pull/1 status=ready note=ci-pending"
  export FAKE_GH_GRAPHQL_ROLLUP_SEQ_1="PENDING"
  export MERGE_POLL_INTERVAL=0
  export MERGE_POLL_TIMEOUT=4
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  ! grep -q 'pr merge' "$GH_LOG"
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-failed --remove-label agent-in-progress' "$GH_LOG"
  [[ "$output" == *"status=failed"* ]]
  [[ "$output" == *"ci-timeout:"* ]]
}

# AC #1 (issue #130): a late-registered check appears PENDING on the
# confirmation re-poll. The initial SUCCESS snapshot alone is not enough.
@test "late-registered check: SUCCESS then PENDING confirmation → defers, eventually merges" {
  export MERGE_MODE=immediate
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tFirst issue'
  export FAKE_PODMAN_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 landing=https://github.com/owner/repo/pull/1 status=ready note=ci-pending"
  # Seq entries are the initial poll, the confirmation re-poll (a late job has
  # registered), and the next poll, which sticks.
  export FAKE_GH_GRAPHQL_ROLLUP_SEQ_1="SUCCESS,PENDING,SUCCESS"
  export MERGE_POLL_INTERVAL=0
  # The timeout must exceed the deferral iteration count, 2 here: elapsed
  # increments by 1 per iteration when MERGE_POLL_INTERVAL=0.
  export MERGE_POLL_TIMEOUT=3
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q 'pr merge' "$GH_LOG"
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-complete --remove-label agent-in-progress' "$GH_LOG"
  [[ "$output" == *"status=verified-merged"* ]]
}

# AC #2 (issue #130): a late-registered job turns red after the initial
# all-green snapshot, so the confirmation re-poll sees FAILURE.
@test "late-registered check fails after SUCCESS snapshot → no merge, agent-failed" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tFirst issue'
  export FAKE_PODMAN_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 landing=https://github.com/owner/repo/pull/1 status=ready note=ci-pending"
  # Seq entries are the initial poll and the confirmation re-poll, by which
  # time the late job has registered and is already red.
  export FAKE_GH_GRAPHQL_ROLLUP_SEQ_1="SUCCESS,FAILURE"
  export MERGE_POLL_INTERVAL=0
  export MERGE_POLL_TIMEOUT=100
  export MAX_FIX_ATTEMPTS=0  # Bare gate test: self-heal disabled.
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  ! grep -q 'pr merge' "$GH_LOG"
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-failed --remove-label agent-in-progress' "$GH_LOG"
  [[ "$output" == *"status=failed"* ]]
}

# Self-heal fix-agent (issue #136).

@test "self-heal: red-then-green → dispatches fix box and merges" {
  export MERGE_MODE=immediate
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tFirst issue'
  export FAKE_PODMAN_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 landing=https://github.com/owner/repo/pull/1 status=ready note=ci-pending"
  # The first FAILURE triggers the fix box.
  export FAKE_GH_GRAPHQL_ROLLUP_SEQ_1="FAILURE,SUCCESS"
  export MERGE_POLL_INTERVAL=0
  export MERGE_POLL_TIMEOUT=100
  export MAX_FIX_ATTEMPTS=3
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  # Two container runs: the initial box plus one fix box.
  [ "$(grep -c '^run ' "$PODMAN_LOG")" -eq 2 ]
  grep -q 'pr merge' "$GH_LOG"
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-complete --remove-label agent-in-progress' "$GH_LOG"
  [[ "$output" == *"status=verified-merged"* ]]
}

@test "self-heal: red-through-cap → exhausts passes and marks agent-failed" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tFirst issue'
  export FAKE_PODMAN_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 landing=https://github.com/owner/repo/pull/1 status=ready note=ci-pending"
  export FAKE_GH_GRAPHQL_ROLLUP_1="FAILURE"
  export MERGE_POLL_INTERVAL=0
  export MERGE_POLL_TIMEOUT=100
  export MAX_FIX_ATTEMPTS=3
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  # One initial run plus three fix passes is four.
  [ "$(grep -c '^run ' "$PODMAN_LOG")" -eq 4 ]
  ! grep -q 'pr merge' "$GH_LOG"
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-failed --remove-label agent-in-progress' "$GH_LOG"
  [[ "$output" == *"status=fix-exhausted"* ]]
  [[ "$output" == *"status=failed"* ]]
}

# Merge-conflict rebase retry (issue #194).

@test "merge gate: conflict → rebase → retried merge → agent-complete" {
  export MERGE_MODE=immediate
  # Set up a real local git remote so gh repo clone (which calls real git) can
  # clone from a local file URL rewritten by the insteadOf config.
  setup_bare_repo
  # Push the PR head branch so git checkout agent/issue-1 resolves after clone.
  local seed="$BATS_TEST_TMPDIR/seed-pr"
  git clone -q "https://github.com/owner/repo.git" "$seed"
  git -C "$seed" checkout -q -b "agent/issue-1"
  git -C "$seed" push -q origin HEAD:agent/issue-1

  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tFirst issue'
  export FAKE_PODMAN_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 landing=https://github.com/owner/repo/pull/1 status=ready note=ci-pending"
  # CI returns SUCCESS twice: once before the merge attempt, once after rebase.
  export FAKE_GH_GRAPHQL_ROLLUP_SEQ_1="SUCCESS,SUCCESS"
  # First merge call fails with conflict; second succeeds.
  export FAKE_GH_PR_MERGE_CONFLICT_1=1
  export FAKE_GH_PR_HEAD_1="agent/issue-1"
  export FAKE_GH_PR_BASE_1="main"
  export FAKE_GH_REPO_CLONE_GIT=1
  export MERGE_POLL_INTERVAL=0
  export MERGE_POLL_TIMEOUT=100
  export MAX_REBASE_ATTEMPTS=3
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q 'pr merge' "$GH_LOG"
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-complete --remove-label agent-in-progress' "$GH_LOG"
  [[ "$output" == *"status=verified-merged"* ]]
}

@test "merge gate: conflict → rebase fails → merge-blocked (stays agent-complete)" {
  export MERGE_MODE=immediate
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tFirst issue'
  export FAKE_PODMAN_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 landing=https://github.com/owner/repo/pull/1 status=ready note=ci-pending"
  export FAKE_GH_GRAPHQL_ROLLUP_1="SUCCESS"
  export FAKE_GH_PR_MERGE_CONFLICT_1=99  # Every merge call fails with a conflict.
  # No real git remote is configured here, so gh repo clone is a no-op, the
  # following git checkout fails, and Rebase returns an error.
  export MERGE_POLL_INTERVAL=0
  export MERGE_POLL_TIMEOUT=100
  export MAX_REBASE_ATTEMPTS=3
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-complete --remove-label agent-in-progress' "$GH_LOG"
  ! grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-failed' "$GH_LOG"
  [[ "$output" == *"status=merge-blocked"* ]]
  # Launcher must log the rebase-retry attempt before blocking.
  [[ "$output" == *"status=rebase-retry"* ]]
}

# Guard (issue #2424): setup_run_env's default poll interval and timeout must
# bound the merge gate even when a test sets neither, so this test sets neither
# and reaches the poll loop with a PENDING rollup. The outer `timeout 5` catches
# a regression back to the launcher's production default of 3600s: that kills
# the launcher at status 124 with no launcher-authored "ci-timeout:" message.
@test "no explicit poll override → setup_run_env default still bounds the gate" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tFirst issue'
  export FAKE_PODMAN_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 landing=https://github.com/owner/repo/pull/1 status=ready note=ci-pending"
  export FAKE_GH_GRAPHQL_ROLLUP_1="PENDING"
  run timeout 5 "$RUN_CMD"
  [ "$status" -eq 0 ]
  ! grep -q 'pr merge' "$GH_LOG"
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-failed --remove-label agent-in-progress' "$GH_LOG"
  [[ "$output" == *"status=failed"* ]]
  [[ "$output" == *"ci-timeout:"* ]]
}

@test "self-heal: pending timeout does not consume fix passes" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tFirst issue'
  export FAKE_PODMAN_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 landing=https://github.com/owner/repo/pull/1 status=ready note=ci-pending"
  export FAKE_GH_GRAPHQL_ROLLUP_1="PENDING"
  export MERGE_POLL_INTERVAL=0
  export MERGE_POLL_TIMEOUT=0
  export MAX_FIX_ATTEMPTS=3
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  # One container run, the initial box: no fix passes dispatched.
  [ "$(grep -c '^run ' "$PODMAN_LOG")" -eq 1 ]
  ! grep -q 'pr merge' "$GH_LOG"
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-failed --remove-label agent-in-progress' "$GH_LOG"
  [[ "$output" == *"status=failed"* ]]
}

