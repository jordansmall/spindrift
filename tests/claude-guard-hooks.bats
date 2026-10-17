#!/usr/bin/env bats
# Claude Code treats a PreToolUse hook exiting anything but 0 or 2 as a
# non-blocking error and runs the tool anyway (issue #5400), so a guard hook
# that cannot start would fail open; so would one that hangs past Claude's own
# hook timeout (issue #5413), so the wrapper bounds it itself. This runs each
# rendered settings.json command the way Claude does (through sh) against
# stand-in hooks.

load helper

# The wrapper prefix lib/image.nix bakes in; commands() rewrites it and the
# literal-present test pins it.
BAKED_BOUND='timeout -k 2 10 '

setup() {
  [ -r "${CLAUDE_SETTINGS_FILE:-}" ] || {
    echo "claude-guard-hooks.bats: settings.json fixture not found: ${CLAUDE_SETTINGS_FILE:-unset}" >&2
    return 1
  }
  SETTINGS="$CLAUDE_SETTINGS_FILE"
  HOOKS="$BATS_TEST_TMPDIR/hooks"
  mkdir -p "$HOOKS"
  INPUT='{"tool_name":"Bash","tool_input":{"command":"true"}}'
}

# Prints each PreToolUse command with the baked hooks dir pointed at $HOOKS
# and the wrapper's bound shrunk so the hang tests are quick (the
# literal-present test keeps this rewrite from silently no-opping).
commands() {
  jq -r '.hooks.PreToolUse[].hooks[].command' "$SETTINGS" |
    sed -e "s#/home/agent/.claude/hooks/#$HOOKS/#g" \
      -e "s#$BAKED_BOUND#timeout -k 1 1 #"
}

# Runs every PreToolUse command with the hook body in $1 (or no file when
# empty) and requires exit status $2 and output matching the glob $3 (@NAME@
# stands for the hook's file name). RUN_PATH, when set, is the PATH the
# command runs under. Stops at the first mismatch and fails when no command
# was registered at all.
each_hook() {
  local body="$1" want_status="$2" want_out="$3" run_path="${RUN_PATH:-$PATH}"
  local cmd name want n=0
  while IFS= read -r cmd; do
    n=$((n + 1))
    name="$(basename "$(grep -o "$HOOKS/[A-Za-z0-9_.-]*" <<<"$cmd" | head -n1)")"
    rm -f "$HOOKS/$name"
    if [ -n "$body" ]; then
      printf '%s\n' "$body" >"$HOOKS/$name"
      chmod +x "$HOOKS/$name"
    fi
    want="${want_out//@NAME@/$name}"
    run env PATH="$run_path" "$(command -v sh)" -c "$cmd" <<<"$INPUT"
    [ "$status" -eq "$want_status" ] || { echo "$name: status $status, output '$output'"; return 1; }
    # shellcheck disable=SC2053 # $want is a deliberate glob pattern
    [[ "$output" == $want ]] || { echo "$name: output '$output'"; return 1; }
  done < <(commands)
  [ "$n" -gt 0 ] || { echo "no PreToolUse commands registered"; return 1; }
}

@test "a missing hook file refuses the call with exit 2 naming the hook" {
  each_hook "" 2 '*guard hook @NAME@ cannot run*refusing the call*'
}

@test "a hook with an unrunnable interpreter refuses the call with exit 2 naming the hook" {
  each_hook $'#!/nonexistent/interp\nexit 0' 2 '*guard hook @NAME@ cannot run*refusing the call*'
}

@test "a hook that fails with an ordinary error code refuses the call as failed" {
  each_hook $'#!/bin/sh\nexit 1' 2 '*guard hook @NAME@ failed (exit 1)*refusing the call*'
}

@test "a jq that cannot run refuses the call with exit 2 even though the hook would allow" {
  mkdir -p "$BATS_TEST_TMPDIR/empty"
  RUN_PATH="$BATS_TEST_TMPDIR/empty" each_hook $'#!/bin/sh\nexit 0' 2 '*guard hook @NAME@ cannot run (no working jq)*refusing the call*'
}

@test "a hook that allows still allows with stdout untouched" {
  each_hook $'#!/bin/sh\nexit 0' 0 ''
}

@test "a hook's own deny stays exit 0 with its JSON untouched" {
  local deny='{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"nope"}}'
  each_hook "#!/bin/sh
printf '%s' '$deny'
exit 0" 0 "$deny"
}

@test "a hook's own exit 2 keeps its own stderr" {
  each_hook $'#!/bin/sh\necho "its own reason" >&2\nexit 2' 2 'its own reason'
}

@test "every PreToolUse command carries the literal bound the test rewrites" {
  local cmd n=0
  while IFS= read -r cmd; do
    n=$((n + 1))
    [[ "$cmd" == *"$BAKED_BOUND"* ]] || { echo "no '$BAKED_BOUND' in: $cmd"; return 1; }
  done < <(jq -r '.hooks.PreToolUse[].hooks[].command' "$SETTINGS")
  [ "$n" -gt 0 ] || { echo "no PreToolUse commands registered"; return 1; }
}

@test "every PreToolUse hook sets a Claude timeout above the wrapper's worst case" {
  jq -e '[.hooks.PreToolUse[].hooks[]
      | (.command | capture("timeout -k (?<g>[0-9]+) (?<b>[0-9]+) ")) as $w
      | .timeout > ($w.g | tonumber) + ($w.b | tonumber)]
    | length > 0 and all' "$SETTINGS" >/dev/null
}

@test "a hook that hangs is cut off and refuses the call as timed out" {
  each_hook $'#!/bin/sh\nsleep 30' 2 '*guard hook @NAME@ timed out (exit 124)*refusing the call*'
}

@test "a hook that ignores TERM while hanging is killed and still refuses the call" {
  each_hook $'#!/bin/sh\ntrap \'\' TERM\nwhile :; do sleep 1; done' 2 '*guard hook @NAME@ failed (exit 137)*refusing the call*'
}

@test "a missing timeout refuses the call with exit 2 even though the hook would allow" {
  mkdir -p "$BATS_TEST_TMPDIR/jqonly"
  ln -s "$(command -v jq)" "$BATS_TEST_TMPDIR/jqonly/jq"
  RUN_PATH="$BATS_TEST_TMPDIR/jqonly" each_hook $'#!/bin/sh\nexit 0' 2 '*guard hook @NAME@ cannot run*refusing the call*'
}
