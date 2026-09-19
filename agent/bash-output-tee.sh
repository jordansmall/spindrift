#!/usr/bin/env bash
# PreToolUse hook (issue #1988): rewrites a Bash call's command through
# updatedInput so stdout and stderr are teed to a log that
# bash-output-summary.sh tails, for every command and not just the
# BASH_MAX_OUTPUT_LENGTH overflow case (issue #1987).
set -euo pipefail

# env-credential-scrub.sh (issue #1927) unsets these when it rewrites the same
# call, but Claude Code does not document how two hooks' updatedInput compose,
# so this hook repeats the unset instead of trusting that one to survive.
CREDENTIAL_VARS="ANTHROPIC_API_KEY CLAUDE_CODE_OAUTH_TOKEN"

input="$(cat)"

# Malformed stdin makes jq return empty, which reads as a non-matching call
# below and allows the command, the same as any other non-match.
if [ "$(jq -r '.tool_name // empty' 2>/dev/null <<<"$input" || true)" != "Bash" ]; then
  exit 0
fi

tool_input="$(jq -c '.tool_input // empty' 2>/dev/null <<<"$input" || true)"
command="$(jq -r '.tool_input.command // empty' 2>/dev/null <<<"$input" || true)"

if [ -z "$tool_input" ] || [ -z "$command" ]; then
  exit 0
fi

log_dir="${BASH_OUTPUT_TEE_DIR:-/tmp/spindrift-bash-output}"
mkdir -p "$log_dir"
log_file="$(mktemp "$log_dir/bash-XXXXXXXX.log")"

# The closing brace sits on its own line after a blank line so a command
# ending in a comment or in a line continuation cannot swallow or splice it.
# ec takes the command's exit status from PIPESTATUS, which tee would shadow.
# shellcheck disable=SC2016 # the single-quoted $-expressions below are written
# verbatim into the rewritten command for the Bash tool to expand later.
new_command="$(
  printf '%s\n' \
    '{' \
    "unset $CREDENTIAL_VARS" \
    "$command" \
    '' \
    "} 2>&1 | tee -- \"$log_file\"" \
    'ec="${PIPESTATUS[0]}"' \
    "printf '%s' \"\$ec\" > \"$log_file.exit\"" \
    'exit "$ec"'
)"

jq -n --argjson orig "$tool_input" --arg cmd "$new_command" '{
  hookSpecificOutput: {
    hookEventName: "PreToolUse",
    updatedInput: ($orig + { command: $cmd })
  }
}'
