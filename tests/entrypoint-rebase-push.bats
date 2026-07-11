#!/usr/bin/env bats
# Rebase-before-push and push-failure handling (issue #345).

load helper

setup() {
  setup_entrypoint_env
}

# --- rebase-before-push and push-failure handling (issue #345) ---------------
# The prompt must instruct the agent to rebase onto the latest base before
# pushing, retry exactly once on rejection, and surface the real error
# (including the .github/workflows/ hard stop) rather than stranding commits.

@test "prompt instructs agent to rebase onto base before pushing" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q 'git rebase' "$CLAUDE_PROMPT_FILE"
  grep -q 'git fetch' "$CLAUDE_PROMPT_FILE"
}

@test "prompt instructs agent to retry push exactly once on rejection" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q 'rejected' "$CLAUDE_PROMPT_FILE"
  grep -q 'retry' "$CLAUDE_PROMPT_FILE"
}

@test "prompt instructs agent to emit status=blocked on persistent push failure" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q 'status=blocked' "$CLAUDE_PROMPT_FILE"
  grep -q 'gh issue comment' "$CLAUDE_PROMPT_FILE"
}

@test "prompt treats genuine .github/workflows/ change as a hard stop" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q '\.github/workflows' "$CLAUDE_PROMPT_FILE"
  grep -q 'workflow' "$CLAUDE_PROMPT_FILE"
}

