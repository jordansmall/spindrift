#!/usr/bin/env bats
# Behaviour of the nix-generated `run` command (fans out one Box per issue).

load helper

setup() {
  setup_fakes
  set_run_env
  cd "$BATS_TEST_TMPDIR"
  export FAKE_GH_ISSUES=$'1\tFirst issue\n2\tSecond issue'
  # Fail fast on future predicate mismatches rather than blocking CI for 2h.
  export DEPS_WAIT_SECS=10
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

@test "run falls back to container build when host cannot realise the image" {
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
  # IMAGE_PATH is /nix/store/<32-char-hash>-spindrift; extract the hash
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

@test "run fans out one container per issue" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^run ' "$PODMAN_LOG")" -eq 2 ]
  grep -q 'ISSUE_NUMBER=1' "$PODMAN_LOG"
  grep -q 'ISSUE_NUMBER=2' "$PODMAN_LOG"
}

@test "run reaps a stale same-named container before launching (interrupted prior run)" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tOnly issue'
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q -- 'rm -f agent-issue-1' "$PODMAN_LOG"
  # The reap must precede the run, or the --name collision still fires.
  rm_line="$(grep -n -- 'rm -f agent-issue-1' "$PODMAN_LOG" | head -1 | cut -d: -f1)"
  run_line="$(grep -n '^run .*ISSUE_NUMBER=1' "$PODMAN_LOG" | head -1 | cut -d: -f1)"
  [ "$rm_line" -lt "$run_line" ]
}

@test "run skips stale-reap for a running container (concurrent invocation is safe)" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tOnly issue'
  # Declare agent-issue-1 as running (a concurrent invocation owns it).
  export FAKE_PODMAN_CONTAINER_STATE_agent_issue_1="running"
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  # rm -f must NOT be issued for a live container.
  ! grep -q -- 'rm -f agent-issue-1' "$PODMAN_LOG"
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
  grep -q 'MODEL=claude-sonnet-4-6' "$PODMAN_LOG"
}

@test "run passes the baked default SCOUT_MODEL and REVIEW_MODEL into the container" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q 'SCOUT_MODEL=claude-haiku-4-5-20251001' "$PODMAN_LOG"
  grep -q 'REVIEW_MODEL=claude-opus-4-8' "$PODMAN_LOG"
}

@test "MODEL env overrides the baked default into the container" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export MODEL=claude-opus-4-8
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q 'MODEL=claude-opus-4-8' "$PODMAN_LOG"
  ! grep -q 'MODEL=claude-sonnet-4-6' "$PODMAN_LOG"
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
  export FAKE_DOCKER_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 pr=https://github.com/owner/repo/pull/1 status=merged note=ok"
  export FAKE_GH_PR_STATE_1="MERGED"
  export FAKE_GH_ISSUE_LABELS_1="agent-complete"
  run "$DOCKER_RUN_CMD"
  [ "$status" -eq 0 ]
  [[ "$output" == *"#1"* ]]
  [[ "$output" == *"status=verified-merged"* ]]
}

@test "run invokes the baked entrypoint and baked prompt (no prompt mount)" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  ! grep -q 'entrypoint.sh:/agent' "$PODMAN_LOG"
  grep -q '/agent/entrypoint.sh' "$PODMAN_LOG"
  ! grep -q ':/agent/prompts' "$PODMAN_LOG"
}

@test "run exits 2 when there are no matching issues" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=""
  run "$RUN_CMD"
  [ "$status" -eq 2 ]
  [[ "$output" == *"nothing to do"* ]]
  [ "$(grep -c '^run ' "$PODMAN_LOG")" -eq 0 ]
}

@test "run includes security hardening flags in container argv" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tOnly issue'
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q -- '--cap-drop=all' "$PODMAN_LOG"
  grep -q -- '--security-opt=no-new-privileges' "$PODMAN_LOG"
  grep -q -- '--pids-limit=' "$PODMAN_LOG"
  grep -q -- '--memory=' "$PODMAN_LOG"
}

