#!/usr/bin/env bash
# PreToolUse hook (issue #1988): rewrites a Bash call's command through
# updatedInput so stdout and stderr are teed to a log that
# bash-output-summary.sh tails, for every command and not just the
# BASH_MAX_OUTPUT_LENGTH overflow case (issue #1987).
set -euo pipefail

# env-credential-scrub.sh (issue #1927) unsets the credential-vars list when it
# rewrites the same call, but Claude Code does not document how two hooks' updatedInput compose,
# so this hook repeats the unset instead of trusting that one to survive.
CREDENTIAL_VARS_FILE="${CREDENTIAL_VARS_FILE:-$(dirname "${BASH_SOURCE[0]}")/credential-vars}"

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

# Exit 2 blocks the call, so a lost list never lets it run unscrubbed.
CREDENTIAL_VARS="$(grep -vE '^[[:space:]]*(#|$)' "$CREDENTIAL_VARS_FILE" 2>/dev/null | tr '\n' ' ' | sed 's/ *$//' || true)"
if [ -z "$CREDENTIAL_VARS" ]; then
  echo "bash-output-tee.sh: credential variable list $CREDENTIAL_VARS_FILE is missing or empty" >&2
  exit 2
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
