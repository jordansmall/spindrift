#!/usr/bin/env bats
# Issue #3940: mint_token (.github/actions/gh-token-refresher/action.yml) must
# never overwrite the refresh token file on a failed mint. bash suspends errexit
# for a function used as an `if` condition, so the fix is an explicit
# `return 1` inside the function rather than relying on `set -e`.
# Issue #1519 additions: the JWT and curl request mint_token builds, the
# refresh loop's 45m/5m backoff, and the whole `run:` block's detachment.
# Everything is extracted from action.yml (only the YAML indent stripped) so
# these tests track the real body, not a hand-copied stand-in that can drift.

bats_require_minimum_version 1.5.0

setup() {
  GH_TOKEN_REFRESHER_ACTION_YML="${GH_TOKEN_REFRESHER_ACTION_YML:-$BATS_TEST_DIRNAME/../.github/actions/gh-token-refresher/action.yml}"

  mkdir -p "$BATS_TEST_TMPDIR/bin"
  PATH="$BATS_TEST_TMPDIR/bin:$PATH"

  MINT_TOKEN_SRC="$BATS_TEST_TMPDIR/mint_token.sh"
  {
    sed -n '/^        b64url() {/p' "$GH_TOKEN_REFRESHER_ACTION_YML"
    sed -n '/^        mint_token() {$/,/^        }$/p' "$GH_TOKEN_REFRESHER_ACTION_YML"
  } > "$MINT_TOKEN_SRC"
  # A missed extraction would otherwise surface as a status-127 "failure"
  # that the failure cases pass on vacuously.
  assert_extracted "$MINT_TOKEN_SRC" '^ *b64url() {' '^ *mint_token() {'

  key_file="$BATS_TEST_TMPDIR/key.pem"
  : > "$key_file"
  token_file="$BATS_TEST_TMPDIR/token"
  APP_ID=123
  INSTALLATION_ID=456
  pidfile="$BATS_TEST_TMPDIR/loop-pids"
  pgid_file="$BATS_TEST_TMPDIR/loop-pgid"
  export key_file token_file APP_ID INSTALLATION_ID MINT_TOKEN_SRC

  # openssl is stubbed for every case since the nix build sandbox has none.
  # `base64` mirrors real wrapping (64 columns unless -A), so a dropped -A
  # leaks newlines into a segment. `dgst` records its argv and stdin and prints
  # a 256-byte (RS256-sized) signature whose base64 holds `+`, `/` and `==`
  # padding, so the b64url alphabet and padding steps are exercised.
  sig_file="$BATS_TEST_TMPDIR/sig.bin"
  { printf '\xfb\xef\xbe'; head -c 253 /dev/zero | tr '\0' '\377'; } > "$sig_file"
  [ "$(stat -c %s "$sig_file")" -eq 256 ]
  write_stub openssl <<EOF
case "\$1" in
  base64) if [ "\${2:-}" = -A ]; then exec base64 -w0; else exec base64 -w64; fi ;;
  dgst)
    printf '%s\\n' "\$@" > "$BATS_TEST_TMPDIR/openssl-dgst-argv"
    cat > "$BATS_TEST_TMPDIR/openssl-dgst-stdin"
    cat "$sig_file"
    ;;
  *) exit 1 ;;
esac
EOF
}

# The run-block test backgrounds a loop that outlives the code under test, so
# it is killed by process group: the group id is recorded before that code
# runs, so no regression in it can leave the loop unreachable.
teardown() {
  if [ -s "${pgid_file:-}" ]; then
    kill -- -"$(cat "$pgid_file")" 2>/dev/null || true
  fi
}

# Guards an extraction against passing vacuously: it must be non-empty, hold
# every given grep pattern, and still parse as bash after a reformat.
assert_extracted() {
  local file=$1 pattern
  shift
  [ -s "$file" ]
  for pattern in "$@"; do
    grep -q -- "$pattern" "$file"
  done
  bash -n "$file"
}

# Writes a stub script named "$1" into "$BATS_TEST_TMPDIR/bin", reading its
# body from stdin. Shebang resolves the sandbox's real bash rather than
# /usr/bin/env, which the nix build sandbox does not have.
write_stub() {
  {
    printf '#!%s\n' "$(command -v bash)"
    cat
  } > "$BATS_TEST_TMPDIR/bin/$1"
  chmod +x "$BATS_TEST_TMPDIR/bin/$1"
}

stub_curl_failing() {
  write_stub curl <<'EOF'
exit 1
EOF
}