@test "PIDS_LIMIT and MEMORY_LIMIT override the baked defaults" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tOnly issue'
  export PIDS_LIMIT=256
  export MEMORY_LIMIT=2g
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q -- '--pids-limit=256' "$PODMAN_LOG"
  grep -q -- '--memory=2g' "$PODMAN_LOG"
  ! grep -q -- '--pids-limit=512' "$PODMAN_LOG"
  ! grep -q -- '--memory=4g' "$PODMAN_LOG"
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
  # ready-for-agent query returns nothing → exits 2 (queue empty).
  run "$RUN_CMD"
  [ "$status" -eq 2 ]
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

@test "run surfaces a never-completing blocker instead of deadlocking" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tDependent'
  export FAKE_GH_ISSUE_BODY_1="depends on #99"
  export DEPS_WAIT_SECS=0
  run "$RUN_CMD"
  [ "$status" -ne 0 ]
  [[ "$output" == *"deadlock"* || "$output" == *"ERROR"* ]]
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

@test "open unlabeled blocker keeps dependent blocked and surfaces as deadlock" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'2\tDependent'
  export FAKE_GH_ISSUE_BODY_2="depends on #1"
  # Blocker #1 is OPEN (default) with no complete label — permanently unready.
  export DEPS_WAIT_SECS=0
  run "$RUN_CMD"
  [ "$status" -ne 0 ]
  [[ "$output" == *"deadlock"* || "$output" == *"ERROR"* ]]
  ! grep -q 'ISSUE_NUMBER=2' "$PODMAN_LOG"
}

@test "run dispatches the blocker before the dependent (wave ordering)" {
  export MERGE_MODE=immediate
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tBlocker\n2\tDependent'
  export FAKE_GH_ISSUE_BODY_2="depends on #1"
  # Issue 1 box writes a ready outcome; the launcher gates it (CI SUCCESS → merge →
  # agent-complete) before wave 2 starts. No FAKE_PODMAN_AUTO_COMPLETE shortcut.
  export FAKE_PODMAN_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 pr=https://github.com/owner/repo/pull/1 status=ready note=ok"
  export FAKE_GH_GRAPHQL_ROLLUP_1="SUCCESS"
  export GH_STATE="$GH_LOG.state"
  # PRForBranch needs this to find the PR URL; gh pr merge then writes MERGED to
  # GH_STATE, which PRState reads to satisfy blockerReady for wave 2.
  export FAKE_GH_PR_LIST_1="https://github.com/owner/repo/pull/1"
  # Pre-seed issue #1 as ready-for-agent so reconcileStranded does not adopt its
  # PR before wave 1 dispatch. Without this, the fake matches any label query for
  # issues with no recorded state, and FAKE_GH_PR_LIST_1 causes reconcile to
  # adopt+merge #1, removing it from the ready queue before it is dispatched.
  printf '1\tready-for-agent\n' > "$GH_LOG.state"
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q 'ISSUE_NUMBER=1' "$PODMAN_LOG"
  grep -q 'ISSUE_NUMBER=2' "$PODMAN_LOG"
  line1="$(grep -n 'ISSUE_NUMBER=1' "$PODMAN_LOG" | cut -d: -f1 | head -1)"
  line2="$(grep -n 'ISSUE_NUMBER=2' "$PODMAN_LOG" | cut -d: -f1 | head -1)"
  [ "$line1" -lt "$line2" ]
}

# --- MAX_JOBS batch cap (dogfood serial loop) ------------------------------

@test "MAX_JOBS=1 dispatches only the oldest ready issue" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export MAX_JOBS=1
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^run ' "$PODMAN_LOG")" -eq 1 ]
  grep -q 'ISSUE_NUMBER=1' "$PODMAN_LOG"
  ! grep -q 'ISSUE_NUMBER=2' "$PODMAN_LOG"
}

