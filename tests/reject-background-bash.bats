#!/usr/bin/env bats
# PreToolUse hook (issue #1609): rejects a Bash tool call carrying
# run_in_background: true before it executes, since a headless Box run has no
# harness watching for a later re-invocation (#1542 lost a run this way).
# Exercised directly against the script -- not through a real claude session,
# since the bats suite drives the bash layer through fakes only (no real
# LLM) -- so this is a unit test of the hook's own stdin/stdout contract.

setup() {
  : "${REJECT_BACKGROUND_BASH_SCRIPT:?REJECT_BACKGROUND_BASH_SCRIPT must be set}"
}

@test "denies a Bash call with run_in_background: true" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"go test ./...","run_in_background":true}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.hookEventName == "PreToolUse"' >/dev/null
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecisionReason | test("foreground")' >/dev/null
}

@test "allows a Bash call with run_in_background: false" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"go test ./...","run_in_background":false}}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "allows a Bash call with no run_in_background key at all" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"go test ./..."}}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "ignores non-Bash tool calls even with run_in_background: true in their input" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Read","tool_input":{"file_path":"/tmp/x","run_in_background":true}}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}
