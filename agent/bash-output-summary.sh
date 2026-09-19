#!/usr/bin/env bash
# PostToolUse hook (issue #1988): once the log that bash-output-tee.sh wrote
# grows past the inline bound, replace the Bash tool result with the exit code,
# the log path, and the last few KB. Build and test errors sit near the end,
# unlike Claude Code's own start-only overflow preview; the log stays on disk.
set -euo pipefail

input="$(cat)"

if [ "$(jq -r '.tool_name // empty' 2>/dev/null <<<"$input" || true)" != "Bash" ]; then
  exit 0
fi

command="$(jq -r '.tool_input.command // empty' 2>/dev/null <<<"$input" || true)"

# Each hook runs as its own process with no shared memory, so this one recovers
# the log path from the `} 2>&1 | tee -- "<path>"` line bash-output-tee.sh wrote.
# Match that whole prefix and take the last one: the user's own command sits
# inside the wrapper unmodified, so a command that itself pipes through
# `tee -- "..."` would otherwise collide with our marker.
log_file="$(grep -oE '^\} 2>&1 \| tee -- "[^"]+"' <<<"$command" | tail -n1 | sed -E 's/^.*tee -- "//; s/"$//' || true)"

if [ -z "$log_file" ] || [ ! -f "$log_file" ]; then
  exit 0
fi

tail_bytes="${BASH_OUTPUT_SUMMARY_TAIL_BYTES:-4096}"
size="$(stat -c%s "$log_file" 2>/dev/null || echo 0)"

if [ "$size" -le "$tail_bytes" ]; then
  exit 0
fi

exit_code="$(cat "$log_file.exit" 2>/dev/null || echo "unknown")"
tail_text="$(tail -c "$tail_bytes" "$log_file")"

summary="$(
  printf '%s\n' \
    "exit code: $exit_code" \
    "log file: $log_file" \
    "--- last $tail_bytes bytes of output ---" \
    "$tail_text"
)"

jq -n --arg summary "$summary" '{
  hookSpecificOutput: {
    hookEventName: "PostToolUse",
    updatedToolOutput: $summary
  }
}'