@test "MAX_JOBS=0 dispatches the whole batch (no limit)" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export MAX_JOBS=0
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^run ' "$PODMAN_LOG")" -eq 2 ]
}

# --- MAX_PARALLEL bounds clamp (issue #91) ------------------------------------

@test "MAX_PARALLEL=0 falls back to default and dispatches the whole batch" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export MAX_PARALLEL=0
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^run ' "$PODMAN_LOG")" -eq 2 ]
}

@test "MAX_PARALLEL=garbage falls back to default and dispatches the whole batch" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export MAX_PARALLEL=garbage
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^run ' "$PODMAN_LOG")" -eq 2 ]
}

# --- Issue query cap and oldest-first ordering (issue #96) -------------------

@test "full window of 100 issues emits a cap warning" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export MAX_JOBS=1
  ISSUES=""
  for i in $(seq 1 100); do
    ISSUES+="${i}"$'\t'"Issue ${i}"$'\n'
  done
  export FAKE_GH_ISSUES="$ISSUES"
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  [[ "$output" == *"WARNING"* ]]
  [[ "$output" == *"100"* ]]
}

# --- Outcome report (issue #41) --------------------------------------------

@test "outcome report lists every dispatched issue with number pr and status" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_PODMAN_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 pr=https://github.com/owner/repo/pull/1 status=merged note=ok"
  export FAKE_PODMAN_OUTCOME_2="SPINDRIFT_OUTCOME issue=2 pr=https://github.com/owner/repo/pull/2 status=merged note=ok"
  export FAKE_GH_PR_STATE_1="MERGED"
  export FAKE_GH_PR_STATE_2="MERGED"
  export FAKE_GH_ISSUE_LABELS_1="agent-complete"
  export FAKE_GH_ISSUE_LABELS_2="agent-complete"
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  [[ "$output" == *"#1"* ]]
  [[ "$output" == *"#2"* ]]
  [[ "$output" == *"status=verified-merged"* ]]
}

@test "outcome report flags blocked issue distinctly with its note" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tBlocker'
  export FAKE_PODMAN_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 pr=https://github.com/owner/repo/pull/1 status=blocked note=stalled"
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  [[ "$output" == *"!!"* ]]
  [[ "$output" == *"stalled"* ]]
}

@test "outcome report reports missing outcome line when log has none" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tOrphan'
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  [[ "$output" == *"missing"* ]]
}

@test "malformed outcome line renders as malformed; subsequent issue is verified independently" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  # Issue 1: outcome line present but missing required pr= and status= tokens.
  export FAKE_PODMAN_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 note=missing-required-tokens"
  # Issue 2: well-formed outcome already merged.
  export FAKE_PODMAN_OUTCOME_2="SPINDRIFT_OUTCOME issue=2 pr=https://github.com/owner/repo/pull/2 status=merged note=ok"
  export FAKE_GH_PR_STATE_2="MERGED"
  export FAKE_GH_ISSUE_LABELS_2="agent-complete"
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  [[ "$output" == *"#1"* ]]
  [[ "$output" == *"status=malformed"* ]]
  [[ "$output" == *"status=verified-merged"* ]]
}

# --- PR adoption when outcome line is absent (issue #122) --------------------

@test "missing outcome line + open non-draft PR → adopted and merged when CI passes" {
  export MERGE_MODE=immediate
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tFirst issue'
  # No FAKE_PODMAN_OUTCOME_1 → no SPINDRIFT_OUTCOME in log
  export FAKE_GH_PR_LIST_1="https://github.com/owner/repo/pull/1"
  # FAKE_GH_PR_DRAFT_1 not set → defaults to "false" (non-draft)
  export FAKE_GH_GRAPHQL_ROLLUP_1="SUCCESS"
  run "$RUN_CMD"
  # reconcileStranded adopts and merges the PR; discoverIssues then finds no
  # ready-for-agent issues → launcher exits 2 (queue empty).
  [ "$status" -eq 2 ]
  grep -q 'pr merge' "$GH_LOG"
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-complete --remove-label agent-in-progress' "$GH_LOG"
  [[ "$output" == *"status=adopted"* ]]
  [[ "$output" == *"status=verified-merged"* ]]
}

