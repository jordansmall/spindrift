#!/usr/bin/env bash
# PreToolUse hook (issue #1909, spec #1907): rejects a Read/Bash tool call
# targeting a known credential path before it executes. Registered as a
# home-wide Claude Code hook (not a permissions.deny rule) because the Box
# invokes the Driver with --dangerously-skip-permissions, which bypasses the
# permission-rule system entirely; hooks are their own enforcement layer,
# evaluated independently, so this still fires under that flag.
#
# Reads the PreToolUse JSON payload from stdin and, for a matching call,
# prints a hookSpecificOutput JSON denial to stdout; Claude Code always
# expects exit 0 here -- the decision is carried in the JSON, not the exit
# code. A non-matching call prints nothing, which Claude Code reads as
# "allow, no opinion".
set -euo pipefail

# True if $1 (a Read call's file_path, or a Bash call's raw command text)
# embeds one of the three denied credential paths anywhere, not just as a
# full-string suffix -- a Bash command piping, redirecting, or copying a
# credential path ("cat ~/.claude/.credentials.json | jq .", "... > /tmp/x",
# "cp ~/.claude/.credentials.json /tmp/leak") still names the path
# somewhere in the middle of the command text, not at its end, and a bare
# relative form ("cat .env", "cat .config/gh/hosts.yml" -- the cwd is often
# the Box's own $HOME or a clone under it) is caught the same as an
# absolute or tilde one. Each alternative requires a non-path-component
# character (anything but alnum/"_"/"."/"-", or start of string) immediately
# before it and one (same set, plus "/", or end of string) immediately
# after, so "/", "~", and a plain space all count as a valid boundary on
# either side, while a longer name that merely contains the same letters (a
# repo file named "config.env", or a command mentioning "environment.txt")
# does not. The .env alternative also accepts an optional ".<variant>"
# suffix (.env.local, .env.production, ...) -- real-world dotenv tooling
# splits secrets across those files the same way it does .env itself.
#
# Purely a text match against a single path/command argument: it doesn't
# reconstruct shell state, so a path split across two Bash arguments (`cd
# ~/.claude && cat .credentials.json`, or one built from a variable) isn't
# caught. Accepted the same way reject-background-bash.sh accepts its own
# parsing gaps -- fail-closed on the direct form, not a shell interpreter.
# The credentials.json and hosts.yml alternatives require their ".claude/"
# or ".config/gh/" component too, so unlike the bare-relative .env case a
# bare ".credentials.json" alone (no leading .claude/) isn't matched --
# that's the same cd-first gap above, not a separate hole. Matching is also
# case-sensitive (deliberately: these are literal filenames the Driver's
# own tooling writes, never user-supplied casing).
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

# Malformed/non-JSON stdin makes these extractions come back empty (jq's own
# parse error goes to stderr, silenced), which reads as "not a matching call"
# below -- the same fail-open-to-allow outcome as any other non-match. `||
# true` keeps a jq parse failure from tripping `set -e` on the assignment
# itself (mirrors reject-background-bash.sh's inline-in-conditional style,
# which sidesteps this the same way).
tool_name="$(jq -r '.tool_name // empty' 2>/dev/null <<<"$input" || true)"
if [ "$tool_name" != "Read" ] && [ "$tool_name" != "Bash" ]; then
  exit 0
fi

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
