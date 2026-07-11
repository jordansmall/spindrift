#!/usr/bin/env bats
# Prompt rendering, FIX_PASS routing, and CI_FAILURE_SUMMARY (issues #425, #426).

load helper

setup() {
  setup_entrypoint_env
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

# FIX_PASS (issue #425): the launcher sets FIX_PASS on a fix box (dispatched
# when CI comes back red) so the entrypoint drives a dedicated warm fix-prompt
# instead of the cold issue-prompt a fresh run uses.
@test "FIX_PASS unset drives issue-prompt.md, not fix-prompt.md" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "Fresh clone, new branch" "$CLAUDE_PROMPT_FILE"
  ! grep -q "already checked out" "$CLAUDE_PROMPT_FILE"
}

@test "FIX_PASS=0 still drives issue-prompt.md (byte-identical to unset)" {
  export FIX_PASS="0"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "Fresh clone, new branch" "$CLAUDE_PROMPT_FILE"
  ! grep -q "already checked out" "$CLAUDE_PROMPT_FILE"
}

@test "FIX_PASS>0 drives fix-prompt.md instead of issue-prompt.md" {
  export FIX_PASS="2"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "already checked out" "$CLAUDE_PROMPT_FILE"
  ! grep -q "Fresh clone, new branch" "$CLAUDE_PROMPT_FILE"
}

# CI_FAILURE_SUMMARY (issue #426): the launcher captures the concrete CI
# failure on genuine-red and forwards it to the fix box so the fix agent goes
# straight to the failing check instead of re-discovering it from scratch.
@test "CI_FAILURE_SUMMARY set on a fix pass is rendered into the prompt" {
  export FIX_PASS="2"
  export CI_FAILURE_SUMMARY="lint: FAILURE
2 errors in main.go"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "lint: FAILURE" "$CLAUDE_PROMPT_FILE"
  grep -q "2 errors in main.go" "$CLAUDE_PROMPT_FILE"
}

@test "CI_FAILURE_SUMMARY unset on a fix pass falls back with no error" {
  export FIX_PASS="2"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "already checked out" "$CLAUDE_PROMPT_FILE"
  ! grep -q '\${CI_FAILURE_SUMMARY}' "$CLAUDE_PROMPT_FILE"
}

@test "CI_FAILURE_SUMMARY is ignored on a fresh (non-fix) run" {
  export CI_FAILURE_SUMMARY="lint: FAILURE"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -q "Fresh clone, new branch" "$CLAUDE_PROMPT_FILE"
  ! grep -q "lint: FAILURE" "$CLAUDE_PROMPT_FILE"
}

