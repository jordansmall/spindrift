#!/usr/bin/env bash
# PreToolUse hook (issue #1609): rejects a Bash tool call carrying
# run_in_background: true before it executes. run_in_background is a
# parameter of the Bash tool call, not a tool name, so it cannot be stripped
# from the Driver's tool surface the way lib/drivers/claude.nix's
# --disallowedTools strips ScheduleWakeup/Cron*/RemoteTrigger/Monitor -- a
# headless Box run has no harness watching for a later re-invocation, so a
# backgrounded gate whose turn ends before it finishes silently loses the run
# (#1542: the Driver backgrounded its test gate, called ScheduleWakeup, and
# the headless runner exited seconds later with zero work pushed).
#
# Reads the PreToolUse JSON payload from stdin and, for a matching call,
# prints a hookSpecificOutput JSON denial to stdout; Claude Code always
# expects exit 0 here -- the decision is carried in the JSON, not the exit
# code. A non-matching call prints nothing, which Claude Code reads as
# "allow, no opinion". Hooks are their own enforcement layer, evaluated
# independently of the permission system, so this still fires under
# --dangerously-skip-permissions (the Box's own invocation flag) exactly as
# it would under any other permission mode.
#
# #1620 widens this beyond the structured run_in_background parameter: a
# foreground Bash call can still self-background at the shell level (a
# trailing/mid-command &, or nohup), which run_in_background never sees.
# command_backgrounds() below parses tool_input.command for that.
set -euo pipefail

# Masks quoted and backslash-escaped characters in a shell command string
# with 'x' (preserving length/word-boundaries) so the caller can pattern-match
# operators like & without tripping on one that's just a quoted/escaped
# literal, e.g. "foo & bar" or foo\&bar.
mask_command() {
  local cmd="$1"
  local -i i=0
  local -i len=${#cmd}
  local -i in_squote=0
  local -i in_dquote=0
  local ch backslash=$'\\'
  local masked=""

  while (( i < len )); do
    ch="${cmd:i:1}"

    if (( in_squote )); then
      if [[ "$ch" == "'" ]]; then
        in_squote=0
        masked+="'"
      else
        masked+="x"
      fi
      i=$((i + 1))
      continue
    fi

    if (( in_dquote )); then
      if [[ "$ch" == "$backslash" ]]; then
        masked+="xx"
        i=$((i + 2))
        continue
      fi
      if [[ "$ch" == '"' ]]; then
        in_dquote=0
        masked+='"'
      else
        masked+="x"
      fi
      i=$((i + 1))
      continue
    fi

    if [[ "$ch" == "$backslash" ]]; then
      masked+="xx"
      i=$((i + 2))
      continue
    fi
    if [[ "$ch" == "'" ]]; then
      in_squote=1
      masked+="'"
      i=$((i + 1))
      continue
    fi
    if [[ "$ch" == '"' ]]; then
      in_dquote=1
      masked+='"'
      i=$((i + 1))
      continue
    fi

    masked+="$ch"
    i=$((i + 1))
  done

  printf '%s' "$masked"
}

# True if the command backgrounds a process at the shell level: a standalone
# & control operator (trailing, or mid-command as in "foo & bar") that isn't
# part of &&, a >&/<&/&> redirection token (2>&1, >&2, &>file are all
# ordinary foreground fd-juggling, not backgrounding), or the |& pipe
# operator (shorthand for 2>&1 |, also foreground).
#
# Two known false-positive gaps, accepted rather than chased: a literal & in
# arithmetic context ($((3 & 4)), bitwise-and) reads as the background
# operator since mask_command doesn't special-case $((...)), and the same
# for a literal & inside a heredoc body, since mask_command isn't
# line-aware. Both deny a call that was actually safe, which is the same
# fail-closed direction as every other edge this hook doesn't model --
# rerunning the command without the & literal (or via a different
# construct) unblocks it.
command_backgrounds() {
  local cmd="$1"
  local masked
  masked="$(mask_command "$cmd")"

  local stripped="$masked"
  stripped="${stripped//&&/}"
  stripped="${stripped//>&/}"
  stripped="${stripped//<&/}"
  stripped="${stripped//&>/}"
  stripped="${stripped//|&/}"
  if [[ "$stripped" == *"&"* ]]; then
    return 0
  fi

  # nohup survives the calling shell exiting, which is exactly the
  # session-outlives-the-turn hazard this hook exists to catch, so it's
  # rejected on its own even without an accompanying &.
  local nohup_re='(^|[[:space:];|(])nohup([[:space:]]|$)'
  [[ "$masked" =~ $nohup_re ]] && return 0

  # setsid detaches the process into a new session, surviving the calling
  # shell exiting the same way nohup does -- rejected on its own.
  local setsid_re='(^|[[:space:];|(])setsid([[:space:]]|$)'
  [[ "$masked" =~ $setsid_re ]] && return 0

  # coproc spawns a bash coprocess in a backgrounded job (named or
  # unnamed: "coproc NAME { ...; }" and "coproc { ...; }" are both valid),
  # the same fail-open shell-level detachment as & and nohup above.
  local coproc_re='(^|[[:space:];|(])coproc([[:space:]]|$)'
  [[ "$masked" =~ $coproc_re ]] && return 0

  # setsid and coproc are two concrete detachment mechanisms this hook now
  # closes (#1635); other tools that decouple a process from the calling
  # session -- disown, at, systemd-run, screen -d, tmux new-session -d, etc.
  # -- are a deliberately out-of-scope judgment call left for future work
  # (#1620's original deferral list).
  return 1
}

input="$(cat)"

# Malformed/non-JSON stdin makes these extractions come back empty (jq's own
# parse error goes to stderr), which reads as "not a matching call" below --
# the same fail-open-to-allow outcome as any other non-match, not a distinct
# bypass. Silenced here so a stray non-JSON payload doesn't spam the
# transcript with jq parse-error noise.
if [ "$(jq -r '.tool_name // empty' 2>/dev/null <<<"$input")" != "Bash" ]; then
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

if [ "$(jq -r '.tool_input.run_in_background // false' 2>/dev/null <<<"$input")" = "true" ]; then
  deny "Backgrounded Bash calls are rejected in headless Box runs -- rerun the command in the foreground and block on it until it completes."
fi

command="$(jq -r '.tool_input.command // empty' 2>/dev/null <<<"$input")"

if [ -n "$command" ] && command_backgrounds "$command"; then
  deny "Bash commands that self-background or detach (e.g. a trailing & or a mid-command &, nohup, setsid, or coproc) are rejected in headless Box runs -- rerun the command in the foreground and block on it until it completes."
fi

exit 0
