#!/usr/bin/env bash
# Reads Claude Code stream-json (NDJSON) from stdin and renders it on the host:
#   format-transcript.sh < .spindrift/logs/issue-<n>.log
# Keep it out of the entrypoint's live pipe: outcome.Classify scans that log for
# transient-failure markers (#123), so it must stay byte-exact raw stream-json.
set -euo pipefail

BOLD=$'\033[1m'
DIM=$'\033[2m'
RESET=$'\033[0m'

_trunc() {
  local s="$1" n="${2:-120}"
  if [ "${#s}" -le "$n" ]; then
    printf '%s' "$s"
  else
    printf '%s\342\200\246' "${s:0:$((n - 1))}"
  fi
}

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

_render_event() {
  local line="$1"
  [ -n "$line" ] || return 0
  local type=""
  type="$(printf '%s' "$line" | jq -r '.type // empty' 2>/dev/null)" || return 0
  [ -n "$type" ] || return 0
  case "$type" in
    system)
      ;;  # Session-init noise.
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
