#!/usr/bin/env bats
# PostToolUse hook (issue #1988): replaces a Bash tool result with a
# bounded, error-oriented tail once the log file bash-output-tee.sh (the
# paired PreToolUse hook) wrote crosses the inline bound -- the full output
# stays on disk for the agent to grep/read, only exit code + log path + tail
# enter the model's context. A command whose output stays under the bound
# is left untouched: no needless round-trip to read a file back.
# Exercised directly against the script, mirroring reject-background-bash.bats.

setup() {
  : "${BASH_OUTPUT_SUMMARY_SCRIPT:?BASH_OUTPUT_SUMMARY_SCRIPT must be set}"
  : "${BASH_OUTPUT_TEE_SCRIPT:?BASH_OUTPUT_TEE_SCRIPT must be set}"
  export BASH_OUTPUT_SUMMARY_TAIL_BYTES=16
  export BASH_OUTPUT_TEE_DIR="$BATS_TEST_TMPDIR/tee-logs"
}

tool_input_json() {
  local log_file="$1"
  local cmd
  cmd="$(printf '%s\n' '{' 'true' "} 2>&1 | tee -- \"$log_file\"")"
  jq -n --arg cmd "$cmd" '{command: $cmd}'
}

@test "replaces the tool result with exit code, log path, and a bounded tail when output exceeds the bound" {
  log_file="$BATS_TEST_TMPDIR/big.log"
  printf 'line1\nline2\nline3\nline4\n' >"$log_file"
  printf '0' >"$log_file.exit"

  payload="$(jq -n --argjson tool_input "$(tool_input_json "$log_file")" \
    '{tool_name: "Bash", tool_input: $tool_input, tool_response: "line1\nline2\nline3\nline4\n"}')"

  run bash "$BASH_OUTPUT_SUMMARY_SCRIPT" <<<"$payload"
  [ "$status" -eq 0 ]

  summary="$(echo "$output" | jq -r '.hookSpecificOutput.updatedToolOutput')"
  echo "$summary" | grep -q "exit code: 0"
  echo "$summary" | grep -q "$log_file"
  echo "$summary" | grep -q "line4"
  ! echo "$summary" | grep -q "line1"
}

@test "surfaces a nonzero exit code in the summary" {
  log_file="$BATS_TEST_TMPDIR/failed.log"
  printf 'line1\nline2\nline3\nline4\n' >"$log_file"
  printf '7' >"$log_file.exit"

  payload="$(jq -n --argjson tool_input "$(tool_input_json "$log_file")" \
    '{tool_name: "Bash", tool_input: $tool_input, tool_response: "line1\nline2\nline3\nline4\n"}')"

  run bash "$BASH_OUTPUT_SUMMARY_SCRIPT" <<<"$payload"
  [ "$status" -eq 0 ]

  summary="$(echo "$output" | jq -r '.hookSpecificOutput.updatedToolOutput')"
  echo "$summary" | grep -q "exit code: 7"
}

@test "falls back to 'unknown' when the .exit sidecar file is missing" {
  log_file="$BATS_TEST_TMPDIR/no-exit-file.log"
  printf 'line1\nline2\nline3\nline4\n' >"$log_file"

  payload="$(jq -n --argjson tool_input "$(tool_input_json "$log_file")" \
    '{tool_name: "Bash", tool_input: $tool_input, tool_response: "line1\nline2\nline3\nline4\n"}')"

  run bash "$BASH_OUTPUT_SUMMARY_SCRIPT" <<<"$payload"
  [ "$status" -eq 0 ]

  summary="$(echo "$output" | jq -r '.hookSpecificOutput.updatedToolOutput')"
  echo "$summary" | grep -q "exit code: unknown"
}

@test "leaves the tool result untouched when output stays under the bound" {
  log_file="$BATS_TEST_TMPDIR/small.log"
  printf 'hi\n' >"$log_file"
  printf '0' >"$log_file.exit"

  payload="$(jq -n --argjson tool_input "$(tool_input_json "$log_file")" \
    '{tool_name: "Bash", tool_input: $tool_input, tool_response: "hi\n"}')"

  run bash "$BASH_OUTPUT_SUMMARY_SCRIPT" <<<"$payload"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "ignores non-Bash tool calls" {
  run bash "$BASH_OUTPUT_SUMMARY_SCRIPT" <<<'{"tool_name":"Read","tool_input":{"file_path":"/tmp/x"},"tool_response":"contents"}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "ignores a Bash call bash-output-tee.sh never rewrote" {
  run bash "$BASH_OUTPUT_SUMMARY_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"echo hi"},"tool_response":"hi\n"}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "resolves the real log file even when the user's own command contains a tee -- fragment" {
  real_log="$BATS_TEST_TMPDIR/real.log"
  printf 'line1\nline2\nline3\nline4\n' >"$real_log"
  printf '0' >"$real_log.exit"

  command="$(
    printf '%s\n' \
      '{' \
      'echo hi | tee -- "/tmp/not-the-real-log"' \
      "} 2>&1 | tee -- \"$real_log\"" \
      'ec="${PIPESTATUS[0]}"' \
      'exit "$ec"'
  )"
  payload="$(jq -n --arg cmd "$command" '{tool_name: "Bash", tool_input: {command: $cmd}, tool_response: ""}')"

  run bash "$BASH_OUTPUT_SUMMARY_SCRIPT" <<<"$payload"
  [ "$status" -eq 0 ]

  summary="$(echo "$output" | jq -r '.hookSpecificOutput.updatedToolOutput')"
  echo "$summary" | grep -q "log file: $real_log"
  echo "$summary" | grep -q "line4"
}

@test "round-trips through bash-output-tee.sh: a big command's real output is bounded end to end" {
  pre_output="$(bash "$BASH_OUTPUT_TEE_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"printf \"line1\\nline2\\nline3\\nline4\\n\""}}')"
  rewritten="$(echo "$pre_output" | jq -r '.hookSpecificOutput.updatedInput.command')"

  run bash -c "$rewritten"
  [ "$status" -eq 0 ]

  post_payload="$(jq -n --arg cmd "$rewritten" '{tool_name: "Bash", tool_input: {command: $cmd}, tool_response: "line1\nline2\nline3\nline4\n"}')"
  run bash "$BASH_OUTPUT_SUMMARY_SCRIPT" <<<"$post_payload"
  [ "$status" -eq 0 ]

  summary="$(echo "$output" | jq -r '.hookSpecificOutput.updatedToolOutput')"
  echo "$summary" | grep -q "exit code: 0"
  echo "$summary" | grep -q "line4"
  ! echo "$summary" | grep -q "line1"
}
