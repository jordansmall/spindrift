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
