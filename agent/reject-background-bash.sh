#!/usr/bin/env bash
# PreToolUse hook (#1609, widened by #1620): denies a Bash call that
# backgrounds, via run_in_background or in the shell. run_in_background is a
# tool parameter, not a tool name, so --disallowedTools cannot strip it, and a
# headless Box run exits without waiting for the backgrounded work (#1542).
set -euo pipefail

# Reads one heredoc opener's delimiter word, from just after the <<, into
# mask_command's hd_recs. Not standalone: through bash dynamic scoping it reads
# mask_command's cmd, len and backslash, advances i, appends to masked and
# hd_recs, and sets bad_delim. Each record is "<quoted><dash><delim>": two 0/1
# flag chars, then the word.
heredoc_read_delim() {
  local -i dash=0 quoted=0 ansi=0 closed
  local word="" c rest inner
  if [[ "${cmd:i:1}" == "-" ]]; then
    dash=1
    masked+="-"
    i=$((i + 1))
  fi
  while [[ "${cmd:i:1}" == [" "$'\t'] ]]; do
    masked+="${cmd:i:1}"
    i=$((i + 1))
  done
  # The delimiter word is never a command, so it is masked: a delimiter
  # named nohup must not match the keyword check.
  while (( i < len )); do
    c="${cmd:i:1}"
    case "$c" in
      [" "$'\t\n;&|<>()'])
        break
        ;;
      "'")
        rest="${cmd:i+1}"
        inner="${rest%%\'*}"
        # A backslash in $'...' is an ANSI-C escape this does not decode.
        if [[ "$rest" != *"'"* || "$inner" == *$'\n'* ||
          ( $ansi -eq 1 && "$inner" == *"$backslash"* ) ]]; then
          bad_delim=1
          break
        fi
        ansi=0
        word+="$inner"
        quoted=1
        masked+="x${inner//?/x}x"
        i=$((i + ${#inner} + 2))
        continue
        ;;
      '"')
        closed=0
        masked+="x"
        i=$((i + 1))
        while (( i < len )); do
          c="${cmd:i:1}"
          if [[ "$c" == '"' ]]; then
            closed=1
            masked+="x"
            i=$((i + 1))
            break
          elif [[ "$c" == $'\n' ]]; then
            break
          elif [[ "$c" == "$backslash" ]]; then
            c="${cmd:i+1:1}"
            # Bash drops a backslash-newline as a line continuation before it
            # reads the word; this does not model that, so fail closed.
            if [[ "$c" == $'\n' ]]; then
              bad_delim=1
              break
            fi
            # Inside double quotes a backslash escapes only these four.
            if [[ -n "$c" && "\$\`\"$backslash" == *"$c"* ]]; then
              word+="$c"
              masked+="xx"
              i=$((i + 2))
            else
              word+="$backslash"
              masked+="x"
              i=$((i + 1))
            fi
          else
            word+="$c"
            masked+="x"
            i=$((i + 1))
          fi
        done
        quoted=1
        if (( ! closed )); then
          bad_delim=1
          break
        fi
        continue
        ;;
      "$backslash")
        # A backslash-newline is a line continuation: fail closed, as above.
        if [[ "${cmd:i+1:1}" == $'\n' ]]; then
          bad_delim=1
          break
        fi
        quoted=1
        word+="${cmd:i+1:1}"
        masked+="xx"
        i=$((i + 2))
        continue
        ;;
      '$')
        # Like bash, drop the $ of $'E' or $"E" and read the quoted part as quoted.
        if [[ "${cmd:i+1:1}" == ["'\""] ]]; then
          [[ "${cmd:i+1:1}" == "'" ]] && ansi=1
          masked+="x"
          i=$((i + 1))
          continue
        fi
        ;;
    esac
    word+="$c"
    masked+="x"
    i=$((i + 1))
  done
  # bad_delim is sticky: later heredocs go unrecorded, which is safe because
  # mask_command's return status already denies.
  if (( bad_delim == 0 )) && [[ -n "$word" ]]; then
    hd_recs+=("$quoted$dash$word")
  fi
}

