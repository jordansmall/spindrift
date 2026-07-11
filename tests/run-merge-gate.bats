#!/usr/bin/env bats
# Launcher merge gate (issue #135): rollup states, self-heal fix passes, merge-conflict rebase retry.

load helper

setup() {
  setup_run_env
}

# --- Launcher merge gate (issue #135) ----------------------------------------

@test "rollup SUCCESS → merges PR and reports verified-merged" {
  export MERGE_MODE=immediate
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tFirst issue'
  export FAKE_PODMAN_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 pr=https://github.com/owner/repo/pull/1 status=ready note=ci-pending"
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
  export FAKE_PODMAN_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 pr=https://github.com/owner/repo/pull/1 status=ready note=ci-pending"
  export FAKE_GH_GRAPHQL_ROLLUP_1="FAILURE"
  export MAX_FIX_ATTEMPTS=0  # bare gate test — self-heal disabled
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
  export FAKE_PODMAN_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 pr=https://github.com/owner/repo/pull/1 status=ready note=ci-pending"
  export FAKE_GH_GRAPHQL_ROLLUP_1="ERROR"
  export MAX_FIX_ATTEMPTS=0  # bare gate test — self-heal disabled
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  ! grep -q 'pr merge' "$GH_LOG"
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-failed --remove-label agent-in-progress' "$GH_LOG"
  [[ "$output" == *"status=failed"* ]]
}

@test "rollup PENDING (timeout) → does NOT merge, swaps to agent-failed" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tFirst issue'
  export FAKE_PODMAN_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 pr=https://github.com/owner/repo/pull/1 status=ready note=ci-pending"
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
  export FAKE_PODMAN_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 pr=https://github.com/owner/repo/pull/1 status=ready note=ci-pending"
  # FAKE_GH_GRAPHQL_ROLLUP_1 unset → empty rollup (no checks registered yet)
  export MERGE_POLL_INTERVAL=0
  export MERGE_POLL_TIMEOUT=0
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  ! grep -q 'pr merge' "$GH_LOG"
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-failed --remove-label agent-in-progress' "$GH_LOG"
  [[ "$output" == *"status=failed"* ]]
}

# PENDING-then-SUCCESS: gate waits through one pending poll then merges.
@test "rollup PENDING then SUCCESS → waits and eventually merges" {
  export MERGE_MODE=immediate
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tFirst issue'
  export FAKE_PODMAN_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 pr=https://github.com/owner/repo/pull/1 status=ready note=ci-pending"
  export FAKE_GH_GRAPHQL_ROLLUP_SEQ_1="PENDING,SUCCESS"
  export MERGE_POLL_INTERVAL=0
  export MERGE_POLL_TIMEOUT=3
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q 'pr merge' "$GH_LOG"
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-complete --remove-label agent-in-progress' "$GH_LOG"
  [[ "$output" == *"status=verified-merged"* ]]
}

# AC #1 (issue #130): a late-registered check appears PENDING on the
# confirmation re-poll; gate keeps waiting and merges once the full set
# is green.  The initial SUCCESS snapshot alone is not sufficient.
@test "late-registered check: SUCCESS then PENDING confirmation → defers, eventually merges" {
  export MERGE_MODE=immediate
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tFirst issue'
  export FAKE_PODMAN_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 pr=https://github.com/owner/repo/pull/1 status=ready note=ci-pending"
  # seq: initial=SUCCESS, confirmation=PENDING (late job registered), next poll=SUCCESS (sticks)
  export FAKE_GH_GRAPHQL_ROLLUP_SEQ_1="SUCCESS,PENDING,SUCCESS"
  export MERGE_POLL_INTERVAL=0
  # Timeout must exceed the deferral iteration count (2 loops here): elapsed
  # increments by 1 per iteration when MERGE_POLL_INTERVAL=0, so ≥2 suffices.
  export MERGE_POLL_TIMEOUT=3
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q 'pr merge' "$GH_LOG"
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-complete --remove-label agent-in-progress' "$GH_LOG"
  [[ "$output" == *"status=verified-merged"* ]]
}

# AC #2 (issue #130): a late-registered job fails after an initial all-green
# snapshot.  Confirmation re-poll sees FAILURE → no merge, agent-failed.
@test "late-registered check fails after SUCCESS snapshot → no merge, agent-failed" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tFirst issue'
  export FAKE_PODMAN_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 pr=https://github.com/owner/repo/pull/1 status=ready note=ci-pending"
  # seq: initial=SUCCESS, confirmation=FAILURE (late job registered and already red)
  export FAKE_GH_GRAPHQL_ROLLUP_SEQ_1="SUCCESS,FAILURE"
  export MERGE_POLL_INTERVAL=0
  export MERGE_POLL_TIMEOUT=100
  export MAX_FIX_ATTEMPTS=0  # bare gate test — self-heal disabled
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  ! grep -q 'pr merge' "$GH_LOG"
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-failed --remove-label agent-in-progress' "$GH_LOG"
  [[ "$output" == *"status=failed"* ]]
}

