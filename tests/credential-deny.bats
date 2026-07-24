#!/usr/bin/env bats
# PreToolUse hook (issue #1909, spec #1907): rejects a Read/Bash tool call
# targeting a known credential path before it executes, so the Driver cannot
# read its own secrets into context even under --dangerously-skip-permissions
# (the hook mechanism is independent of the permission system, mirroring
# reject-background-bash.sh, issue #1609).
# Exercised directly against the script -- not through a real claude session,
# since the bats suite drives the bash layer through fakes only (no real
# LLM) -- so this is a unit test of the hook's own stdin/stdout contract.

setup() {
  : "${CREDENTIAL_DENY_HOOK_SCRIPT:?CREDENTIAL_DENY_HOOK_SCRIPT must be set}"
}

@test "denies a Read of ~/.claude/.credentials.json" {
  run bash "$CREDENTIAL_DENY_HOOK_SCRIPT" <<<'{"tool_name":"Read","tool_input":{"file_path":"/home/agent/.claude/.credentials.json"}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.hookEventName == "PreToolUse"' >/dev/null
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "denies a Read of ~/.config/gh/hosts.yml" {
  run bash "$CREDENTIAL_DENY_HOOK_SCRIPT" <<<'{"tool_name":"Read","tool_input":{"file_path":"/home/agent/.config/gh/hosts.yml"}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "denies a Read of a nested .env file anywhere in the tree" {
  run bash "$CREDENTIAL_DENY_HOOK_SCRIPT" <<<'{"tool_name":"Read","tool_input":{"file_path":"/work/some/nested/dir/.env"}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "denies a Bash call catting ~/.claude/.credentials.json" {
  run bash "$CREDENTIAL_DENY_HOOK_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"cat ~/.claude/.credentials.json"}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "denies a Bash call piping a credential file through another command" {
  run bash "$CREDENTIAL_DENY_HOOK_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"cat ~/.claude/.credentials.json | jq ."}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "denies a Bash call redirecting a credential file to another path" {
  run bash "$CREDENTIAL_DENY_HOOK_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"cat ~/.claude/.credentials.json > /tmp/x"}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "denies a Bash call copying a credential file elsewhere" {
  run bash "$CREDENTIAL_DENY_HOOK_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"cp ~/.claude/.credentials.json /tmp/leak"}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "denies a Bash call catting a bare relative .env" {
  run bash "$CREDENTIAL_DENY_HOOK_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"cat .env"}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "denies a Bash call catting a bare relative .config/gh/hosts.yml" {
  run bash "$CREDENTIAL_DENY_HOOK_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"cat .config/gh/hosts.yml"}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "allows a Read of an ordinary repository file" {
  run bash "$CREDENTIAL_DENY_HOOK_SCRIPT" <<<'{"tool_name":"Read","tool_input":{"file_path":"/work/lib/image.nix"}}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "allows a Bash call over an ordinary repository file" {
  run bash "$CREDENTIAL_DENY_HOOK_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"go test ./..."}}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "ignores a non-Read/Bash tool call even over a credential path" {
  run bash "$CREDENTIAL_DENY_HOOK_SCRIPT" <<<'{"tool_name":"Write","tool_input":{"file_path":"/home/agent/.claude/.credentials.json"}}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "allows a Bash call whose command merely contains .env as part of a longer word" {
  run bash "$CREDENTIAL_DENY_HOOK_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"cat environment.txt"}}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "denies a Read of a .env.local dotenv variant" {
  run bash "$CREDENTIAL_DENY_HOOK_SCRIPT" <<<'{"tool_name":"Read","tool_input":{"file_path":"/work/.env.local"}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "allows a Bash call over a file that merely ends in .env as part of a longer name" {
  run bash "$CREDENTIAL_DENY_HOOK_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"cat config.env"}}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "allows malformed non-JSON stdin" {
  run bash "$CREDENTIAL_DENY_HOOK_SCRIPT" <<<'not json at all'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}
