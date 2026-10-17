#!/usr/bin/env bats
# PreToolUse hook (issue #1988): rewrites every Bash tool call so its output
# tees to a per-command file while the real exit code still propagates, so
# PostToolUse (bash-output-summary.sh) always has a full copy to summarize
# from, not just on overflow.

setup() {
  : "${BASH_OUTPUT_TEE_SCRIPT:?BASH_OUTPUT_TEE_SCRIPT must be set}"
  export BASH_OUTPUT_TEE_DIR="$BATS_TEST_TMPDIR/tee-logs"
}

@test "rewrites a Bash call to tee its output to a log file while preserving the exit code" {
  run bash "$BASH_OUTPUT_TEE_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"echo hi"}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.hookEventName == "PreToolUse"' >/dev/null

  rewritten="$(echo "$output" | jq -r '.hookSpecificOutput.updatedInput.command')"
  echo "$rewritten" | grep -q 'tee'
  echo "$rewritten" | grep -q 'echo hi'

  run bash -c "$rewritten"
  [ "$status" -eq 0 ]
  [ "$output" = "hi" ]
}

@test "ignores non-Bash tool calls" {
  run bash "$BASH_OUTPUT_TEE_SCRIPT" <<<'{"tool_name":"Read","tool_input":{"file_path":"/tmp/x"}}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "preserves a nonzero exit code and writes the full output to the log file" {
  run bash "$BASH_OUTPUT_TEE_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"echo boom >&2; exit 7"}}'
  [ "$status" -eq 0 ]

  rewritten="$(echo "$output" | jq -r '.hookSpecificOutput.updatedInput.command')"
  log_file="$(grep -oE '[^"[:space:]]+\.log' <<<"$rewritten" | head -1)"

  run bash -c "$rewritten"
  [ "$status" -eq 7 ]
  [ "$output" = "boom" ]

  [ -f "$log_file" ]
  grep -q boom "$log_file"
  [ "$(cat "$log_file.exit")" = "7" ]
}

@test "unsets the credential env vars itself, independent of env-credential-scrub.sh" {
  run bash "$BASH_OUTPUT_TEE_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"echo \"[$ANTHROPIC_API_KEY][$CLAUDE_CODE_OAUTH_TOKEN][$OPENCODE_AUTH_CONTENT]\""}}'
  [ "$status" -eq 0 ]

  rewritten="$(echo "$output" | jq -r '.hookSpecificOutput.updatedInput.command')"

  ANTHROPIC_API_KEY=leaked-key CLAUDE_CODE_OAUTH_TOKEN=leaked-token \
    OPENCODE_AUTH_CONTENT=leaked-opencode \
    run bash -c "$rewritten"
  [ "$status" -eq 0 ]
  [ "$output" = "[][][]" ]
}

@test "handles a command whose last line ends in a line-continuation backslash" {
  run bash "$BASH_OUTPUT_TEE_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"echo hi \\"}}'
  [ "$status" -eq 0 ]

  rewritten="$(echo "$output" | jq -r '.hookSpecificOutput.updatedInput.command')"

  run bash -c "$rewritten"
  [ "$status" -eq 0 ]
  [ "$output" = "hi" ]
}

@test "handles a command whose last line is a comment with no trailing newline" {
  run bash "$BASH_OUTPUT_TEE_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"echo hi # a trailing comment"}}'
  [ "$status" -eq 0 ]

  rewritten="$(echo "$output" | jq -r '.hookSpecificOutput.updatedInput.command')"

  run bash -c "$rewritten"
  [ "$status" -eq 0 ]
  [ "$output" = "hi" ]
}

@test "blocks a Bash call when the credential list file is missing" {
  CREDENTIAL_VARS_FILE="$BATS_TEST_TMPDIR/absent" \
    run bash "$BASH_OUTPUT_TEE_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"echo hi"}}'
  [ "$status" -eq 2 ]
}

@test "blocks a Bash call when the credential list file names no variables" {
  printf '# only a comment\n\n' >"$BATS_TEST_TMPDIR/empty"
  CREDENTIAL_VARS_FILE="$BATS_TEST_TMPDIR/empty" \
    run bash "$BASH_OUTPUT_TEE_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"echo hi"}}'
  [ "$status" -eq 2 ]
}

@test "unsets every name in an overridden credential list file, skipping comments" {
  printf '# header\nFOO_SECRET\n\nBAR_SECRET\n' >"$BATS_TEST_TMPDIR/vars"
  CREDENTIAL_VARS_FILE="$BATS_TEST_TMPDIR/vars" \
    run bash "$BASH_OUTPUT_TEE_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"echo hi"}}'
  [ "$status" -eq 0 ]
  rewritten="$(echo "$output" | jq -r '.hookSpecificOutput.updatedInput.command')"
  [[ "$rewritten" == *"unset FOO_SECRET BAR_SECRET"* ]]
}

# Fail closed: the parse helper is sourced from beside the hook, so a hook
# copied away from it must refuse the call rather than run unscrubbed.
@test "blocks a Bash call when the parse helper is missing beside the hook" {
  cp "$BASH_OUTPUT_TEE_SCRIPT" "$BATS_TEST_TMPDIR/bash-output-tee.sh"
  printf 'FOO_SECRET\n' >"$BATS_TEST_TMPDIR/vars"
  CREDENTIAL_VARS_FILE="$BATS_TEST_TMPDIR/vars" \
    run bash "$BATS_TEST_TMPDIR/bash-output-tee.sh" <<<'{"tool_name":"Bash","tool_input":{"command":"echo hi"}}'
  [ "$status" -eq 2 ]
  [[ "$output" == *"cannot source credential-vars-parse"* ]]
}
