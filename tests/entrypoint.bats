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

@test "entrypoint invokes claude headlessly with skip-permissions" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "claude invoked for issue #7" "$CLAUDE_LOG"
  grep -q -- "--dangerously-skip-permissions" "$CLAUDE_LOG"
}

@test "entrypoint runs the configured prefetch hook inside the work tree" {
  export PREFETCH_LOG="$BATS_TEST_TMPDIR/prefetch.log"
  cat >"$FAKE_BIN/warm-cache" <<'FAKE'
#!/usr/bin/env bash
echo "warmed $PWD for #${ISSUE_NUMBER:-?}" >>"$PREFETCH_LOG"
FAKE
  chmod +x "$FAKE_BIN/warm-cache"
  export PREFETCH="warm-cache"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "warmed" "$PREFETCH_LOG"
  # runs after the clone, from within the cloned work tree
  grep -q "$WORK_DIR" "$PREFETCH_LOG"
}

@test "entrypoint skips the prefetch hook when it is empty" {
  export PREFETCH_LOG="$BATS_TEST_TMPDIR/prefetch.log"
  cat >"$FAKE_BIN/warm-cache" <<'FAKE'
#!/usr/bin/env bash
echo ran >>"$PREFETCH_LOG"
FAKE
  chmod +x "$FAKE_BIN/warm-cache"
  export PREFETCH=""
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ ! -f "$PREFETCH_LOG" ]
}