@test "missing outcome line + draft PR → not adopted, reported as blocked" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tFirst issue'
  # No FAKE_PODMAN_OUTCOME_1 → no SPINDRIFT_OUTCOME in log
  export FAKE_GH_PR_LIST_1="https://github.com/owner/repo/pull/1"
  export FAKE_GH_PR_DRAFT_1="true"
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  ! grep -q 'pr merge' "$GH_LOG"
  [[ "$output" == *"status=blocked"* ]]
  [[ "$output" != *"status=adopted"* ]]
}

@test "missing outcome line + no open PR → status=missing unchanged" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tFirst issue'
  # No FAKE_PODMAN_OUTCOME_1, no FAKE_GH_PR_LIST_1 → no PR found
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  [[ "$output" == *"status=missing"* ]]
  ! grep -q 'pr merge' "$GH_LOG"
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
  ! grep -q 'agent-failed' "$GH_LOG"
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
  ! grep -q 'agent-failed' "$GH_LOG"
}

# --- engage subcommand (issue #195) ------------------------------------------

@test "engage: green PR is adopted and merged" {
  export MERGE_MODE=immediate
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tStranded issue'
  printf '1\tagent-in-progress\n' >> "$GH_LOG.state"
  export FAKE_GH_PR_LIST_1="https://github.com/owner/repo/pull/1"
  export FAKE_GH_GRAPHQL_ROLLUP_1="SUCCESS"
  run "$SPINDRIFT_CMD" engage 1
  [ "$status" -eq 0 ]
  [[ "$output" == *"status=adopted"* ]]
  [[ "$output" == *"status=verified-merged"* ]]
  grep -q 'pr merge' "$GH_LOG"
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-complete --remove-label agent-in-progress' "$GH_LOG"
}

@test "engage: draft PR is skipped and exits non-zero" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tStranded issue'
  printf '1\tagent-in-progress\n' >> "$GH_LOG.state"
  export FAKE_GH_PR_LIST_1="https://github.com/owner/repo/pull/1"
  export FAKE_GH_PR_DRAFT_1="true"
  run "$SPINDRIFT_CMD" engage 1
  [ "$status" -ne 0 ]
  [[ "$output" == *"status=skipped"* ]]
  ! grep -q 'pr merge' "$GH_LOG"
  ! grep -q 'agent-complete' "$GH_LOG"
  ! grep -q 'agent-failed' "$GH_LOG"
}

@test "engage: no open PR exits non-zero without label churn" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tStranded issue'
  printf '1\tagent-in-progress\n' >> "$GH_LOG.state"
  # No FAKE_GH_PR_LIST_1 → no PR found
  run "$SPINDRIFT_CMD" engage 1
  [ "$status" -ne 0 ]
  [[ "$output" == *"status=skipped"* ]]
  ! grep -q 'pr merge' "$GH_LOG"
  ! grep -q 'agent-complete' "$GH_LOG"
  ! grep -q 'agent-failed' "$GH_LOG"
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
  ! grep -q 'agent-failed' "$GH_LOG"
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
  ! grep -q 'agent-failed' "$GH_LOG"
}

@test "engage: deprecation notice printed to stderr then delegates to recover" {
  export MERGE_MODE=immediate
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tStranded issue'
  printf '1\tagent-in-progress\n' >> "$GH_LOG.state"
  export FAKE_GH_PR_LIST_1="https://github.com/owner/repo/pull/1"
  export FAKE_GH_GRAPHQL_ROLLUP_1="SUCCESS"
  run "$SPINDRIFT_CMD" engage 1
  [ "$status" -eq 0 ]
  [[ "$output" == *"status=adopted"* ]]
  [[ "$output" == *"status=verified-merged"* ]]
  [[ "$output" == *"deprecated"* ]]
}

