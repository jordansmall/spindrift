#!/usr/bin/env bats
# PostToolUse hook (issue #1988), paired with the PreToolUse hook
# bash-output-tee.sh that writes the log: replacing a big tool result with a tail
# keeps the full output on disk to grep instead of filling the model's context.

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

# Issue #3669: this hook is why the research-verdict fragments make the Box
# guard its SPINDRIFT_COMMENT line's size inside the command instead of reading
# the echoed line back — the tail keeps the wrong end of the line.
@test "the default tail drops a marker line's head while still reading as intact base64" {
  unset BASH_OUTPUT_SUMMARY_TAIL_BYTES

  log_file="$BATS_TEST_TMPDIR/marker.log"
  nonce="0123456789abcdef0123456789abcdef"
  # 5999 bytes -> 8000 base64 chars: past the 4096-byte tail the hook keeps,
  # but still under the 8192-char Bash cut, so the head the tail hides belongs
  # to a line the host would have accepted whole.
  encoded="$(head -c 5999 /dev/zero | tr '\0' 'x' | base64 -w0)"
  printf 'SPINDRIFT_COMMENT %s %s\n' "$nonce" "$encoded" >"$log_file"
  printf '0' >"$log_file.exit"

  payload="$(jq -n --argjson tool_input "$(tool_input_json "$log_file")" \
    '{tool_name: "Bash", tool_input: $tool_input, tool_response: "ignored"}')"

  run bash "$BASH_OUTPUT_SUMMARY_SCRIPT" <<<"$payload"
  [ "$status" -eq 0 ]

  summary="$(echo "$output" | jq -r '.hookSpecificOutput.updatedToolOutput')"
  [ -n "$summary" ]
  ! echo "$summary" | grep -q 'SPINDRIFT_COMMENT'
  echo "$summary" | tail -n1 | grep -qE '^[A-Za-z0-9+/]+=$'
}
