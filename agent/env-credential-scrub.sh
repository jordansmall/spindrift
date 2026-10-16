#!/usr/bin/env bash
# PreToolUse hook (issue #1927, spec #1907). Claude Code's built-in
# CLAUDE_CODE_SUBPROCESS_ENV_SCRUB (#1909) is unusable in the Box (#1926): it
# forces permission mode to `default`, which a headless Box cannot approve,
# and its nested bwrap sandbox cannot mount /proc inside the Box's own bwrap.

# So this hook rewrites every Bash call through
# hookSpecificOutput.updatedInput to unset the credentials first. That really
# removes them from the subprocess environment instead of hiding them behind
# a denylist that every new reader command would have to be added to.

# The rewrite path sets no `permissionDecision`, omitting it rather than
# saying "allow": three PreToolUse hooks share the Bash matcher, and an
# explicit allow here could read as overriding a sibling hook's deny on the
# same call.

# The rewrite cannot close two paths, so the code below denies them outright:
# a subprocess reading another same-uid process's /proc/<pid>/environ (the
# Driver's own still holds the credential for its API auth), and one reading
# its own through a pid form other than self or thread-self, since the live
# env block only clears for a process that forked after the unset.
set -euo pipefail

# Baked beside the hook in the image; CREDENTIAL_VARS_FILE overrides it where the
# hook runs from a lone store path (the bats suite).
CREDENTIAL_VARS_FILE="${CREDENTIAL_VARS_FILE:-$(dirname "${BASH_SOURCE[0]}")/credential-vars}"

# This matches a plain substring rather than enumerating pid forms: the hook
# only ever sees static, unexpanded command text, and matching specific forms
# while allowing the rest is the gap that let `cat /proc/$$/environ` slip past
# an earlier version of this check. Like credential-deny.sh it matches text
# rather than interpreting shell, so it misses an indirectly built path.
reads_any_environ() {
  local s="$1"
  [[ "$s" == *"/proc/"*"/environ"* ]] && return 0
  return 1
}

input="$(cat)"

# Malformed stdin makes this extraction come back empty, which reads as "not
# a matching call" and allows it, the same way credential-deny.sh does.
if [ "$(jq -r '.tool_name // empty' 2>/dev/null <<<"$input" || true)" != "Bash" ]; then
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

tool_input="$(jq -c '.tool_input // empty' 2>/dev/null <<<"$input" || true)"
command="$(jq -r '.tool_input.command // empty' 2>/dev/null <<<"$input" || true)"

if [ -z "$tool_input" ] || [ -z "$command" ]; then
  exit 0
fi

if reads_any_environ "$command"; then
  deny "Reading a /proc/<pid>/environ path is rejected in headless Box runs -- the Driver's own process still holds its model-auth credential to authenticate, and the hook can't tell a safe self-reference (\$\$, \$BASHPID) from an unsafe one from command text alone, so every form is denied outright."
fi

# Fail closed with a deny: a non-zero exit is only a non-blocking hook error
# under Claude Code and would let the call run with its credentials intact.
CREDENTIAL_VARS="$(grep -vE '^[[:space:]]*(#|$)' "$CREDENTIAL_VARS_FILE" 2>/dev/null | tr '\n' ' ' | sed 's/ *$//' || true)"
if [ -z "$CREDENTIAL_VARS" ]; then
  deny "The credential variable list at $CREDENTIAL_VARS_FILE is missing or names no variables, so this hook cannot scrub the Bash call's environment; refusing to run it unscrubbed."
fi

new_command="unset $CREDENTIAL_VARS; $command"

jq -n --argjson orig "$tool_input" --arg cmd "$new_command" '{
  hookSpecificOutput: {
    hookEventName: "PreToolUse",
    updatedInput: ($orig + { command: $cmd })
  }
}'
