#!/usr/bin/env bats
# Behaviour of the nix-generated `run` command (fans out one Box per issue).

load helper

setup() {
  setup_fakes
  set_run_env
  cd "$BATS_TEST_TMPDIR"
  export FAKE_GH_ISSUES=$'1\tFirst issue\n2\tSecond issue'
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
  grep -q "image exists spindrift:$image_hash" "$PODMAN_LOG"
  ! grep -q 'image exists spindrift:latest' "$PODMAN_LOG"
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
  grep -q 'MODEL=claude-opus-4-8' "$PODMAN_LOG"
}

@test "MODEL env overrides the baked default into the container" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export MODEL=claude-sonnet-4-6
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q 'MODEL=claude-sonnet-4-6' "$PODMAN_LOG"
  ! grep -q 'MODEL=claude-opus-4-8' "$PODMAN_LOG"
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

@test "run invokes the baked entrypoint and baked prompt (no prompt mount)" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  ! grep -q 'entrypoint.sh:/agent' "$PODMAN_LOG"
  grep -q '/agent/entrypoint.sh' "$PODMAN_LOG"
  ! grep -q ':/agent/prompts' "$PODMAN_LOG"
}

@test "run exits cleanly when there are no matching issues" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=""
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^run ' "$PODMAN_LOG")" -eq 0 ]
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
  # ready-for-agent query returns nothing and no new container starts.
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
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

@test "run dispatches an issue whose external blocker already carries COMPLETE_LABEL" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'2\tDependent'
  export FAKE_GH_ISSUE_BODY_2="Depends on #1"
  export FAKE_GH_ISSUE_LABELS_1="agent-complete"
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q 'ISSUE_NUMBER=2' "$PODMAN_LOG"
}

@test "run parses 'blocked by' syntax as a blocker reference" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'2\tDependent'
  export FAKE_GH_ISSUE_BODY_2="blocked by #1"
  export FAKE_GH_ISSUE_LABELS_1="agent-complete"
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q 'ISSUE_NUMBER=2' "$PODMAN_LOG"
}

@test "run parses the header-plus-list blocked-by format from /to-issues" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'2\tDependent'
  # /to-issues renders "## Blocked by" as a section header with "- #N" items —
  # the format the original shell regex missed entirely (issue #60).
  export FAKE_GH_ISSUE_BODY_2=$'## Blocked by\n\n- #1 (some prerequisite issue)'
  export FAKE_GH_ISSUE_LABELS_1="agent-complete"
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q 'ISSUE_NUMBER=2' "$PODMAN_LOG"
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

@test "run dispatches the blocker before the dependent (wave ordering)" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  export FAKE_GH_ISSUES=$'1\tBlocker\n2\tDependent'
  export FAKE_GH_ISSUE_BODY_2="depends on #1"
  export FAKE_PODMAN_AUTO_COMPLETE=1
  export GH_STATE="$GH_LOG.state"
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q 'ISSUE_NUMBER=1' "$PODMAN_LOG"
  grep -q 'ISSUE_NUMBER=2' "$PODMAN_LOG"
  line1="$(grep -n 'ISSUE_NUMBER=1' "$PODMAN_LOG" | cut -d: -f1 | head -1)"
  line2="$(grep -n 'ISSUE_NUMBER=2' "$PODMAN_LOG" | cut -d: -f1 | head -1)"
  [ "$line1" -lt "$line2" ]
}

@test "run preserves single-wave behaviour when no dependencies are declared" {
  export FAKE_PODMAN_IMAGE_PRESENT=1
  run "$RUN_CMD"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^run ' "$PODMAN_LOG")" -eq 2 ]
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
  [ "$(grep -c '^--ro-bind' "$BWRAP_LOG")" -ge 2 ]
  grep -q 'ISSUE_NUMBER' "$BWRAP_LOG"
  [ ! -s "$PODMAN_LOG" ]
}

@test "runtime=bwrap never loads an OCI image" {
  run "$BWRAP_RUN_CMD"
  [ "$status" -eq 0 ]
  ! grep -q 'load -i' "$BWRAP_LOG"
  ! grep -q 'load -i' "$PODMAN_LOG"
}

@test "runtime=bwrap passes required env vars via --setenv" {
  run "$BWRAP_RUN_CMD"
  [ "$status" -eq 0 ]
  grep -q 'GH_TOKEN' "$BWRAP_LOG"
  grep -q 'REPO_SLUG' "$BWRAP_LOG"
  grep -q 'GIT_USER_NAME' "$BWRAP_LOG"
  grep -q 'GIT_USER_EMAIL' "$BWRAP_LOG"
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