# Consumes the bodies of the heredocs opened on the line just ended, through
# each terminator, masking a quoted-delimiter body and joining an unquoted
# one's backslash-continued lines for the terminator test. Dynamically scoped
# to mask_command's locals: reads cmd and len, advances i and masked, and
# clears hd_recs. An unquoted body is copied through raw, not stack-scanned, so its
# substitutions stay visible to the caller's match.
heredoc_read_bodies() {
  local -i n found
  local rec delim line cmp rest raw trail
  masked+=$'\n'
  i=$((i + 1))
  for rec in "${hd_recs[@]}"; do
    delim="${rec:2}"
    found=0
    while (( i < len && ! found )); do
      rest="${cmd:i}"
      line="${rest%%$'\n'*}"
      n=${#line}
      cmp="$line"
      raw="$line"
      # Bash joins an unquoted body line ending in an odd run of backslashes to
      # the next before comparing it with the delimiter.
      if [[ "$rec" != 1* ]]; then
        while (( i + n < len )) && trail="${line##*[!\\]}" && (( ${#trail} % 2 )); do
          cmp="${cmp%?}"
          rest="${rest:${#line}+1}"
          line="${rest%%$'\n'*}"
          cmp+="$line"
          raw+=$'\n'"$line"
          n=$((n + ${#line} + 1))
        done
      fi
      if [[ "$rec" == ?1* ]]; then
        while [[ "$cmp" == $'\t'* ]]; do
          cmp="${cmp#?}"
        done
      fi
      if [[ "$cmp" == "$delim" ]]; then
        masked+="${raw//[!$'\n']/x}"
        found=1
      elif [[ "$rec" == 1* ]]; then
        masked+="${line//?/x}"
      else
        masked+="$raw"
      fi
      i=$((i + n))
      if (( i < len )); then
        masked+=$'\n'
        i=$((i + 1))
      fi
    done
  done
  hd_recs=()
}

# Masks quoted and backslash-escaped characters with 'x' so the caller can
# match operators like & without tripping on a quoted or escaped literal such
# as "foo & bar" or foo\&bar. Command substitution inside double quotes runs in
# bash, so $(...) and backticks there are scanned live, and $'...' honours \'.
# Returns 1 (after printing what it masked) when an open substitution,
# backtick, $'...', ${...} or arithmetic remains anywhere on the stack at end
# of input, or a heredoc delimiter word is unreadable (an unclosed quote, a
# $'...' escape, a backslash-newline), so the caller can fail closed. A lone '
# or " outside any substitution does not.
# A heredoc body with a quoted delimiter is masked; an unquoted one stays
# visible: bash expands $(...) and backticks there, so the body is copied raw
# rather than stack-scanned, and an & or keyword in its prose denies (accepted
# false positive). A << in a # comment, ${...} or arithmetic opens no
# heredoc, so the lines after it stay scanned. Unmodeled fail-open: inside
# $(...) any ) closes the substitution, including a case pattern's a) or a )
# in a # comment, so what follows up to the closing " is masked.
mask_command() {
  local cmd="$1"
  local -i i=0
  local -i len=${#cmd}
  # One char per open context, top last: p paren (from $( or a bare "("),
  # b backtick, s single quote, a ANSI-C $'...', d double quote, v ${...},
  # m arithmetic ($((, ((, $[, and a [ inside m). An empty stack, p, b, v and m
  # are scanned as code; v and m skip the # and << rules, and m also handles
  # [ and ] and pushes no nested m for a bare ((. A lone ) that ends any m
  # turns it into p: $(( or (( with no )) was $( or ( plus a subshell.
  local stack="" top
  # Set by a # comment, cleared at the next newline; no heredoc opens meanwhile.
  local -i in_comment=0
  local ch two backslash=$'\\'
  local masked=""
  # Heredocs opened on the current line; their bodies start after its newline.
  local -a hd_recs=()
  local -i bad_delim=0

  while (( i < len )); do
    ch="${cmd:i:1}"
    two="${cmd:i:2}"
    top="${stack: -1}"
    if [[ "$ch" == $'\n' ]]; then
      in_comment=0
    fi

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
    if [[ "${cmd:i:3}" == "\$((" || ( "$two" == "((" && "$top" != m ) ]]; then
      stack+="m"
      if [[ "$two" == "((" ]]; then
        masked+="(("
        i=$((i + 2))
      else
        masked+="\$(("
        i=$((i + 3))
      fi
      continue
    fi
    if [[ "$two" == "\$[" ]]; then
      stack+="m"
      masked+="\$["
      i=$((i + 2))
      continue
    fi
    if [[ "$two" == "\${" ]]; then
      stack+="v"
      masked+="\${"
      i=$((i + 2))
      continue
    fi
    if [[ "$two" == "\$(" ]]; then
      stack+="p"
      masked+="\$("
      i=$((i + 2))
      continue
    fi
    # Tests only top: a $(...) inside ${...} is code, where # starts a comment.
    # The << rule below tests the whole stack and opens no heredoc under any v
    # or m, so ambiguous nesting leaves later lines scanned.
    if [[ "$ch" == "#" && "$top" != [vm] ]]; then
      # Only at word start: ${#x} and $# are not comments.
      if (( i == 0 )) || [[ "${cmd:i-1:1}" == [" "$'\t\n;&|()`'] ]]; then
        in_comment=1
      fi
    fi
    if [[ "$two" == "<<" && "$stack" != *[vm]* ]] && (( ! in_comment )); then
      if [[ "${cmd:i:3}" == "<<<" ]]; then
        masked+="<<<"
        i=$((i + 3))
        continue
      fi
      masked+="<<"
      i=$((i + 2))
      heredoc_read_delim
      continue
    fi
    if [[ "$ch" == $'\n' && ${#hd_recs[@]} -gt 0 ]]; then
      heredoc_read_bodies
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
        elif [[ "$top" == "m" && "${cmd:i+1:1}" == ")" ]]; then
          stack="${stack%?}"
          masked+="))"
          i=$((i + 2))
          continue
        elif [[ "$top" == "m" ]]; then
          stack="${stack%?}p"
        fi
        masked+=")"
        ;;
      "}")
        if [[ "$top" == "v" ]]; then
          stack="${stack%?}"
        fi
        masked+="}"
        ;;
      "[")
        # Nests inside arithmetic so the subscript's ] does not pop the $[ m.
        if [[ "$top" == "m" ]]; then
          stack+="m"
        fi
        masked+="["
        ;;
      "]")
        if [[ "$top" == "m" ]]; then
          stack="${stack%?}"
        fi
        masked+="]"
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
  [[ "$stack" != *[pbamv]* ]] && (( bad_delim == 0 ))
}

# True if the command backgrounds a process at the shell level. The strip below
# removes &&, >&, <&, &> and |&, which are foreground fd juggling. Three accepted
# false positives: a bitwise & in $((...)) reads as the background operator
# because arithmetic is scanned as code, an & or keyword in an unquoted
# heredoc body does too (bash expands it, so it is scanned), and a redirect
# to a file named after a keyword (cmd >&nohup) reads as that keyword. All
# deny a safe call, which is the fail-closed direction.
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
