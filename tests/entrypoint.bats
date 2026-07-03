#!/usr/bin/env bats
# Behaviour of the in-container entrypoint: clone, branch, render prompt, and
# hand off to the (stubbed) agent. No network, no real LLM.

load helper

setup() {
  setup_fakes
  setup_bare_repo
  export REPO_SLUG="owner/repo"
  export GH_TOKEN="fake-token"
  export GIT_USER_NAME="Bot"
  export GIT_USER_EMAIL="bot@example.com"
  export BASE_BRANCH="main"
  export BRANCH_PREFIX="agent/issue-"
  export ISSUE_NUMBER="7"
  export ISSUE_TITLE="Do the thing"
  export WORK_DIR="$BATS_TEST_TMPDIR/work"
}

@test "entrypoint clones the target repo and cuts the issue branch" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -d "$WORK_DIR/.git" ]
  run git -C "$WORK_DIR" rev-parse --abbrev-ref HEAD
  [ "$status" -eq 0 ]
  [ "$output" = "agent/issue-7" ]
}

@test "entrypoint renders the prompt with issue placeholders substituted" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "Implement GitHub issue #7: Do the thing" "$CLAUDE_PROMPT_FILE"
  grep -q "agent/issue-7" "$CLAUDE_PROMPT_FILE"
  grep -q "cut from" "$CLAUDE_PROMPT_FILE"
}

@test "the configured mkHarness prompt is what reaches claude" {
  : "${PROMPT_HARNESS_DIR:?PROMPT_HARNESS_DIR must be set by the check}"
  export PROMPTS_DIR="$PROMPT_HARNESS_DIR"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "CONFIGURED-PROMPT-MARKER" "$CLAUDE_PROMPT_FILE"
  grep -q "Implement issue #7: Do the thing on agent/issue-7" "$CLAUDE_PROMPT_FILE"
}

@test "entrypoint invokes claude headlessly with skip-permissions" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "claude invoked for issue #7" "$CLAUDE_LOG"
  grep -q -- "--dangerously-skip-permissions" "$CLAUDE_LOG"
}

@test "entrypoint invokes claude with the baked default model when MODEL is unset" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q -- "--model claude-opus-4-8" "$CLAUDE_LOG"
}

@test "MODEL env overrides the baked default model at runtime" {
  export MODEL="claude-sonnet-4-6"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q -- "--model claude-sonnet-4-6" "$CLAUDE_LOG"
  ! grep -q -- "--model claude-opus-4-8" "$CLAUDE_LOG"
}

@test "entrypoint runs the configured prefetch hook inside the work tree" {
  export PREFETCH_LOG="$BATS_TEST_TMPDIR/prefetch.log"
  {
    printf '#!%s\n' "$(command -v bash)"
    cat <<'FAKE'
echo "warmed $PWD for #${ISSUE_NUMBER:-?}" >>"$PREFETCH_LOG"
FAKE
  } >"$FAKE_BIN/warm-cache"
  chmod +x "$FAKE_BIN/warm-cache"
  export PREFETCH="warm-cache"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "warmed" "$PREFETCH_LOG"
  grep -q "$WORK_DIR" "$PREFETCH_LOG"
}

@test "entrypoint passes --agents to claude when SCOUT_MODEL and REVIEW_MODEL are set" {
  export SCOUT_MODEL="claude-haiku-3-5"
  export REVIEW_MODEL="claude-opus-4-5"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q -- '--agents' "$CLAUDE_LOG"
  grep -q 'scout' "$CLAUDE_LOG"
  grep -q 'reviewer' "$CLAUDE_LOG"
}

@test "entrypoint omits --agents when SCOUT_MODEL is unset" {
  unset SCOUT_MODEL
  export REVIEW_MODEL="claude-opus-4-5"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! grep -q -- '--agents' "$CLAUDE_LOG"
}