# --- Self-heal fix-agent (issue #136) -----------------------------------------

# Red-then-green: launcher dispatches one fix box, CI turns green, PR merges.
@test "self-heal: red-then-green → dispatches fix box and merges" {
  export MERGE_MODE=immediate
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tFirst issue'
  export FAKE_PODMAN_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 pr=https://github.com/owner/repo/pull/1 status=ready note=ci-pending"
  # First GraphQL call returns FAILURE (triggers fix box); second returns SUCCESS.
  export FAKE_GH_GRAPHQL_ROLLUP_SEQ_1="FAILURE,SUCCESS"
  export MERGE_POLL_INTERVAL=0
  export MERGE_POLL_TIMEOUT=100
  export MAX_FIX_ATTEMPTS=3
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  # Exactly 2 container runs: initial box + 1 fix box.
  [ "$(grep -c '^run ' "$PODMAN_LOG")" -eq 2 ]
  grep -q 'pr merge' "$GH_LOG"
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-complete --remove-label agent-in-progress' "$GH_LOG"
  [[ "$output" == *"status=verified-merged"* ]]
}

# Red-through-cap: all fix passes fail, issue is marked agent-failed.
@test "self-heal: red-through-cap → exhausts passes and marks agent-failed" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tFirst issue'
  export FAKE_PODMAN_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 pr=https://github.com/owner/repo/pull/1 status=ready note=ci-pending"
  # CI is always FAILURE — never recovers.
  export FAKE_GH_GRAPHQL_ROLLUP_1="FAILURE"
  export MERGE_POLL_INTERVAL=0
  export MERGE_POLL_TIMEOUT=100
  export MAX_FIX_ATTEMPTS=3
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  # 1 initial run + 3 fix passes = 4 total.
  [ "$(grep -c '^run ' "$PODMAN_LOG")" -eq 4 ]
  ! grep -q 'pr merge' "$GH_LOG"
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-failed --remove-label agent-in-progress' "$GH_LOG"
  [[ "$output" == *"status=fix-exhausted"* ]]
  [[ "$output" == *"status=failed"* ]]
}

# --- Merge-conflict rebase retry (issue #194) ---------------------------------

# conflict→rebase→merge: merge fails with conflict, rebase resolves cleanly via
# a local bare repo (setup_bare_repo + insteadOf URL rewrite), second merge
# attempt succeeds → issue reaches agent-complete.
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
  export FAKE_PODMAN_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 pr=https://github.com/owner/repo/pull/1 status=ready note=ci-pending"
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

# conflict→rebase-fails→merge-blocked: merge fails with conflict; the rebase
# fails (no git repo in the clone dir because gh repo clone is a no-op stub
# here) → launcher leaves the issue at agent-complete with a merge-blocked note
# rather than demoting it to agent-failed.
@test "merge gate: conflict → rebase fails → merge-blocked (stays agent-complete)" {
  export MERGE_MODE=immediate
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tFirst issue'
  export FAKE_PODMAN_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 pr=https://github.com/owner/repo/pull/1 status=ready note=ci-pending"
  export FAKE_GH_GRAPHQL_ROLLUP_1="SUCCESS"
  export FAKE_GH_PR_MERGE_CONFLICT_1=99  # all merge calls fail with conflict
  # gh repo clone is a no-op here (no real git remote configured), so the
  # subsequent git checkout fails → Rebase returns an error → merge-blocked.
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

# Pending-timeout: no fix passes consumed, gate timeout marks agent-failed.
@test "self-heal: pending timeout does not consume fix passes" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tFirst issue'
  export FAKE_PODMAN_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 pr=https://github.com/owner/repo/pull/1 status=ready note=ci-pending"
  export FAKE_GH_GRAPHQL_ROLLUP_1="PENDING"
  export MERGE_POLL_INTERVAL=0
  export MERGE_POLL_TIMEOUT=0
  export MAX_FIX_ATTEMPTS=3
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  # Only 1 container run (the initial box); no fix passes dispatched.
  [ "$(grep -c '^run ' "$PODMAN_LOG")" -eq 1 ]
  ! grep -q 'pr merge' "$GH_LOG"
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-failed --remove-label agent-in-progress' "$GH_LOG"
  [[ "$output" == *"status=failed"* ]]
}

