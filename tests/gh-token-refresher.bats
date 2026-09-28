#!/usr/bin/env bats
# Issue #3940. mint_token (.github/actions/gh-token-refresher/action.yml) must
# never overwrite the refresh token file on a failed mint: bash suspends
# errexit for a function used as an `if` condition, so the fix is an explicit
# `return 1` inside the function rather than relying on `set -e`. Extracts the
# function verbatim from the action's `run:` block so this test tracks the
# real body, not a hand-copied stand-in that can drift.

setup() {
  GH_TOKEN_REFRESHER_ACTION_YML="${GH_TOKEN_REFRESHER_ACTION_YML:-$BATS_TEST_DIRNAME/../.github/actions/gh-token-refresher/action.yml}"

  mkdir -p "$BATS_TEST_TMPDIR/bin"
  PATH="$BATS_TEST_TMPDIR/bin:$PATH"

  MINT_TOKEN_SRC="$BATS_TEST_TMPDIR/mint_token.sh"
  {
    sed -n '/^        b64url() {/p' "$GH_TOKEN_REFRESHER_ACTION_YML"
    sed -n '/^        mint_token() {$/,/^        }$/p' "$GH_TOKEN_REFRESHER_ACTION_YML"
  } > "$MINT_TOKEN_SRC"
  [ -s "$MINT_TOKEN_SRC" ]
  # A missed extraction would otherwise surface as a status-127 "failure"
  # that the failure cases pass on vacuously; a reformat that broke the
  # extracted body would otherwise surface the same way.
  grep -q '^ *b64url() {' "$MINT_TOKEN_SRC"
  grep -q '^ *mint_token() {' "$MINT_TOKEN_SRC"
  bash -n "$MINT_TOKEN_SRC"

  key_file="$BATS_TEST_TMPDIR/key.pem"
  : > "$key_file"
  token_file="$BATS_TEST_TMPDIR/token"
  APP_ID=123
  INSTALLATION_ID=456
  export key_file token_file APP_ID INSTALLATION_ID MINT_TOKEN_SRC

  # openssl is stubbed for every case: mint_token only needs *some* bytes
  # out of the base64/dgst subcommands it drives, never real JWT crypto.
  write_stub openssl <<'EOF'
cat >/dev/null
printf 'stub'
EOF
}

# Writes a stub script named "$1" into "$BATS_TEST_TMPDIR/bin", reading its
# body from stdin. Shebang resolves the sandbox's real bash rather than
# /usr/bin/env, which the nix build sandbox does not have (tests/helper.bash's
# stub_failing_bind_registry does the same).
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
cat "$body_file"
EOF
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