@test "entrypoint omits --agents when REVIEW_MODEL is unset" {
  export SCOUT_MODEL="claude-haiku-3-5"
  unset REVIEW_MODEL
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  ! grep -q -- '--agents' "$CLAUDE_LOG"
}

@test "IN_PROGRESS_LABEL and COMPLETE_LABEL are substituted in the prompt" {
  local prompt_dir="$BATS_TEST_TMPDIR/prompts"
  mkdir -p "$prompt_dir"
  cat >"$prompt_dir/issue-prompt.md" <<'EOF'
label: ${IN_PROGRESS_LABEL} complete: ${COMPLETE_LABEL}
EOF
  export PROMPTS_DIR="$prompt_dir"
  export IN_PROGRESS_LABEL="wip"
  export COMPLETE_LABEL="done"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q 'label: wip' "$CLAUDE_PROMPT_FILE"
  grep -q 'complete: done' "$CLAUDE_PROMPT_FILE"
}

@test "default prompt delegates exploration to the scout subagent" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qi 'scout' "$CLAUDE_PROMPT_FILE"
}

@test "default prompt spawns a reviewer subagent with SPEC and STANDARDS rubric" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qi 'reviewer' "$CLAUDE_PROMPT_FILE"
  grep -q 'SPEC' "$CLAUDE_PROMPT_FILE"
  grep -q 'STANDARDS' "$CLAUDE_PROMPT_FILE"
}

@test "default prompt specifies a review-build loop that never advances with a blocking finding" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q 'BLOCKING\|blocking' "$CLAUDE_PROMPT_FILE"
}

@test "default prompt degrades gracefully when tier models are unavailable" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q 'if available\|if it.*available\|when.*available' "$CLAUDE_PROMPT_FILE"
}

@test "default prompt blocks on CI and never merges on red" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qi 'pr checks\|watch.*ci\|ci.*watch\|watch.*check\|check.*watch' "$CLAUDE_PROMPT_FILE"
  grep -qi 'never.*merg.*red\|red.*never.*merg\|do not.*merg.*red\|merg.*only.*green\|green.*merg' "$CLAUDE_PROMPT_FILE"
}

@test "default prompt merges with rebase and deletes the branch on green" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q -- '--rebase' "$CLAUDE_PROMPT_FILE"
  grep -q -- '--delete-branch' "$CLAUDE_PROMPT_FILE"
}

@test "default prompt relabels in-progress to complete on success" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q 'COMPLETE_LABEL\|agent-complete' "$CLAUDE_PROMPT_FILE"
  grep -q -- '--remove-label.*IN_PROGRESS_LABEL\|--remove-label.*agent-in-progress\|--add-label.*COMPLETE_LABEL\|--add-label.*agent-complete' "$CLAUDE_PROMPT_FILE"
}

@test "default prompt emits exactly one SPINDRIFT_OUTCOME line" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -c 'SPINDRIFT_OUTCOME' "$CLAUDE_PROMPT_FILE" | grep -q '^[1-9]'
}

@test "default prompt emits SPINDRIFT_OUTCOME with status=blocked in the blocked path" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q 'status=blocked' "$CLAUDE_PROMPT_FILE"
}

@test "default prompt opens a draft PR and comments on the issue when blocked" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q -- '--draft' "$CLAUDE_PROMPT_FILE"
  grep -q 'issue comment\|pr comment\|comment.*issue\|comment.*blocked' "$CLAUDE_PROMPT_FILE"
}

@test "entrypoint skips the prefetch hook when it is empty" {
  export PREFETCH_LOG="$BATS_TEST_TMPDIR/prefetch.log"
  {
    printf '#!%s\n' "$(command -v bash)"
    cat <<'FAKE'
echo ran >>"$PREFETCH_LOG"
FAKE
  } >"$FAKE_BIN/warm-cache"
  chmod +x "$FAKE_BIN/warm-cache"
  export PREFETCH=""
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ ! -f "$PREFETCH_LOG" ]
}
