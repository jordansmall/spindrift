#!/usr/bin/env bash
# PreToolUse hook (issue #1927, spec #1907): strips the model-auth credentials
# from every Bash subprocess the Driver spawns.
#
# Claude Code's own CLAUDE_CODE_SUBPROCESS_ENV_SCRUB is unusable inside the Box
# (#1926): it forces the Driver's permission mode to `default` (every Bash call
# then "requires approval", and a headless Box has no interactive approver), and
# it wraps every Bash subprocess in a nested bwrap sandbox that cannot mount
# /proc inside the Box's own outer bwrap sandbox. That failure was reproduced
# directly here, not merely inferred: a nested `--proc /proc` mount fails with
# "bwrap: Can't mount proc on /newroot/proc: Operation not permitted" across
# every --unshare-user/--disable-userns combination tried -- a kernel/capability
# boundary on nested mount-namespace /proc mounts, not a flag this harness can
# tune away.
#
# So every Bash call is instead rewritten, via hookSpecificOutput.updatedInput
# (a documented PreToolUse capability, independent of permissionDecision), to
# `unset ANTHROPIC_API_KEY CLAUDE_CODE_OAUTH_TOKEN` before the Driver's original
# command. This is an actual removal, not a denylist -- `env`, `printenv`,
# `set`, `export -p`, a direct `$VAR` expansion, and the subprocess's own
# /proc/self/environ all come up empty as a structural consequence, with no
# per-command denylist to maintain or route around.
#
# Deliberately no `permissionDecision` on the rewrite path -- omitted, not set
# to "allow". Three PreToolUse hooks share the Bash matcher; asserting an
# explicit "allow" here would risk being read as this hook's opinion overriding
# a sibling's "deny" on the very same call, which an unopinionated
# updatedInput-only response cannot do.
#
# Two vectors this rewrite alone cannot close, both handled by an outright deny:
#   1. A Bash subprocess reading a *different* same-uid process's
#      /proc/<pid>/environ -- e.g. the Driver's own, which legitimately still
#      holds the credential for its own API auth (the scrub only ever applies to
#      spawned subprocesses, never the Driver itself). No rewrite of the current
#      call can scrub another process's memory.
#   2. A Bash subprocess reading its own /proc/<pid>/environ under a pid form
#      other than `self`/`thread-self` -- `$$`, `$BASHPID`, `$PPID`, a wildcard.
#      Confirmed empirically: `unset` clears the env block a process that forked
#      *after* it sees, but the still-alive parent shell keeps its original
#      env_start/env_end region, so `cat /proc/$$/environ` read by a forked child
#      leaks the credential regardless. The hook sees only static command text,
#      never a resolved pid, so every `/proc/.../environ` reference is denied
#      outright rather than trying to enumerate which pid forms are "safe".
set -euo pipefail

CREDENTIAL_VARS="ANTHROPIC_API_KEY CLAUDE_CODE_OAUTH_TOKEN"

# True if $1 (a Bash call's raw command text) references any
# /proc/<anything>/environ path at all. A plain substring match, not an
# enumeration of pid forms: the hook only ever sees static, unexpanded command
# text, so whatever sits between "/proc/" and "/environ" is opaque to it
# regardless of what pid it resolves to -- matching only specific pid forms and
# allowing the rest is exactly the gap that let `cat /proc/$$/environ` slip past
# an earlier, narrower version of this check. Same fail-closed posture as
# credential-deny.sh: a text match, not a shell interpreter.
reads_any_environ() {
  local s="$1"
  [[ "$s" == *"/proc/"*"/environ"* ]] && return 0
  return 1
}

input="$(cat)"

# Malformed/non-JSON stdin makes these extractions come back empty (jq's own
# parse error goes to stderr, silenced), which reads as "not a matching call"
# below -- the same fail-open-to-allow outcome as any other non-match.
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

new_command="unset $CREDENTIAL_VARS; $command"

jq -n --argjson orig "$tool_input" --arg cmd "$new_command" '{
  hookSpecificOutput: {
    hookEventName: "PreToolUse",
    updatedInput: ($orig + { command: $cmd })
  }
}'