stub_curl_body() {
  # Via a file, so quotes in the body can't break the stub script.
  local body_file="$BATS_TEST_TMPDIR/curl-body"
  printf '%s' "$1" > "$body_file"
  write_stub curl <<EOF
printf '%s\\n' "\$@" > "$BATS_TEST_TMPDIR/curl-argv"
cat "$body_file"
EOF
}

# Reverses b64url: restores the alphabet and padding, then decodes.
b64url_decode() {
  local s
  s=$(printf '%s' "$1" | tr '_-' '/+')
  while [ $(( ${#s} % 4 )) -ne 0 ]; do s="$s="; done
  printf '%s' "$s" | base64 -d
}

run_mint_token() {
  run bash -c '
    set -e -o pipefail
    source "$MINT_TOKEN_SRC"
    if mint_token; then exit 0; else exit 1; fi
  '
}

# Seeds the token file, runs mint_token, and asserts it failed without
# disturbing the token file. The `-L` check precedes the `cat`: if a failed
# write to a /dev/full "$token_file.tmp" symlink still reached the mv, the
# token file is that symlink and `cat` would hang on infinite zeros.
assert_mint_fails_preserving_token() {
  printf 'pre-existing-token' > "$token_file"

  run_mint_token

  [ "$status" -ne 0 ]
  [ ! -L "$token_file" ]
  [ "$(cat "$token_file")" = "pre-existing-token" ]
}

@test "mint_token fails and leaves the token file untouched when curl fails" {
  stub_curl_failing

  assert_mint_fails_preserving_token
}

@test "mint_token fails and leaves the token file untouched when the API returns an empty token" {
  stub_curl_body '{"token":""}'

  assert_mint_fails_preserving_token
}

@test "mint_token fails and leaves the token file untouched when the API returns no token field" {
  stub_curl_body '{}'

  assert_mint_fails_preserving_token
}

@test "mint_token fails and leaves the token file untouched when writing the temp file fails" {
  stub_curl_body '{"token":"tok"}'
  ln -s /dev/full "$token_file.tmp"

  assert_mint_fails_preserving_token
}

@test "mint_token succeeds and writes the token file" {
  stub_curl_body '{"token":"tok"}'

  run_mint_token

  [ "$status" -eq 0 ]
  [ "$(cat "$token_file")" = "tok" ]
}

@test "mint_token signs an RS256 JWT and POSTs it to the installation token endpoint" {
  write_stub date <<'EOF'
printf '1700000000\n'
EOF
  stub_curl_body '{"token":"tok"}'
  local curl_argv="$BATS_TEST_TMPDIR/curl-argv"

  run_mint_token

  [ "$status" -eq 0 ]

  # Argv is one arg per line, so flag/value pairing is asserted by adjacency.
  local argv jwt
  argv=$(cat "$curl_argv")
  [[ "$argv" == *$'-X\nPOST\n'* ]]
  [[ "$argv" == *$'-H\nAccept: application/vnd.github+json\n'* ]]
  [[ "$argv" == *$'\nhttps://api.github.com/app/installations/456/access_tokens' ]]
  jwt=$(grep -m1 '^Authorization: Bearer ' "$curl_argv")
  jwt=${jwt#Authorization: Bearer }
  [[ "$argv" == *$'-H\nAuthorization: Bearer '"$jwt"$'\n'* ]]

  [[ "$jwt" =~ ^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$ ]]
  local header payload sig
  IFS=. read -r header payload sig <<< "$jwt"
  [ "$(b64url_decode "$header")" = '{"alg":"RS256","typ":"JWT"}' ]
  [ "$(b64url_decode "$payload")" = '{"iat":1699999940,"exp":1700000540,"iss":123}' ]
  b64url_decode "$sig" > "$BATS_TEST_TMPDIR/sig-decoded"
  cmp "$sig_file" "$BATS_TEST_TMPDIR/sig-decoded"

  [ "$(cat "$BATS_TEST_TMPDIR/openssl-dgst-argv")" = "$(printf '%s\n' dgst -sha256 -sign "$key_file")" ]
  [ "$(cat "$BATS_TEST_TMPDIR/openssl-dgst-stdin")" = "$header.$payload" ]
}

# The loop body is sourced into a shell where `sleep` and `mint_token` are
# functions, so the backoff sequence is observable without waiting out real
# intervals. `sleep` exits the shell on its 3rd call to end the `while true`.
@test "the refresh loop sleeps 45m, backs off to 5m after a failed mint, and returns to 45m on success" {
  local loop_src="$BATS_TEST_TMPDIR/loop.sh"
  sed -n '/^          sleep_secs=/,/^          done$/p' "$GH_TOKEN_REFRESHER_ACTION_YML" > "$loop_src"
  assert_extracted "$loop_src" 'while true; do'

  export LOOP_SRC="$loop_src" SLEEP_LOG="$BATS_TEST_TMPDIR/sleep-log"
  # shellcheck disable=SC2016
  run --separate-stderr timeout 10 bash -c '
    set -e -o pipefail
    sleep() {
      printf "%s\n" "$1" >> "$SLEEP_LOG"
      if [ "$(wc -l < "$SLEEP_LOG")" -ge 3 ]; then exit 0; fi
    }
    mints=0
    mint_token() {
      mints=$((mints + 1))
      [ "$mints" -ne 1 ]
    }
    source "$LOOP_SRC"
  '

  [ "$status" -eq 0 ]
  [ "$(cat "$SLEEP_LOG")" = "$(printf '%s\n' 2700 300 2700)" ]
  [ "$stderr" = "gh-token-refresher: mint attempt failed, retrying in 5m" ]
}

# Runs the whole `run:` body the way Actions does, and requires it to return
# while the backgrounded loop is still asleep with its fds detached. Not under
# `run`: a leaked fd would keep bats' capture pipe open and hang the suite
# instead of failing, so output goes to a file and the fds are checked directly.
@test "the run block returns promptly with the loop backgrounded, writing owner-only token and key files" {
  local run_src="$BATS_TEST_TMPDIR/run.sh"
  # Stops at the first line not indented like the `run:` body, so a step added
  # after this one is not pulled in.
  awk '/^      run: \|$/ { on = 1; next }
       on && NF && !/^        / { exit }
       on { sub(/^        /, ""); print }' "$GH_TOKEN_REFRESHER_ACTION_YML" > "$run_src"
  assert_extracted "$run_src" '^mint_token() {'

  # Must outlast the poll below plus teardown, so the loop is still parked
  # when its fds are inspected.
  local park_secs=60
  # The stub records the loop subshell's pid ($PPID) and its own, then execs
  # the real sleep so the loop stays parked, as it would for 45m in CI.
  local real_sleep
  real_sleep=$(command -v sleep)
  write_stub sleep <<EOF
printf '%s %s\\n' "\$PPID" "\$\$" > "$pidfile"
exec "$real_sleep" $park_secs
EOF
  # A loop that skips its sleep spins on mint_token; keep it off the network.
  stub_curl_failing

  export RUNNER_TEMP="$BATS_TEST_TMPDIR" GITHUB_ENV="$BATS_TEST_TMPDIR/github-env"
  export INITIAL_TOKEN=initial APP_PRIVATE_KEY=dummy
  local stdin_file="$BATS_TEST_TMPDIR/block-stdin" out="$BATS_TEST_TMPDIR/block-out"
  : > "$stdin_file"

  # set -m gives the job its own process group (pgid == $!), recorded before
  # the block runs. fd 3 is closed because bats waits on it and the loop
  # would inherit it. `timeout` bounds a block that fails to return.
  local rc=0
  (
    set -m
    timeout 10 bash -e -o pipefail "$run_src" < "$stdin_file" > "$out" 2>&1 3>&- &
    echo "$!" > "$pgid_file"
    wait "$!"
  ) || rc=$?

  [ "$rc" -eq 0 ]
  [ "$(cat "$GITHUB_ENV")" = "GH_TOKEN_REFRESH_FILE=$BATS_TEST_TMPDIR/gh-token-refresh" ]
  [ "$(cat "$BATS_TEST_TMPDIR/gh-token-refresh")" = "initial" ]

  # The loop removes the key file on exit, so check it while the loop sleeps.
  local _
  for _ in $(seq 50); do
    [ -s "$pidfile" ] && break
    "$real_sleep" 0.1
  done
  [ -s "$pidfile" ]
  [ "$(stat -c %a "$BATS_TEST_TMPDIR/gh-token-refresh")" = 600 ]
  [ "$(stat -c %a "$BATS_TEST_TMPDIR/gh-app-private-key.pem")" = 600 ]

  local loop_pid
  read -r loop_pid _ < "$pidfile"
  [ "$(readlink "/proc/$loop_pid/fd/0")" = /dev/null ]
  [ "$(readlink "/proc/$loop_pid/fd/1")" = "$BATS_TEST_TMPDIR/gh-token-refresher.log" ]
  [ "$(readlink "/proc/$loop_pid/fd/2")" = "$BATS_TEST_TMPDIR/gh-token-refresher.log" ]
}
