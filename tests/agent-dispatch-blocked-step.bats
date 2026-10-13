#!/usr/bin/env bats
# Issue #5257: the Forgejo "Detect a blocked marker" step writes blockers to
# $GITHUB_OUTPUT as a heredoc. The launcher writes blocked.txt with no trailing
# newline, so the closing `EOF` must not be glued onto the last blocker, or
# act/forgejo-runner rejects the delimiter and drops every output. The step
# body is extracted from the workflow so the test tracks the real text.

bats_require_minimum_version 1.5.0

setup() {
  FORGEJO_AGENT_DISPATCH_YML="${FORGEJO_AGENT_DISPATCH_YML:-$BATS_TEST_DIRNAME/../.forgejo/workflows/agent-dispatch.yml}"

  step_src="$BATS_TEST_TMPDIR/step.sh"
  sed -n '/^      - name: Detect a blocked marker$/,/^$/p' "$FORGEJO_AGENT_DISPATCH_YML" \
    | sed -n '/^        run: |$/,$p' | sed '1d; s/^          //' > "$step_src"
  # Anchor on the body's last line so a run: block cut short (say, by a blank
  # line inside it) fails here instead of running a truncated step.
  grep -q 'blocked.txt' "$step_src"
  grep -q 'echo "blocked=false"' "$step_src"
  [ "$(sed '/^$/d' "$step_src" | tail -n 1)" = "fi" ]
  bash -n "$step_src"

  work="$BATS_TEST_TMPDIR/work"
  mkdir -p "$work/.spindrift/logs"
  out="$BATS_TEST_TMPDIR/github-output"
  : > "$out"
}

run_step() {
  (cd "$work" && GITHUB_OUTPUT="$out" bash -e "$step_src")
}

# Parse $out the way act does: `name<<DELIM` opens a multiline value that
# only a line exactly equal to DELIM closes. A missing delimiter, or a line
# outside a heredoc that is neither form, rejects the whole file, so no output
# survives it -- not even ones written before it. An absent key returns 1.
parse_output() {
  local want="$1" line delim="" name="" value="" found="" have=""
  while IFS= read -r line || [ -n "$line" ]; do
    if [ -n "$delim" ]; then
      if [ "$line" = "$delim" ]; then
        [ "$name" = "$want" ] && found="${value%$'\n'}" have=1
        delim=""
      else
        value+="$line"$'\n'
      fi
    elif [[ "$line" =~ ^([^=<]+)\<\<(.+)$ ]]; then
      name="${BASH_REMATCH[1]}" delim="${BASH_REMATCH[2]}" value=""
    elif [[ "$line" =~ ^([^=]+)=(.*)$ ]]; then
      [ "${BASH_REMATCH[1]}" = "$want" ] && found="${BASH_REMATCH[2]}" have=1
    else
      echo "invalid line: $line" >&2
      return 3
    fi
  done < "$out"
  [ -z "$delim" ] || { echo "unterminated heredoc for $name" >&2; return 2; }
  [ -n "$have" ] || return 1
  printf '%s' "$found"
}

@test "no marker sets blocked=false" {
  run_step
  run parse_output blocked
  [ "$status" -eq 0 ]
  [ "$output" = "false" ]
}

@test "newline-less native blocker list closes the heredoc cleanly" {
  printf '%s' '#12 (native)' > "$work/.spindrift/logs/blocked.txt"
  run_step
  run parse_output blocked
  [ "$status" -eq 0 ]
  [ "$output" = "true" ]
  run parse_output blockers
  [ "$status" -eq 0 ]
  [ "$output" = "#12 (native)" ]
}

@test "newline-less transient-failure marker closes the heredoc cleanly" {
  printf '%s' 'a transient blocker check failure (will retry)' > "$work/.spindrift/logs/blocked.txt"
  run_step
  run parse_output blocked
  [ "$status" -eq 0 ]
  [ "$output" = "true" ]
  run parse_output blockers
  [ "$status" -eq 0 ]
  [ "$output" = "a transient blocker check failure (will retry)" ]
}

@test "marker already ending in a newline yields the same value, no blank line" {
  printf '%s\n' '#12 (native), #13 (native)' > "$work/.spindrift/logs/blocked.txt"
  run_step
  run parse_output blockers
  [ "$status" -eq 0 ]
  [ "$output" = "#12 (native), #13 (native)" ]
}
