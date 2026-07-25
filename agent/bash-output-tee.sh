#!/usr/bin/env bash
# PreToolUse hook (issue #1988): tees every Bash call's stdout+stderr to a
# per-command file on disk while the real exit code still propagates to
# Claude Code, so PostToolUse (bash-output-summary.sh) can hand back a
# bounded, error-oriented tail instead of the full output -- uniformly, for
# every command, not just the overflow case Claude Code's own
# BASH_MAX_OUTPUT_LENGTH spillover (issue #1987) already covers.
#
# Reads the PreToolUse JSON payload from stdin and, for a matching call,
# rewrites tool_input.command via hookSpecificOutput.updatedInput (the same
# documented PreToolUse capability agent/env-credential-scrub.sh uses) to
# wrap the original command in a group that tees combined stdout+stderr to
# the log file, then re-exits with the original command's own status code
# (captured via PIPESTATUS before tee's own exit status can shadow it). A
# non-matching call prints nothing, read as "allow, no opinion" -- same
# posture as the other Bash-matched PreToolUse hooks.
#
# One other PreToolUse hook already shares the Bash matcher and rewrites
# input independently (env-credential-scrub.sh, issue #1927): Claude Code
# does not document how multiple hooks' updatedInput responses for the same
# call compose (last-writer-wins, a merge, or something else), so this
# hook's own rewrite duplicates env-credential-scrub.sh's `unset
# ANTHROPIC_API_KEY CLAUDE_CODE_OAUTH_TOKEN` prefix (CREDENTIAL_VARS below)
# instead of relying on that hook's rewrite surviving alongside this one.
# Redundant if both compose, but correct either way -- unlike leaving the
# unset to chance, which silently drops the credential scrub if only the
# last-registered hook's updatedInput wins.
set -euo pipefail

CREDENTIAL_VARS="ANTHROPIC_API_KEY CLAUDE_CODE_OAUTH_TOKEN"

input="$(cat)"

# Malformed/non-JSON stdin makes these extractions come back empty (jq's own
# parse error goes to stderr, silenced), which reads as "not a matching
# call" below -- the same fail-open-to-allow outcome as any other non-match.
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

# The closing brace lands on its own line so a command whose last line is a
# comment (no trailing newline before it) can't swallow it, and a blank
# line separates the command from that brace so a command whose last line
# ends in a line-continuation backslash can't splice the brace line into
# itself either -- the blank line absorbs the continuation and, having no
# trailing backslash itself, ends it there. ec captures the
# original command's exit status via PIPESTATUS before tee's own status
# would otherwise shadow it, and is both the value re-exited (so the real
# exit code still reaches Claude Code) and the value written to
# "$log_file.exit" for bash-output-summary.sh to read back.
# shellcheck disable=SC2016 # single-quoted $-expressions below are intentionally
# unexpanded here -- they're written verbatim into the rewritten command for
# the Bash tool to expand later, not expanded by this hook.
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
