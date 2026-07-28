#!/usr/bin/env bats
# End-to-end: clone -> branch -> stub agent -> commit -> push -> PR, all against
# a local bare repo with a faked `gh pr create`. Proves the whole path without a
# real container, network, or LLM.

load helper

setup() {
  setup_fakes
  setup_bare_repo
  export FAKE_CLAUDE_COMMIT=1
  # This fixture's stub agent pushes and opens the PR itself (the read-write
  # agent-owned code-out flow), so it is a read-write Box: set the same
  # BOX_WRITE_ENABLED signal a real read-write Box receives (issue #1951).
  # Without it the read-only harness-owned bundle-out gate (issue #2082)
  # fires post-driver and dies writing to a nonexistent /outbox.
  export BOX_WRITE_ENABLED=1
  export REPO_SLUG="owner/repo"
  export GH_TOKEN="fake-token"
  export GIT_USER_NAME="Bot"
  export GIT_USER_EMAIL="bot@example.com"
  export BASE_BRANCH="main"
  export BRANCH_PREFIX="agent/issue-"
  export ISSUE_NUMBER="3"
  export ISSUE_TITLE="Ship it"
  export WORK_DIR="$BATS_TEST_TMPDIR/work"
}

@test "pipeline pushes the agent's branch to the bare repo" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  run git --git-dir="$REMOTE_ROOT/owner/repo.git" log --oneline agent/issue-3
  [ "$status" -eq 0 ]
  [[ "$output" == *"stub agent commit for #3"* ]]
}

@test "pipeline opens a PR that closes the issue" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "pr create" "$GH_LOG"
  grep -q -- "--base main" "$GH_LOG"
  grep -q -- "--head agent/issue-3" "$GH_LOG"
  grep -q "Closes #3" "$GH_LOG"
}
