#!/usr/bin/env bash
# PreToolUse hook (#1609, widened by #1620): denies a Bash call that
# backgrounds, via run_in_background or in the shell. run_in_background is a
# tool parameter, not a tool name, so --disallowedTools cannot strip it, and a
# headless Box run exits without waiting for the backgrounded work (#1542).
set -euo pipefail

# Masks quoted and backslash-escaped characters with 'x' so the caller can
# match operators like & without tripping on a quoted or escaped literal such
# as "foo & bar" or foo\&bar.
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
      # Before a metacharacter the backslash neutralizes the operator, so mask
      # both characters. Before an ordinary letter or digit it is a no-op
      # (\setsid really invokes setsid), and masking it would hide the keyword
      # from command_backgrounds, so keep the literal character there. The
      # masked string is then shorter than the input.
      local next="${cmd:i+1:1}"
      if [[ "$next" =~ [[:alnum:]] ]]; then
        masked+="$next"
      else
        masked+="xx"
      fi
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

# True if the command backgrounds a process at the shell level. The strip below
# removes &&, >&, <&, &> and |&, which are foreground fd juggling. Two accepted
# false positives: a bitwise & in $((...)) and an & inside a heredoc body both
# read as the background operator, since mask_command models neither. Both deny
# a safe call, which is the fail-closed direction.
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

  # nohup survives the calling shell exiting, the hazard this hook exists to
  # catch, so it is rejected even without an accompanying &.
  local nohup_re='(^|[[:space:];|(])nohup([[:space:]]|$)'
  [[ "$masked" =~ $nohup_re ]] && return 0

  # setsid detaches into a new session, surviving the calling shell the same
  # way nohup does.
  local setsid_re='(^|[[:space:];|(])setsid([[:space:]]|$)'
  [[ "$masked" =~ $setsid_re ]] && return 0

  # coproc spawns a bash coprocess as a background job. The regex covers both
  # the named and unnamed forms.
  local coproc_re='(^|[[:space:];|(])coproc([[:space:]]|$)'
  [[ "$masked" =~ $coproc_re ]] && return 0

  # #1635 closed setsid and coproc. Other ways to detach from the calling
  # session, such as disown, at, systemd-run, screen -d and tmux
  # new-session -d, stay deliberately out of scope (#1620's deferral list).
  return 1
}

input="$(cat)"

# Non-JSON stdin makes these extractions come back empty, which reads as a
# non-matching call and allows it, the same as any other non-match. jq's parse
# error is silenced so a stray payload does not spam the transcript.
if [ "$(jq -r '.tool_name // empty' 2>/dev/null <<<"$input")" != "Bash" ]; then
  exit 0
fi

# Claude Code expects exit 0 here: the denial travels in the JSON, not the
# exit code. Hooks run independently of the permission system, so this still
# fires under the Box's --dangerously-skip-permissions.
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
