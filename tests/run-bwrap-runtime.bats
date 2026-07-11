#!/usr/bin/env bats
# bwrap runner (issue #54): sandboxed invocation, env/secret passing, mounts, label swaps.

load helper

setup() {
  setup_run_env
}

# --- bwrap runner (issue #54) ------------------------------------------------

@test "runtime=bwrap launches one bwrap invocation per issue" {
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

