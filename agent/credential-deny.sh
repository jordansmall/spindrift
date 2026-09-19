#!/usr/bin/env bash
# PreToolUse hook (issue #1909, spec #1907): denies a Read/Bash call naming a
# known credential path. It is a home-wide hook, not a permissions.deny rule,
# because the Box runs the Driver with --dangerously-skip-permissions, which
# bypasses permission rules; hooks are evaluated separately and still fire.
set -euo pipefail

# Matches $1 (a Read call's file_path, or a Bash call's raw command text)
# anywhere in the text, not just as a suffix, because a Bash command can pipe,
# redirect, or copy a credential path mid-command, and a bare relative form
# ("cat .env") must be caught too. The boundary classes stop a longer name
# that merely contains the letters ("config.env").

# The optional .env variant suffix also denies safe committed templates such
# as .env.example. Deliberate (issue #1921): an allowlist of "safe" suffixes
# would miss real conventions, and dropping back to bare .env reopens the
# #1907 gap. Auth here is environment-based, so the Driver never needs any
# dotenv file, and the cost of over-blocking is an occasional odd denial.

# This is a text match with no shell state, so a path split across arguments
# ("cd ~/.claude && cat .credentials.json") or built from a variable escapes
# it, the same gap reject-background-bash.sh accepts. Matching is
# case-sensitive because these are literal filenames the Driver writes.
targets_credential_path() {
  local s="$1"
  local lead='[^[:alnum:]_.-]'
  local trail='[^[:alnum:]_./-]'
  [[ "$s" =~ (^|$lead)\.claude/\.credentials\.json($|$trail) ]] && return 0
  [[ "$s" =~ (^|$lead)\.config/gh/hosts\.yml($|$trail) ]] && return 0
  [[ "$s" =~ (^|$lead)\.env(\.[[:alnum:]_-]+)?($|$trail) ]] && return 0
  return 1
}

input="$(cat)"

# Malformed stdin makes the extraction come back empty, which reads as "not a
# matching call" below and allows it. `|| true` keeps a jq parse failure from
# tripping `set -e` on the assignment itself.
tool_name="$(jq -r '.tool_name // empty' 2>/dev/null <<<"$input" || true)"
if [ "$tool_name" != "Read" ] && [ "$tool_name" != "Bash" ]; then
  exit 0
fi

# Claude Code expects exit 0 either way: the decision is in the JSON printed
# to stdout, and printing nothing means allow.
deny() {
  jq -n --arg reason "$1" '{
    hookSpecificOutput: {
      hookEventName: "PreToolUse",
      permissionDecision: "deny",
      permissionDecisionReason: $reason
    }
  }'
  exit 0
}

target="$(jq -r '.tool_input.file_path // .tool_input.command // empty' 2>/dev/null <<<"$input" || true)"

if [ -n "$target" ] && targets_credential_path "$target"; then
  deny "Reading credential files is rejected in headless Box runs -- auth is environment-based, so the Driver never needs to read this file directly."
fi

exit 0
