#!/usr/bin/env bats
# Dependency-wave ordering (issue #39): blocker detection, cycles, deadlock, wave dispatch order.

load helper

setup() {
  setup_run_env
}

# --- Dependency-wave ordering (issue #39) ----------------------------------

@test "run dispatches an issue whose external blocker has a merged PR" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'2\tDependent'
  export FAKE_GH_ISSUE_BODY_2="Depends on #1"
  export FAKE_GH_PR_LIST_1="https://github.com/owner/repo/pull/1"
  export FAKE_GH_PR_STATE_1="MERGED"
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q 'ISSUE_NUMBER=2' "$PODMAN_LOG"
}

@test "run parses 'blocked by' syntax as a blocker reference" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'2\tDependent'
  export FAKE_GH_ISSUE_BODY_2="blocked by #1"
  export FAKE_GH_PR_LIST_1="https://github.com/owner/repo/pull/1"
  export FAKE_GH_PR_STATE_1="MERGED"
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q 'ISSUE_NUMBER=2' "$PODMAN_LOG"
}

@test "run preserves single-wave behaviour when no dependencies are declared" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^run ' "$PODMAN_LOG")" -eq 2 ]
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

@test "run exits 3 immediately when a blocker never completes (no in-process wait)" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tDependent'
  export FAKE_GH_ISSUE_BODY_1="depends on #99"
  run "$RUN_CMD"
  [ "$status" -eq 3 ]
  [[ "$output" == *"remain blocked or deferred"* ]]
}

@test "closed unlabeled blocker unblocks its dependent (treated as satisfied)" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'2\tDependent'
  export FAKE_GH_ISSUE_BODY_2="depends on #1"
  # Blocker #1 is CLOSED but never received the complete label.
  export FAKE_GH_ISSUE_STATE_1="CLOSED"
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q 'ISSUE_NUMBER=2' "$PODMAN_LOG"
  [[ "$output" == *"no discoverable PR"* ]]
}

@test "open unlabeled blocker keeps dependent blocked and exits 3" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'2\tDependent'
  export FAKE_GH_ISSUE_BODY_2="depends on #1"
  # Blocker #1 is OPEN (default) with no complete label — permanently unready.
  run "$RUN_CMD"
  [ "$status" -eq 3 ]
  [[ "$output" == *"remain blocked or deferred"* ]]
  ! grep -q 'ISSUE_NUMBER=2' "$PODMAN_LOG"
}

@test "run dispatches the blocker, then the dependent on the next invocation (ADR 0019)" {
  export MERGE_MODE=immediate
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tBlocker\n2\tDependent'
  export FAKE_GH_ISSUE_BODY_2="depends on #1"
  # Issue 1 box writes a ready outcome; the launcher gates it (CI SUCCESS →
  # merge → agent-complete). No FAKE_PODMAN_AUTO_COMPLETE shortcut.
  export FAKE_PODMAN_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 pr=https://github.com/owner/repo/pull/1 status=ready note=ok"
  export FAKE_GH_GRAPHQL_ROLLUP_1="SUCCESS"
  export GH_STATE="$GH_LOG.state"
  # PRForBranch needs this to find the PR URL; gh pr merge then writes MERGED
  # to GH_STATE, which PRState reads to satisfy blockerReady on the next
  # invocation.
  export FAKE_GH_PR_LIST_1="https://github.com/owner/repo/pull/1"
  # Pre-seed issue #1 as ready-for-agent so reconcileStranded does not adopt
  # its PR before dispatch. Without this, the fake matches any label query for
  # issues with no recorded state, and FAKE_GH_PR_LIST_1 causes reconcile to
  # adopt+merge #1, removing it from the ready queue before it is dispatched.
  printf '1\tready-for-agent\n' > "$GH_LOG.state"

  # First invocation: only the unblocked blocker dispatches in this wave; the
  # dependent stays held on the label rather than looping an in-process
  # second wave from the same (now-stale) image (ADR 0019 / issue #477).
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q 'ISSUE_NUMBER=1' "$PODMAN_LOG"
  ! grep -q 'ISSUE_NUMBER=2' "$PODMAN_LOG"
  [[ "$output" == *"1 issue(s) remain for a later invocation"* ]]

  # #1 reached agent-complete during settle; a fresh invocation (which
  # re-evaluates the flake image in production) now dispatches the dependent.
  : >"$PODMAN_LOG"
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q 'ISSUE_NUMBER=2' "$PODMAN_LOG"
}

