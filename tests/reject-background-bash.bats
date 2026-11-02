#!/usr/bin/env bats
# PreToolUse hook (issue #1609): denies a Bash call with run_in_background: true,
# since a headless Box run has no harness watching for a later re-invocation
# (#1542 lost a run this way). #1620 widens it to parse tool_input.command for a
# shell-level escape (trailing or mid-command &, nohup) the parameter never sees.

setup() {
  : "${REJECT_BACKGROUND_BASH_SCRIPT:?REJECT_BACKGROUND_BASH_SCRIPT must be set}"
}

@test "denies a Bash call with run_in_background: true" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"go test ./...","run_in_background":true}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.hookEventName == "PreToolUse"' >/dev/null
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecisionReason | test("foreground")' >/dev/null
}

@test "allows a Bash call with run_in_background: false" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"go test ./...","run_in_background":false}}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "allows a Bash call with no run_in_background key at all" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"go test ./..."}}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "ignores non-Bash tool calls even with run_in_background: true in their input" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Read","tool_input":{"file_path":"/tmp/x","run_in_background":true}}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "denies a Bash call whose command ends in a trailing &" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"sleep 300 &"}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.hookEventName == "PreToolUse"' >/dev/null
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "denies a Bash call that backgrounds a command mid-line, not just trailing" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"sleep 300 & echo done"}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "allows a Bash call chained with && (logical and, not background)" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"go build ./... && go test ./..."}}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "allows a Bash call with a literal & inside a quoted string" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"echo \"foo & bar\""}}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "allows a Bash call with a literal & inside a single-quoted string" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<"{\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"echo 'foo & bar'\"}}"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "denies a Bash call redirecting stderr to stdout and then backgrounding" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"long_running.sh > /tmp/out.log 2>&1 &"}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "allows a Bash call using >&2 to redirect stdout to stderr in the foreground" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"echo failed >&2"}}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "allows a Bash call using &> to redirect stdout and stderr in the foreground" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"go build ./... &> /tmp/build.log"}}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "denies a Bash call invoking nohup" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"nohup long_running.sh"}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "allows a Bash call with a backslash-escaped & outside any quotes" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"echo foo \\& bar"}}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "allows a Bash call whose command merely contains nohup as part of a longer word" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"echo mynohupthing"}}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "allows a Bash call where nohup only appears inside a quoted string" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"echo \"nohup\""}}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "denies a Bash call invoking nohup via a backslash-escaped keyword prefix" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"\\nohup long_running.sh"}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "allows a Bash call where a backslash escapes the space before nohup" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"echo foo\\ nohup"}}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "denies a Bash call invoking setsid" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"setsid long_running.sh"}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "allows a Bash call whose command merely contains setsid as part of a longer word" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"echo mysetsidthing"}}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "allows a Bash call where setsid only appears inside a quoted string" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"echo \"setsid\""}}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "denies a Bash call invoking setsid via a backslash-escaped keyword prefix" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"\\setsid long_running.sh"}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "denies a Bash call using named coproc" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"coproc foo { sleep 300; }"}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "denies a Bash call using unnamed coproc" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"coproc { sleep 300; }"}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "allows a Bash call whose command merely contains coproc as part of a longer word" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"echo mycoprocthing"}}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "allows a Bash call where coproc only appears inside a quoted string" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"echo \"coproc\""}}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "denies a Bash call invoking coproc via a backslash-escaped keyword prefix" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"\\coproc foo { sleep 300; }"}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "denies a Bash call with nohup glued after &&" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"true&&nohup long_running.sh"}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "denies a Bash call with nohup glued to a space-led &&" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"true &&nohup long_running.sh"}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "denies a Bash call with setsid glued after &&" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"true&&setsid long_running.sh"}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "denies a Bash call with coproc glued after &&" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"true&&coproc { sleep 300; }"}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "denies a Bash call with nohup inside a backtick command substitution" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"x=`nohup long_running.sh`"}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "denies a Bash call with setsid inside a backtick command substitution" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"x=`setsid long_running.sh`"}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "denies a Bash call with coproc inside a backtick command substitution" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"x=`coproc { sleep 300; }`"}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "allows a Bash call where a word merely starting with nohup follows a glued &&" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"echo a&&echo nohupx"}}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "allows a Bash call using |& to pipe stdout and stderr in the foreground" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"make |& tee build.log"}}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "allows a Bash call using <& to duplicate a file descriptor on stdin" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"read x <&3"}}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "denies a Bash call backgrounding inside command substitution" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"echo $(sleep 300 &)"}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "denies nohup inside command substitution within double quotes" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"echo \"$(nohup sleep 300 >/dev/null 2>&1 &)\""}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "denies backgrounding inside backticks within double quotes" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"echo \"`sleep 300 &`\""}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "denies nohup glued to a backtick inside double quotes" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"echo \"`nohup sleep 300`\""}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "denies setsid inside command substitution within double quotes" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"echo \"$(setsid sleep 300)\""}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "denies a trailing & after an ANSI-C string holding an escaped quote" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<'JSON'
{"tool_name":"Bash","tool_input":{"command":"echo $'it\\'s'; sleep 300 &"}}
JSON
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "denies an unterminated command substitution inside double quotes" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"echo \"$(sleep 300 &"}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "denies an unterminated ANSI-C string (fail closed)" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"echo $'"'"'abc"}}'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "allows a harmless command substitution inside double quotes" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"echo \"$(date)\""}}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "allows a quoted & nested in a double-quoted command substitution" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<'{"tool_name":"Bash","tool_input":{"command":"x=\"$(echo \"a & b\")\""}}'
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "allows an & inside an ANSI-C string" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<'JSON'
{"tool_name":"Bash","tool_input":{"command":"echo $'a & b'"}}
JSON
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "allows an ANSI-C string holding an escaped quote" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<'JSON'
{"tool_name":"Bash","tool_input":{"command":"echo $'it\\'s'"}}
JSON
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "allows a heredoc body with a lone apostrophe (no fail-closed on plain quotes)" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<'JSON'
{"tool_name":"Bash","tool_input":{"command":"cat <<EOF\ndon't\nEOF"}}
JSON
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "allows a heredoc body with a parenthesised aside holding an apostrophe" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<'JSON'
{"tool_name":"Bash","tool_input":{"command":"cat <<EOF\n(don't do this)\nEOF"}}
JSON
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "allows a heredoc body with an unbalanced open paren" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<'JSON'
{"tool_name":"Bash","tool_input":{"command":"cat <<EOF\nsad :-(\nEOF"}}
JSON
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "allows a heredoc body with a backtick span holding an apostrophe" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<'JSON'
{"tool_name":"Bash","tool_input":{"command":"cat <<EOF\nthe `it's` case\nEOF"}}
JSON
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "denies a trailing & after a commit-message heredoc holding an apostrophe" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<'JSON'
{"tool_name":"Bash","tool_input":{"command":"git commit -m \"$(cat <<'EOF'\nfix: don't\nEOF\n)\"; sleep 300 &"}}
JSON
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "denies a trailing & after a comment with an apostrophe inside a double-quoted substitution" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<'JSON'
{"tool_name":"Bash","tool_input":{"command":"echo \"$(echo hi # it's\n)\"; sleep 300 &"}}
JSON
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "denies a trailing & after a backtick span holding an unclosed apostrophe" {
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<'JSON'
{"tool_name":"Bash","tool_input":{"command":"echo `echo it's`; sleep 300 &"}}
JSON
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

# Runs the hook on a multi-line command; JSON-encoding it with jq avoids
# hand-escaping newlines and tabs in the heredoc cases below.
run_command() {
  local payload
  payload="$(jq -n --arg c "$1" '{tool_name:"Bash",tool_input:{command:$c}}')"
  run bash "$REJECT_BACKGROUND_BASH_SCRIPT" <<<"$payload"
  [ "$status" -eq 0 ]
}

assert_allowed() {
  [ -z "$output" ]
}

assert_denied() {
  echo "$output" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null
}

@test "allows a & in a single-quoted-delimiter heredoc body" {
  run_command $'driver-exec signal comment <<\'SPINDRIFT_SIGNAL_EOF\'\nrepro: sleep 300 &\nSPINDRIFT_SIGNAL_EOF'
  assert_allowed
}

@test "allows a & in a double-quoted-delimiter heredoc body" {
  run_command $'cat <<"D"\nrepro: sleep 300 &\nD'
  assert_allowed
}

@test "allows a & in a backslash-delimiter heredoc body" {
  run_command $'cat <<\\D\nrepro: sleep 300 &\nD'
  assert_allowed
}

@test "allows a & in a dash-form quoted heredoc with a tab-indented terminator" {
  run_command $'cat <<-\'D\'\n\trepro: sleep 300 &\n\tD'
  assert_allowed
}

@test "allows nohup as a bare word in a quoted heredoc body" {
  run_command $'cat <<\'D\'\nnohup cmd\nD'
  assert_allowed
}

@test "allows an apostrophe and & in a heredoc inside a double-quoted substitution" {
  run_command $'git commit -m "$(cat <<\'EOF\'\nfix: don\'t & stop\nEOF\n)"'
  assert_allowed
}

@test "allows a & only inside the second of two heredocs on one line" {
  run_command $'cat <<\'A\' <<\'B\'\nx\nA\ny & z\nB'
  assert_allowed
}

@test "denies a & substitution in an unquoted heredoc body" {
  run_command $'cat <<D\n$(sleep 300 &)\nD'
  assert_denied
}

@test "denies a trailing & after a quoted heredoc body holding an apostrophe" {
  run_command $'cat <<\'E\'\ndon\'t\nE\nsleep 300 &'
  assert_denied
}

@test "denies a trailing & after an unquoted heredoc body holding an apostrophe" {
  run_command $'cat <<E\nit\'s\nE\nsleep 300 &'
  assert_denied
}

@test "denies a trailing & after two heredocs on one line" {
  run_command $'cat <<\'A\' <<\'B\'\nx\nA\ny\nB\nsleep 300 &'
  assert_denied
}

@test "denies a & after a here-string" {
  run_command 'cat <<< foo &'
  assert_denied
}

@test "allows a quoted & inside a here-string" {
  run_command "cat <<<'a & b'"
  assert_allowed
}

@test "denies a trailing & after an unterminated quote in a heredoc delimiter" {
  run_command $'cat <<\'EOF\nsleep 300 &'
  assert_denied
}

@test "denies a & after a quoted heredoc opener inside a # comment" {
  run_command $'echo hi # <<\'E\'\nsleep 300 &'
  assert_denied
}

@test "denies a & after a quoted heredoc opener inside a parameter expansion" {
  run_command $'echo ${x:-<<\'E\'}\nsleep 300 &'
  assert_denied
}

@test "denies a & after a << inside arithmetic expansion" {
  run_command $'echo $((1<<\'E\'))\nsleep 300 &'
  assert_denied
}

@test "denies a & after a << inside a (( )) command" {
  run_command $'((1<<\'E\'))\nsleep 300 &'
  assert_denied
}

@test "denies a & after a << inside double-quoted arithmetic expansion" {
  run_command $'echo "$((1<<\'E\'))"\nsleep 300 &'
  assert_denied
}

@test "denies a & after a << inside a legacy dollar-bracket expansion" {
  run_command $'echo $[1<<\'E\']\nsleep 300 &'
  assert_denied
}

@test "allows a quoted heredoc after a closed length expansion" {
  run_command $'x=${#y}; cat <<\'E\'\nsleep 300 &\nE'
  assert_allowed
}

@test "allows a bit shift in arithmetic expansion" {
  run_command 'echo $(( 1 << 2 ))'
  assert_allowed
}

@test "allows a quoted heredoc after a closed arithmetic expansion" {
  run_command $'echo $((1+2)); cat <<\'E\'\nsleep 300 &\nE'
  assert_allowed
}

@test "allows a quoted heredoc inside a command substitution" {
  run_command $'echo $(cat <<\'E\'\nsleep 300 &\nE\n)'
  assert_allowed
}

@test "allows a quoted heredoc on the line after a # comment" {
  run_command $'echo hi # note\ncat <<\'E\'\nsleep 300 &\nE'
  assert_allowed
}

@test "denies a & after a << following an array subscript in arithmetic expansion" {
  run_command $'echo $((a[1]<<\'E\'))\nsleep 300 &'
  assert_denied
}

@test "denies a & after a << following an array subscript in legacy bracket arithmetic" {
  run_command $'echo $[a[1]<<\'E\']\nsleep 300 &'
  assert_denied
}

@test "allows a bit shift after an array subscript in arithmetic expansion" {
  run_command 'echo $(( a[1] << 2 ))'
  assert_allowed
}

@test "allows a & in a \$'E'-delimiter heredoc body" {
  run_command $'cat <<$\'E\'\nsleep 300 &\nE'
  assert_allowed
}

@test "allows a & in a \$\"E\"-delimiter heredoc body" {
  run_command $'cat <<$"E"\nsleep 300 &\nE'
  assert_allowed
}

@test "denies a trailing & after a \$'E'-delimiter heredoc terminator" {
  run_command $'cat <<$\'E\'\nbody\nE\nsleep 300 &'
  assert_denied
}

@test "denies a trailing & after a \$\"E\"-delimiter heredoc terminator" {
  run_command $'cat <<$"E"\nbody\nE\nsleep 300 &'
  assert_denied
}

@test "denies a heredoc delimiter holding an ANSI-C escape" {
  run_command $'cat <<$\'E\\x41\'\nbody\nEA\nsleep 300 &'
  assert_denied
}

@test "denies a backslash-newline continuation after a quoted heredoc delimiter" {
  run_command $'cat <<\'E\'\\\n\nx\nE\nsleep 300 &'
  assert_denied
}

@test "denies a backslash-newline continuation inside a bare heredoc delimiter" {
  run_command $'cat <<E\\\nF\nbody\nEF\nsleep 300 &'
  assert_denied
}

@test "denies a backslash-newline continuation inside a double-quoted heredoc delimiter" {
  run_command $'cat <<"E\\\nF"\nbody\nEF\nsleep 300 &'
  assert_denied
}

@test "denies a & after a double-quoted delimiter whose backslash bash keeps" {
  run_command $'cat <<"E\\a"\nhi\nE\\a\nsleep 300 &\nEa'
  assert_denied
}

@test "allows a & in a body closed by a double-quoted delimiter that keeps its backslash" {
  run_command $'cat <<"E\\a"\nsleep 300 &\nE\\a'
  assert_allowed
}

@test "allows a & in a body closed by a double-quoted delimiter whose backslash escapes a dollar" {
  run_command $'cat <<"E\\$"\nsleep 300 &\nE$'
  assert_allowed
}

@test "denies a & after an unquoted body line joined into the terminator" {
  run_command $'cat <<EOF 3<<\'Q\'\nEO\\\nF\nQ\nEOF\nsleep 300 &\nQ'
  assert_denied
}

@test "denies a & hidden by a quote after a continued unquoted body line" {
  run_command $'cat <<E\nfoo\\\nE\ndon\'t\nE\nsleep 300 &'
  assert_denied
}

@test "denies a & after a continued tab-indented dash-form body line" {
  run_command $'cat <<-E\nfoo\\\n\tE\ndon\'t\n\tE\nsleep 300 &'
  assert_denied
}

@test "allows a continued line in an unquoted heredoc body" {
  run_command $'cat <<EOF\nfoo \\\nbar\nEOF'
  assert_allowed
}

@test "denies a & in a tab-indented unquoted dash-form heredoc body" {
  run_command $'cat <<-EOF\n\tsleep 300 &\nEOF'
  assert_denied
}

@test "denies a trailing & after a tab-indented unquoted dash-form terminator" {
  run_command $'cat <<-EOF\nok\n\tEOF\nsleep 300 &'
  assert_denied
}

@test "denies a & after a \$(( that closes as a command substitution" {
  run_command 'echo "$((sleep 1) )"; sleep 300 &'
  assert_denied
}

@test "denies nohup after a \$(( that closes as a command substitution" {
  run_command 'x="$((sleep 1) )"; nohup sleep 300'
  assert_denied
}

@test "allows a \$(( that closes as a command substitution with no backgrounding" {
  run_command 'echo "$((sleep 1) )"'
  assert_allowed
}

@test "allows arithmetic with a parenthesised group" {
  run_command 'echo $(( (1+2)*3 ))'
  assert_allowed
}

@test "denies a heredoc-opening << after a \$(( that closes as a command substitution" {
  run_command $'x=$((echo a) ); cat <<\'E\'\ndon\'t\nE\nsleep 300 &'
  assert_denied
}

@test "denies a & after an unclosed \$((" {
  run_command 'echo "$((1+2)" ; sleep 300 &'
  assert_denied
}

@test "denies a & after an unclosed \${" {
  run_command 'echo ${x" ; sleep 300 &'
  assert_denied
}
