#!/usr/bin/env bash
# PreToolUse hook (#1609, widened by #1620): denies a Bash call that
# backgrounds, via run_in_background or in the shell. run_in_background is a
# tool parameter, not a tool name, so --disallowedTools cannot strip it, and a
# headless Box run exits without waiting for the backgrounded work (#1542).
set -euo pipefail

# Masks quoted and backslash-escaped characters with 'x' so the caller can
# match operators like & without tripping on a quoted or escaped literal such
# as "foo & bar" or foo\&bar. Command substitution inside double quotes runs in
# bash, so $(...) and backticks there are scanned live, and $'...' honours \'.
# Returns 1 (after printing what it masked) when an open substitution,
# backtick, or $'...' remains anywhere on the stack at end of input, so the
# caller can fail closed. A lone ' or " outside any substitution does not:
# heredoc bodies routinely hold an apostrophe. Accepted false positive: a
# heredoc inside "$(...)" (git commit -m "$(cat <<'EOF' ...)") is scanned as
# code, so an apostrophe or & in its body denies, and an unbalanced $(, $' or
# backtick in any heredoc body's prose fails closed. Unmodeled fail-open:
# inside $(...) any ) closes the substitution, including a case pattern's a)
# or a ) in a # comment, so what follows up to the closing " is masked.
mask_command() {
  local cmd="$1"
  local -i i=0
  local -i len=${#cmd}
  # One char per open context, top last: p paren (from $( or a bare "("),
  # b backtick, s single quote, a ANSI-C $'...', d double quote. An empty
  # stack, p and b are all code, where every rule below applies.
  local stack="" top
  local ch two backslash=$'\\'
  local masked=""

  while (( i < len )); do
    ch="${cmd:i:1}"
    two="${cmd:i:2}"
    top="${stack: -1}"

    # A backslash escapes the next char in "..." and $'...' but is literal in
    # '...'; code contexts have their own rule below.
    if [[ "$ch" == "$backslash" && ( "$top" == a || "$top" == d ) ]]; then
      masked+="xx"
      i=$((i + 2))
      continue
    fi

    case "$top" in
      s)
        if [[ "$ch" == "'" ]]; then
          stack="${stack%?}"
          masked+="'"
        elif [[ "$ch" == '`' && "$stack" == *b* ]]; then
          # Bash ends a backtick substitution at the next backtick even with a
          # quote still open inside it, so drop everything above that b.
          stack="${stack%b*}"
          masked+='`'
        else
          masked+="x"
        fi
        i=$((i + 1))
        continue
        ;;
      a)
        if [[ "$ch" == "'" ]]; then
          stack="${stack%?}"
          masked+="'"
        else
          masked+="x"
        fi
        i=$((i + 1))
        continue
        ;;
      d)
        if [[ "$ch" == '"' ]]; then
          stack="${stack%?}"
          masked+='"'
          i=$((i + 1))
          continue
        elif [[ "$two" != "\$(" && "$ch" != '`' ]]; then
          # $' stays literal here; only $( and a backtick fall through to the
          # code rules below.
          masked+="x"
          i=$((i + 1))
          continue
        fi
        ;;
    esac

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
    if [[ "$two" == "\$'" ]]; then
      stack+="a"
      masked+="\$'"
      i=$((i + 2))
      continue
    fi
    if [[ "$two" == "\$(" ]]; then
      stack+="p"
      masked+="\$("
      i=$((i + 2))
      continue
    fi
    case "$ch" in
      "'")
        stack+="s"
        masked+="'"
        ;;
      '"')
        stack+="d"
        masked+='"'
        ;;
      "(")
        # Nests inside a substitution; at top level it stays literal, since
        # heredoc prose like ":-(" would otherwise leave a paren open.
        if [[ -n "$stack" ]]; then
          stack+="p"
        fi
        masked+="("
        ;;
      ")")
        if [[ "$top" == "p" ]]; then
          stack="${stack%?}"
        fi
        masked+=")"
        ;;
      '`')
        if [[ "$top" == "b" ]]; then
          stack="${stack%?}"
        else
          stack+="b"
        fi
        masked+='`'
        ;;
      *)
        masked+="$ch"
        ;;
    esac
    i=$((i + 1))
  done

  printf '%s' "$masked"
  [[ "$stack" != *[pba]* ]]
}

# True if the command backgrounds a process at the shell level. The strip below
# removes &&, >&, <&, &> and |&, which are foreground fd juggling. Three accepted
# false positives: a bitwise & in $((...)) reads as the background operator
# because the paren scan sees it as code, an & inside a heredoc body does too,
# since mask_command does not model heredocs, and a redirect to a file named
# after a keyword (cmd >&nohup) reads as that keyword. All deny a safe call,
# which is the fail-closed direction.
command_backgrounds() {
  local cmd="$1"
  local masked
  masked="$(mask_command "$cmd")" || return 0

  local stripped="$masked"
  stripped="${stripped//&&/}"
  stripped="${stripped//>&/}"
  stripped="${stripped//<&/}"
  stripped="${stripped//&>/}"
  stripped="${stripped//|&/}"
  if [[ "$stripped" == *"&"* ]]; then
    return 0
  fi

  # A keyword counts only at a command boundary. & and ` are in the class
  # because the keyword checks run on the unstripped string, where a glued &&
  # or a backtick substitution sits right before the keyword (#6215).
  local kw_lead='(^|[[:space:];|(&`])'

  # nohup survives the calling shell exiting, the hazard this hook exists to
  # catch, so it is rejected even without an accompanying &.
  local nohup_re="${kw_lead}nohup([[:space:]]|$)"
  [[ "$masked" =~ $nohup_re ]] && return 0

  # setsid detaches into a new session, surviving the calling shell the same
  # way nohup does.
  local setsid_re="${kw_lead}setsid([[:space:]]|$)"
  [[ "$masked" =~ $setsid_re ]] && return 0

  # coproc spawns a bash coprocess as a background job. The regex covers both
  # the named and unnamed forms.
  local coproc_re="${kw_lead}coproc([[:space:]]|$)"
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
