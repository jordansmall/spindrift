#!/usr/bin/env bats
# PreToolUse hook (issue #1609): rejects a Bash tool call carrying
# run_in_background: true before it executes, since a headless Box run has no
# harness watching for a later re-invocation (#1542 lost a run this way).
# #1620 widens the same hook to also parse tool_input.command for a shell-level
# self-backgrounding escape (trailing/mid-command &, nohup) that the
# structured run_in_background parameter never sees.
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

@test "denies a Bash call whose command ends in a trailing &" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"sleep 300 &"}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.hookEventName == "PreToolUse"' >/dev/null
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "denies a Bash call that backgrounds a command mid-line, not just trailing" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"sleep 300 & echo done"}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "allows a Bash call chained with && (logical and, not background)" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"go build ./... && go test ./..."}}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "allows a Bash call with a literal & inside a quoted string" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"echo \"foo & bar\""}}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "allows a Bash call with a literal & inside a single-quoted string" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<"{\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"echo 'foo & bar'\"}}"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "denies a Bash call redirecting stderr to stdout and then backgrounding" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"long_running.sh > /tmp/out.log 2>&1 &"}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "allows a Bash call using >&2 to redirect stdout to stderr in the foreground" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"echo failed >&2"}}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "allows a Bash call using &> to redirect stdout and stderr in the foreground" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"go build ./... &> /tmp/build.log"}}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "denies a Bash call invoking nohup" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"nohup long_running.sh"}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "allows a Bash call with a backslash-escaped & outside any quotes" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"echo foo \\& bar"}}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "allows a Bash call whose command merely contains nohup as part of a longer word" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"echo mynohupthing"}}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "allows a Bash call where nohup only appears inside a quoted string" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"echo \"nohup\""}}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "denies a Bash call invoking nohup via a backslash-escaped keyword prefix" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"\\nohup long_running.sh"}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "allows a Bash call where a backslash escapes the space before nohup" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"echo foo\\ nohup"}}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "denies a Bash call invoking setsid" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"setsid long_running.sh"}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "allows a Bash call whose command merely contains setsid as part of a longer word" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"echo mysetsidthing"}}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "allows a Bash call where setsid only appears inside a quoted string" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"echo \"setsid\""}}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "denies a Bash call invoking setsid via a backslash-escaped keyword prefix" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"\\setsid long_running.sh"}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "denies a Bash call using named coproc" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"coproc foo { sleep 300; }"}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "denies a Bash call using unnamed coproc" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"coproc { sleep 300; }"}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "allows a Bash call whose command merely contains coproc as part of a longer word" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"echo mycoprocthing"}}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "allows a Bash call where coproc only appears inside a quoted string" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"echo \"coproc\""}}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "denies a Bash call invoking coproc via a backslash-escaped keyword prefix" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"\\coproc foo { sleep 300; }"}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "allows a Bash call using |& to pipe stdout and stderr in the foreground" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"make |& tee build.log"}}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "allows a Bash call using <& to duplicate a file descriptor on stdin" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"read x <&3"}}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "denies a Bash call backgrounding inside command substitution" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"echo $(sleep 300 &)"}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}
