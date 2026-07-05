#!/usr/bin/env bats
# Tests for agent/format-transcript.sh: reads stream-json NDJSON from stdin
# and renders each event as human-readable output.

load helper

setup() {
  : "${FORMAT_TRANSCRIPT_SCRIPT:?FORMAT_TRANSCRIPT_SCRIPT must point at the formatter}"
}

@test "system event produces no output" {
  run bash "$FORMAT_TRANSCRIPT_SCRIPT" \
    <<< '{"type":"system","subtype":"init","session_id":"fake"}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "assistant text event is rendered as prose" {
  run bash "$FORMAT_TRANSCRIPT_SCRIPT" \
    <<< '{"type":"assistant","message":{"content":[{"type":"text","text":"Hello, world!"}]}}'
  [ "$status" -eq 0 ]
  [[ "$output" == *"Hello, world!"* ]]
}

@test "assistant tool_use event is rendered with a glyph and the tool name" {
  run bash "$FORMAT_TRANSCRIPT_SCRIPT" \
    <<< '{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls -la"}}]}}'
  [ "$status" -eq 0 ]
  [[ "$output" == *"⏺"* ]]
  [[ "$output" == *"Bash"* ]]
}

@test "tool_result event is rendered with a dim connector and the content" {
  run bash "$FORMAT_TRANSCRIPT_SCRIPT" \
    <<< '{"type":"tool_result","tool_use_id":"t1","content":"result output here"}'
  [ "$status" -eq 0 ]
  [[ "$output" == *"result output here"* ]]
  [[ "$output" == *"└─"* ]]
}

@test "tool_result with array content is rendered" {
  run bash "$FORMAT_TRANSCRIPT_SCRIPT" \
    <<< '{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"array output"}]}'
  [ "$status" -eq 0 ]
  [[ "$output" == *"array output"* ]]
}

@test "result event renders a summary line with turn count" {
  run bash "$FORMAT_TRANSCRIPT_SCRIPT" \
    <<< '{"type":"result","subtype":"success","is_error":false,"num_turns":3,"total_cost_usd":0.05,"duration_ms":1200,"result":"SPINDRIFT_OUTCOME issue=7 pr=https://github.com/o/r/pull/1 status=ready note=fake","session_id":"fake"}'
  [ "$status" -eq 0 ]
  [[ "$output" == *"3 turns"* ]]
  [[ "$output" == *"─── "* ]]
}

@test "result event shows singular 'turn' for a single turn" {
  run bash "$FORMAT_TRANSCRIPT_SCRIPT" \
    <<< '{"type":"result","subtype":"success","is_error":false,"num_turns":1,"total_cost_usd":0.01,"duration_ms":500,"result":"done","session_id":"fake"}'
  [ "$status" -eq 0 ]
  [[ "$output" == *"1 turn"* ]]
  [[ "$output" != *"1 turns"* ]]
}

@test "long tool input is truncated to at most 130 chars in the glyph line" {
  local long_cmd
  long_cmd="$(printf 'x%.0s' {1..200})"
  run bash "$FORMAT_TRANSCRIPT_SCRIPT" \
    <<< "{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"tool_use\",\"id\":\"t1\",\"name\":\"Bash\",\"input\":{\"command\":\"${long_cmd}\"}}]}}"
  [ "$status" -eq 0 ]
  # Output line with glyph must be shorter than the original 200-char input
  line_with_glyph="$(printf '%s\n' "$output" | grep '⏺')"
  [ "${#line_with_glyph}" -lt 200 ]
  [[ "$line_with_glyph" == *"…"* ]]
}

@test "unknown event type is silently skipped, no crash" {
  run bash "$FORMAT_TRANSCRIPT_SCRIPT" \
    <<< '{"type":"future_event_type","data":"some value"}'
  [ "$status" -eq 0 ]
}

@test "malformed JSON is silently skipped, no crash" {
  run bash "$FORMAT_TRANSCRIPT_SCRIPT" \
    <<< 'not json at all { { {'
  [ "$status" -eq 0 ]
}

@test "empty input produces no output" {
  run bash "$FORMAT_TRANSCRIPT_SCRIPT" <<< ""
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "multiple events are all rendered in order" {
  local input
  input="$(printf '%s\n' \
    '{"type":"system","subtype":"init","session_id":"s"}' \
    '{"type":"assistant","message":{"content":[{"type":"text","text":"working on it"}]}}' \
    '{"type":"result","subtype":"success","num_turns":2,"total_cost_usd":0.02,"duration_ms":800,"result":"done","session_id":"s"}')"
  run bash "$FORMAT_TRANSCRIPT_SCRIPT" <<< "$input"
  [ "$status" -eq 0 ]
  [[ "$output" == *"working on it"* ]]
  [[ "$output" == *"─── "* ]]
  # System event must NOT add visible text
  [[ "$output" != *"system"* ]]
  [[ "$output" != *"init"* ]]
}

@test "mixed content block (text then tool_use) renders both" {
  local event
  event='{"type":"assistant","message":{"content":[{"type":"text","text":"Let me check:"},{"type":"tool_use","id":"t2","name":"Read","input":{"file_path":"/foo"}}]}}'
  run bash "$FORMAT_TRANSCRIPT_SCRIPT" <<< "$event"
  [ "$status" -eq 0 ]
  [[ "$output" == *"Let me check:"* ]]
  [[ "$output" == *"Read"* ]]
  [[ "$output" == *"⏺"* ]]
}