# --- Outcome verification (issue #51) ----------------------------------------

@test "outcome report flags as failed when PR is not MERGED on GitHub" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tFirst issue'
  export FAKE_PODMAN_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 pr=https://github.com/owner/repo/pull/1 status=merged note=ok"
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
  export FAKE_PODMAN_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 pr=https://github.com/owner/repo/pull/1 status=merged note=ok"
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
  export FAKE_PODMAN_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 pr=https://github.com/owner/repo/pull/1 status=merged note=ok"
  export FAKE_GH_PR_STATE_1="MERGED"
  export FAKE_GH_ISSUE_LABELS_1="agent-complete"
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  [[ "$output" == *"status=verified-merged"* ]]
  ! grep -q -- 'agent-failed' "$GH_LOG"
}

# --- bwrap runner (issue #54) ------------------------------------------------

@test "runtime=bwrap fans out one bwrap invocation per issue" {
  run "$BWRAP_RUN_CMD"
  [ "$status" -eq 0 ]
  # Each issue must have its own per-issue log file so concurrent writes never
  # race on a shared sink; assert each dispatched issue produced a sandboxed
  # invocation independently.
  grep -q '^--ro-bind' "${BWRAP_LOG}.issue-1"
  grep -q 'ISSUE_NUMBER' "${BWRAP_LOG}.issue-1"
  grep -q '^--ro-bind' "${BWRAP_LOG}.issue-2"
  grep -q 'ISSUE_NUMBER' "${BWRAP_LOG}.issue-2"
  [ ! -s "$PODMAN_LOG" ]
}

@test "runtime=bwrap never loads an OCI image" {
  run "$BWRAP_RUN_CMD"
  [ "$status" -eq 0 ]
  ! grep -q 'load -i' "$BWRAP_LOG"
  ! grep -q 'load -i' "$PODMAN_LOG"
}

@test "runtime=bwrap passes non-secret env vars via --setenv" {
  run "$BWRAP_RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q 'REPO_SLUG' "$BWRAP_LOG"
  grep -q 'GIT_USER_NAME' "$BWRAP_LOG"
  grep -q 'GIT_USER_EMAIL' "$BWRAP_LOG"
}

@test "runtime=bwrap secrets are not on the command line" {
  run "$BWRAP_RUN_CMD"
  [ "$status" -eq 0 ]
  # Values must not appear in bwrap argv; names must appear as ENV_SECRET entries.
  ! grep -qF -- '--setenv GH_TOKEN' "$BWRAP_LOG"
  ! grep -qF -- '--setenv CLAUDE_CODE_OAUTH_TOKEN' "$BWRAP_LOG"
  ! grep -qF -- '--setenv ANTHROPIC_API_KEY' "$BWRAP_LOG"
  grep -q 'ENV_SECRET:GH_TOKEN' "$BWRAP_LOG"
  grep -q 'ENV_SECRET:CLAUDE_CODE_OAUTH_TOKEN' "$BWRAP_LOG"
}

@test "runtime=bwrap passes MODEL and lifecycle labels via --setenv" {
  run "$BWRAP_RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q 'MODEL' "$BWRAP_LOG"
  grep -q 'IN_PROGRESS_LABEL' "$BWRAP_LOG"
  grep -q 'COMPLETE_LABEL' "$BWRAP_LOG"
}

@test "runtime=bwrap mounts /nix/store read-only" {
  run "$BWRAP_RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q -- '--ro-bind /nix/store /nix/store' "$BWRAP_LOG"
}

@test "runtime=bwrap invokes /agent/entrypoint.sh inside the sandbox" {
  run "$BWRAP_RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q '/agent/entrypoint.sh' "$BWRAP_LOG"
}

