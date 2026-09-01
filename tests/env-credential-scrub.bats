#!/usr/bin/env bats
# PreToolUse hook (issue #1927, spec #1907): every Bash call is rewritten via
# hookSpecificOutput.updatedInput to `unset` ANTHROPIC_API_KEY and
# CLAUDE_CODE_OAUTH_TOKEN before it runs, so the variables are genuinely absent
# from a subprocess that forks after the unset -- not merely undumpable by a
# denylisted command. Any /proc/<anything>/environ reference is denied outright
# instead: the hook can't tell a safe self-reference from an unsafe one from
# static command text (see the hook's own header for the full reasoning).
# Exercised directly against the script, so this is a unit test of the hook's
# own stdin/stdout contract.

setup() {
  : "${ENV_CREDENTIAL_SCRUB_HOOK_SCRIPT:?ENV_CREDENTIAL_SCRUB_HOOK_SCRIPT must be set}"
}

@test "rewrites an ordinary Bash command to unset both credential vars first" {
  run bash "$ENV_CREDENTIAL_SCRUB_HOOK_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"echo hello"}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.hookEventName == "PreToolUse"' >/dev/null
  echo "$output" | jq -e '.hookSpecificOutput | has("permissionDecision") | not' >/dev/null
  new_command="$(echo "$output" | jq -r '.hookSpecificOutput.updatedInput.command')"
  [[ "$new_command" == *"unset ANTHROPIC_API_KEY CLAUDE_CODE_OAUTH_TOKEN"* ]]
  [[ "$new_command" == *"echo hello"* ]]
}

@test "preserves other tool_input fields (e.g. description) in updatedInput" {
  run bash "$ENV_CREDENTIAL_SCRUB_HOOK_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"echo hi","description":"say hi","timeout":5000}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.updatedInput.description == "say hi"' >/dev/null
  echo "$output" | jq -e '.hookSpecificOutput.updatedInput.timeout == 5000' >/dev/null
}

@test "denies a read of /proc/self/environ" {
  run bash "$ENV_CREDENTIAL_SCRUB_HOOK_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"cat /proc/self/environ"}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "denies a read of /proc/thread-self/environ" {
  run bash "$ENV_CREDENTIAL_SCRUB_HOOK_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"cat /proc/thread-self/environ"}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "denies a read of a numeric pid's /proc/<pid>/environ" {
  run bash "$ENV_CREDENTIAL_SCRUB_HOOK_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"cat /proc/1/environ"}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

# Regression test for the exact bypass an earlier version (matching only
# literal self/thread-self/digit forms) was shown to miss: reading the
# still-alive shell's own /proc/<pid>/environ via $$/$BASHPID/$PPID from a
# forked child leaks the credential regardless of `unset`, since that memory
# region is fixed at the shell's exec time. The hook can't resolve those forms
# from static text, so it denies the whole family.
@test "denies /proc/\$\$, /proc/\$BASHPID, /proc/\$PPID, and a glob form" {
  for cmd in \
    'cat /proc/$$/environ' \
    'cat /proc/$BASHPID/environ' \
    'cat /proc/$PPID/environ' \
    'cat /proc/*/environ'
  do
    run bash "$ENV_CREDENTIAL_SCRUB_HOOK_SCRIPT" <<<"$(jq -n --arg c "$cmd" '{tool_name:"Bash",tool_input:{command:$c}}')"
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null || {
      echo "not denied: $cmd" >&2
      false
    }
  done
}

@test "ignores a non-Bash tool call" {
  run bash "$ENV_CREDENTIAL_SCRUB_HOOK_SCRIPT" <<<'{"tool_name":"Read","tool_input":{"file_path":"/proc/self/environ"}}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "allows malformed non-JSON stdin" {
  run bash "$ENV_CREDENTIAL_SCRUB_HOOK_SCRIPT" <<<'not json at all'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "allows a Bash call with no command field" {
  run bash "$ENV_CREDENTIAL_SCRUB_HOOK_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{}}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

# Combined guard (issue #1927 AC): executes the actual rewritten command in a
# real Bash subprocess, proving both properties together rather than in
# isolation -- Bash still runs an ordinary command correctly AND the credential
# is genuinely absent from that subprocess's environment -- so the two can't
# silently trade off the way #1926 found them to.
@test "the rewritten command runs normally and never exposes the real credential" {
  run bash "$ENV_CREDENTIAL_SCRUB_HOOK_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"echo start; env; echo done"}}'
  [ "$status" -eq 0 ]
  new_command="$(echo "$output" | jq -r '.hookSpecificOutput.updatedInput.command')"

  exec_output_file="$(mktemp)"
  ANTHROPIC_API_KEY="leaked-api-key-value" \
    CLAUDE_CODE_OAUTH_TOKEN="leaked-oauth-token-value" \
    bash -c "$new_command" >"$exec_output_file" 2>&1
  exec_status=$?
  exec_output="$(cat "$exec_output_file")"
  rm -f "$exec_output_file"

  [ "$exec_status" -eq 0 ]
  [[ "$exec_output" == *"start"* ]]
  [[ "$exec_output" == *"done"* ]]
  [[ "$exec_output" != *"leaked-api-key-value"* ]]
  [[ "$exec_output" != *"leaked-oauth-token-value"* ]]
  [[ "$exec_output" != *"ANTHROPIC_API_KEY"* ]]
  [[ "$exec_output" != *"CLAUDE_CODE_OAUTH_TOKEN"* ]]
}

# Regression coverage for the bypasses a denylist-only design was shown to
# miss: `set`, `export -p`, `declare -p`, command substitution, and
# `&&`-chaining all enumerate the shell's own variable table rather than
# calling `env`/`printenv` by name. The unset rewrite removes the variables
# before any of them run, so all come up empty without being named.
@test "set/export -p/declare -p/command substitution/chaining never expose the credential" {
  for cmd in \
    'set' \
    'export -p' \
    'declare -p ANTHROPIC_API_KEY' \
    'x=$(env); echo "$x"' \
    'true && env'
  do
    run bash "$ENV_CREDENTIAL_SCRUB_HOOK_SCRIPT" <<<"$(jq -n --arg c "$cmd" '{tool_name:"Bash",tool_input:{command:$c}}')"
    [ "$status" -eq 0 ]
    new_command="$(echo "$output" | jq -r '.hookSpecificOutput.updatedInput.command')"

    exec_output="$(ANTHROPIC_API_KEY="leaked-api-key-value" \
      CLAUDE_CODE_OAUTH_TOKEN="leaked-oauth-token-value" \
      bash -c "$new_command" 2>&1)" || true

    [[ "$exec_output" != *"leaked-api-key-value"* ]] || {
      echo "credential leaked via: $cmd" >&2
      false
    }
    [[ "$exec_output" != *"leaked-oauth-token-value"* ]] || {
      echo "credential leaked via: $cmd" >&2
      false
    }
  done
}
