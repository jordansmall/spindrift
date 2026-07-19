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
set -euo pipefail

input="$(cat)"

# Malformed/non-JSON stdin makes these extractions come back empty (jq's own
# parse error goes to stderr), which reads as "not a matching call" below --
# the same fail-open-to-allow outcome as any other non-match, not a distinct
# bypass. Silenced here so a stray non-JSON payload doesn't spam the
# transcript with jq parse-error noise.
if [ "$(jq -r '.tool_name // empty' 2>/dev/null <<<"$input")" != "Bash" ]; then
  exit 0
fi

if [ "$(jq -r '.tool_input.run_in_background // false' 2>/dev/null <<<"$input")" != "true" ]; then
  exit 0
fi

jq -n '{
  hookSpecificOutput: {
    hookEventName: "PreToolUse",
    permissionDecision: "deny",
    permissionDecisionReason: "Backgrounded Bash calls are rejected in headless Box runs -- rerun the command in the foreground and block on it until it completes."
  }
}'
