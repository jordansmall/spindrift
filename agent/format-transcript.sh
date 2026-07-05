#!/usr/bin/env bash
# Host-side viewer: reads Claude Code stream-json (NDJSON) from stdin and renders
# each event as human-readable terminal output. Silently skips unknown event
# types and non-JSON lines, and never aborts on malformed input.
#
# Deliberately NOT in the entrypoint's live pipe: logs/issue-<n>.log must stay
# byte-exact raw stream-json because the launcher's outcome.Classify scans it for
# transient-failure markers (see #123). Run this on the host over the saved log:
#   format-transcript.sh < logs/issue-<n>.log
set -euo pipefail

BOLD=$'\033[1m'
DIM=$'\033[2m'
RESET=$'\033[0m'

# Print at most N characters of string S, appending … if truncated.
_trunc() {
  local s="$1" n="${2:-120}"
  if [ "${#s}" -le "$n" ]; then
    printf '%s' "$s"
  else
    printf '%s\342\200\246' "${s:0:$((n - 1))}"
  fi
}

# Render one assistant content block (text or tool_use).
_render_content_block() {
  local block="$1"
  local ctype=""
  ctype="$(printf '%s' "$block" | jq -r '.type // empty' 2>/dev/null)" || return 0
  [ -n "$ctype" ] || return 0
  case "$ctype" in
    text)
      local text=""
      text="$(printf '%s' "$block" | jq -r '.text // empty' 2>/dev/null)" || return 0
      [ -n "$text" ] || return 0
      printf '%s\n' "$text"
      ;;
    tool_use)
      local name="" input_str=""
      name="$(printf '%s' "$block" | jq -r '.name // "?"' 2>/dev/null)" || return 0
      input_str="$(printf '%s' "$block" | jq -c '.input // {}' 2>/dev/null)" || input_str="{}"
      local line="${name}(${input_str})"
      printf '%s\xe2\x8f\xba %s%s\n' "$BOLD" "$(_trunc "$line" 120)" "$RESET"
      ;;
  esac
}

# Render one NDJSON event line; unknown types are silently ignored.
_render_event() {
  local line="$1"
  [ -n "$line" ] || return 0
  local type=""
  type="$(printf '%s' "$line" | jq -r '.type // empty' 2>/dev/null)" || return 0
  [ -n "$type" ] || return 0
  case "$type" in
    system)
      ;;  # session-init noise — skip
    assistant)
      local nblocks=0
      nblocks="$(printf '%s' "$line" | jq -r '(.message.content | length)' 2>/dev/null)" || return 0
      local i=0
      for (( i=0; i<nblocks; i++ )); do
        local block=""
        block="$(printf '%s' "$line" | jq -c ".message.content[$i]" 2>/dev/null)" || continue
        _render_content_block "$block" || true
      done
      ;;
    tool_result)
      local content=""
      content="$(printf '%s' "$line" | jq -r '
        if (.content | type) == "array" then
          [.content[] | select(.type == "text") | .text] | join(" ")
        elif (.content | type) == "string" then .content
        else empty
        end' 2>/dev/null)" || return 0
      [ -n "$content" ] || return 0
      printf '  %s\xe2\x94\x94\xe2\x94\x80 %s%s\n' "$DIM" "$(_trunc "$content" 120)" "$RESET"
      ;;
    result)
      local summary=""
      summary="$(printf '%s' "$line" | jq -r '
        [
          if .num_turns then
            "\(.num_turns) turn\(if .num_turns == 1 then "" else "s" end)"
          else empty end,
          if .total_cost_usd then
            "$\(.total_cost_usd | . * 10000 | round / 10000)"
          else empty end,
          if .duration_ms then
            "\(.duration_ms / 1000 | . * 10 | round / 10)s"
          else empty end
        ] | join(" · ")' 2>/dev/null)" || return 0
      printf '%s\xe2\x94\x80\xe2\x94\x80\xe2\x94\x80 %s \xe2\x94\x80\xe2\x94\x80\xe2\x94\x80\xe2\x94\x80\xe2\x94\x80\xe2\x94\x80%s\n' \
        "$BOLD" "${summary:-(done)}" "$RESET"
      ;;
  esac
}

while IFS= read -r line; do
  _render_event "$line" || true
done