@test "runtime=bwrap swap label: agent-in-progress on dispatch" {
  run "$BWRAP_RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-in-progress --remove-label ready-for-agent' "$GH_LOG"
  grep -q -- 'issue edit 2 --repo owner/repo --add-label agent-in-progress --remove-label ready-for-agent' "$GH_LOG"
}

@test "runtime=bwrap a non-zero exit swaps agent-in-progress -> agent-failed" {
  export FAKE_BWRAP_RUN_EXIT=1
  export FAKE_GH_ISSUES=$'1\tOnly issue'
  run "$BWRAP_RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q -- 'issue edit 1 --repo owner/repo --add-label agent-failed --remove-label agent-in-progress' "$GH_LOG"
}

@test "runtime=bwrap outcome report lists dispatched issues" {
  export FAKE_GH_ISSUES=$'1\tSingle'
  export FAKE_BWRAP_OUTCOME_1="SPINDRIFT_OUTCOME issue=1 pr=https://github.com/owner/repo/pull/1 status=merged note=ok"
  export FAKE_GH_PR_STATE_1="MERGED"
  export FAKE_GH_ISSUE_LABELS_1="agent-complete"
  run "$BWRAP_RUN_CMD"
  [ "$status" -eq 0 ]
  [[ "$output" == *"#1"* ]]
  [[ "$output" == *"status=verified-merged"* ]]
}

# --- Egress restriction (issue #100) -----------------------------------------

@test "runtime=podman passes --network flag when PODMAN_NETWORK is set" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tOnly issue'
  export PODMAN_NETWORK=pasta
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q -- '--network pasta' "$PODMAN_LOG"
}

@test "runtime=podman omits --network when PODMAN_NETWORK is unset" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tOnly issue'
  unset PODMAN_NETWORK
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  ! grep -q -- '--network' "$PODMAN_LOG"
}

@test "runtime=bwrap adds --unshare-net when BWRAP_UNSHARE_NET is set" {
  export BWRAP_UNSHARE_NET=1
  export FAKE_GH_ISSUES=$'1\tOnly issue'
  run "$BWRAP_RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q -- '--unshare-net' "$BWRAP_LOG"
}

@test "runtime=bwrap default: no --unshare-net (shares host netns; host-loopback reachable)" {
  export FAKE_GH_ISSUES=$'1\tOnly issue'
  unset BWRAP_UNSHARE_NET
  run "$BWRAP_RUN_CMD"
  [ "$status" -eq 0 ]
  ! grep -q -- '--unshare-net' "$BWRAP_LOG"
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

# --- dispatch --no-build (issue #276) ----------------------------------------

@test "dispatch --no-build fails fast with a clear message when image is absent" {
  export FAKE_PODMAN_IMAGE_PRESENT=0
  export FAKE_GH_ISSUES=$'1\tFirst issue'
  run "$SPINDRIFT_CMD" dispatch --no-build
  [ "$status" -ne 0 ]
  [[ "$output" == *"spindrift build"* ]]
  # Must not attempt nix build or container launch
  ! grep -q 'build' "$NIX_LOG"
  ! grep -q '^run ' "$PODMAN_LOG"
}

@test "dispatch --no-build runs normally when image is present" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tFirst issue'
  run "$SPINDRIFT_CMD" dispatch --no-build
  [ "$status" -eq 0 ]
  # No build was triggered
  ! grep -q 'build' "$NIX_LOG"
  ! grep -q "load -i" "$PODMAN_LOG"
  # Issue was dispatched
  grep -q 'ISSUE_NUMBER=1' "$PODMAN_LOG"
}

@test "dispatch --no-build accepts an issue number as a positional arg" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'42\tTarget issue'
  export FAKE_GH_ISSUE_LABELS_42="ready-for-agent"
  run "$SPINDRIFT_CMD" dispatch --no-build 42
  [ "$status" -eq 0 ]
  grep -q 'ISSUE_NUMBER=42' "$PODMAN_LOG"
}
