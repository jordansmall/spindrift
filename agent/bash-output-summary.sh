#!/usr/bin/env bash
# PostToolUse hook (issue #1988): replaces a Bash tool result with a bounded,
# error-oriented tail once the log file bash-output-tee.sh (the paired
# PreToolUse hook) wrote crosses the inline bound -- an exit code, the log
# file path, and the last ~4 KB of output (build/test errors sit near the
# end, unlike Claude Code's own start-only overflow preview). The full
# output stays on disk the whole time; this only ever changes what enters
# the model's context. A call whose output stays under the bound is left
# untouched -- reading the file back would be a needless round-trip when
# the output already fits.
#
# Reads the PostToolUse JSON payload from stdin. tool_input.command is the
# command Claude Code actually ran -- i.e. bash-output-tee.sh's rewrite, if
# that hook fired for this call -- so the log file path is recovered by
# parsing the `} 2>&1 | tee -- "<path>"` line it wrote, rather than
# threading state between the two hook invocations (each hook is its own
# process with no shared memory). Anchored on the full `} 2>&1 | tee -- `
# prefix, not a bare `tee -- `, and on the LAST such line in the command:
# the user's own command text sits inside the wrapper unmodified, so a user
# command that itself pipes through `tee -- "..."` would otherwise collide
# with our own marker -- our own line is always both more specific (it
# includes the closing brace and redirect) and the last one in the
# rewritten script. A call with no such line (bash-output-tee.sh didn't
# match, e.g. a non-Bash tool) reads as "not one of ours": prints nothing,
# same "allow, no opinion" posture the other hooks use for a non-match.
set -euo pipefail

input="$(cat)"

if [ "$(jq -r '.tool_name // empty' 2>/dev/null <<<"$input" || true)" != "Bash" ]; then
  exit 0
fi

command="$(jq -r '.tool_input.command // empty' 2>/dev/null <<<"$input" || true)"

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
