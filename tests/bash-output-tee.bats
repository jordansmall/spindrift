#!/usr/bin/env bats
# PreToolUse hook (issue #1988): rewrites every Bash tool call so its
# stdout+stderr tee to a per-command file on disk while the real exit code
# still propagates, giving PostToolUse (bash-output-summary.sh) a full copy
# on disk to summarize from -- uniformly, not just on overflow, and with an
# error-oriented tail rather than Claude Code's own start-only preview.
# Exercised directly against the script, mirroring reject-background-bash.bats.

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
  run bash "$BASH_OUTPUT_TEE_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"echo \"[$ANTHROPIC_API_KEY][$CLAUDE_CODE_OAUTH_TOKEN]\""}}'
  [ "$status" -eq 0 ]

  rewritten="$(echo "$output" | jq -r '.hookSpecificOutput.updatedInput.command')"

  ANTHROPIC_API_KEY=leaked-key CLAUDE_CODE_OAUTH_TOKEN=leaked-token \
    run bash -c "$rewritten"
  [ "$status" -eq 0 ]
  [ "$output" = "[][]" ]
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
