#!/usr/bin/env bats

# Unit test for the PreToolUse hook of issue #1927 (spec #1907), driven
# through the script's own stdin/stdout contract, not a real claude session.
# See agent/env-credential-scrub.sh for why the hook unsets the credentials
# instead of denylisting readers, and why it denies /proc/<pid>/environ.

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

# An earlier hook matched only literal self/thread-self/pid-number forms and
# missed this: the still-alive shell's own /proc/<pid>/environ keeps the
# credential despite `unset`, since that memory region is fixed at exec time.
# The hook cannot resolve $$/$BASHPID/$PPID statically, so it denies them all.
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

# Issue #1927 AC: run the rewritten command in a real Bash subprocess so one
# test covers both properties. The command still works and no credential
# survives in its environment, so the two cannot trade off against each other
# the way #1926 found them to.
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

# `set`, `export -p`, `declare -p`, command substitution, and `&&`-chaining
# read the shell's own variable table instead of calling `env` or `printenv`
# by name, so this hook's first-draft denylist missed them. The unset rewrite
# removes the variables before any of them run.
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
