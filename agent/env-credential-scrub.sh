#!/usr/bin/env bash
# PreToolUse hook (issue #1927, spec #1907): re-introduces the subprocess
# env-credential scrub #1926 reverted, without reproducing what broke it.
#
# #1909 originally baked `export CLAUDE_CODE_SUBPROCESS_ENV_SCRUB=1` into the
# entrypoint so Claude Code's own built-in subprocess isolation would strip
# ANTHROPIC_API_KEY / CLAUDE_CODE_OAUTH_TOKEN from every spawned subprocess.
# #1926 found that feature bundles two effects that make it unusable inside
# the Box: it forces the Driver's permission mode to `default` (every Bash
# call then "requires approval", and a headless Box has no interactive
# approver to give it), and it wraps every Bash subprocess in Claude Code's
# own nested bwrap sandbox -- which cannot mount /proc inside the Box's own
# outer bwrap sandbox. The /proc failure was reproduced directly here (not
# just inferred from the production postmortem): invoking bubblewrap for a
# nested `--proc /proc` mount inside this harness's own bwrap-sandboxed
# environment fails with the identical "bwrap: Can't mount proc on
# /newroot/proc: Operation not permitted", across every
# --unshare-user/--disable-userns combination tried -- a kernel/capability
# boundary on nested mount-namespace /proc mounts, not a bwrap flag this
# harness can tune away.
#
# This hook re-introduces the protection a different way, via
# hookSpecificOutput.updatedInput (a documented PreToolUse capability,
# independent of permissionDecision -- Claude Code's own hook-output schema
# carries them as two unrelated optional fields on the same object -- that
# lets a hook rewrite a tool call's input before it runs): every Bash call
# is rewritten to `unset ANTHROPIC_API_KEY CLAUDE_CODE_OAUTH_TOKEN` before
# the Driver's original command. This is an actual removal, not a denylist
# -- the variables are gone from that subprocess's real environment. `env`,
# `printenv`, `set`, `export -p`, a direct `$VAR` expansion, and reading the
# subprocess's own /proc/self/environ (once the read happens in a process
# that forked *after* the unset, which every ordinary command in the
# rewritten script does) all come up empty as a structural consequence, no
# per-command denylist to maintain or route around.
#
# Deliberately no `permissionDecision` on the rewrite path -- omitted, not
# set to "allow" -- the same "prints nothing/no explicit opinion" posture
# reject-background-bash.sh and credential-deny.sh use for a call they don't
# deny. Three PreToolUse hooks share the Bash matcher (this one alongside
# those two); asserting an explicit "allow" here would risk being read as
# this hook's opinion overriding a sibling's "deny" on the very same call,
# which an unopinionated updatedInput-only response can't do.
#
# Two vectors this rewrite alone cannot close, both handled by an outright
# deny instead of a rewrite:
#   1. A Bash subprocess reading a *different* same-uid process's
#      /proc/<pid>/environ -- e.g. the Driver's own, which legitimately
#      still holds the credential for its own API auth (the scrub only ever
#      applies to spawned subprocesses, never the Driver itself). No
#      rewrite of the current call can scrub another process's memory.
#   2. A Bash subprocess reading its *own* /proc/<pid>/environ via a
#      pid-in-source-text form other than `self`/`thread-self` -- `$$`,
#      `$BASHPID`, `$PPID`, a wildcard -- before any command has forked
#      since the unset. Confirmed empirically: `unset` clears the live
#      /proc/self/environ Linux exposes to a process that forked *after*
#      the unset (a fresh execve() env block), but the pre-existing process
#      Claude Code invoked bash *as* keeps its original, unmodified
#      env_start/env_end memory region for its own lifetime -- `cat
#      /proc/$$/environ`, read by a forked child but naming the still-alive
#      parent shell's own pid, reads that original region and leaks the
#      credential regardless of the `unset` that ran inside it. Since the
#      hook only ever sees static command text (never a resolved pid), it
#      can't distinguish a safe self-reference from an unsafe one -- so
#      every `/proc/.../environ` reference is denied outright, without
#      trying to enumerate which pid-forms are "safe".
set -euo pipefail

CREDENTIAL_VARS="ANTHROPIC_API_KEY CLAUDE_CODE_OAUTH_TOKEN"

# True if $1 (a Bash call's raw command text) references any
# /proc/<anything>/environ path at all. A plain substring match, not an
# enumeration of pid forms (self, thread-self, a literal digit, $$,
# $BASHPID, $PPID, a `*`/`[0-9]*` glob, ...): the hook only ever sees
# static, unexpanded command text, so whatever sits between "/proc/" and
# "/environ" in that text is opaque to it regardless of what pid it
# resolves to at run time -- matching only specific pid forms and allowing
# the rest is exactly the gap that let `cat /proc/$$/environ` slip past an
# earlier, narrower version of this check. Same fail-closed posture as
# credential-deny.sh: a text match, not a shell interpreter, so a path
# built from an indirect variable reference isn't caught.
reads_any_environ() {
  local s="$1"
  [[ "$s" == *"/proc/"*"/environ"* ]] && return 0
  return 1
}

input="$(cat)"

# Malformed/non-JSON stdin makes these extractions come back empty (jq's own
# parse error goes to stderr, silenced), which reads as "not a matching
# call" below -- the same fail-open-to-allow outcome as any other non-match.
# Mirrors credential-deny.sh's handling of the same case.
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
