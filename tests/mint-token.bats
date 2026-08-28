#!/usr/bin/env bats
# cmd/launcher/internal/ghapptoken/mint-token.sh (issue #2867) is the shared
# JWT-sign-and-exchange recipe both .github/actions/gh-token-refresher and
# ghapptoken.Mint() invoke -- but every Go test in that package fakes the
# script out entirely (mintWithScript/watchWithScript), so the recipe itself
# has zero executed coverage. This suite runs the real script end-to-end,
# faking out only the two things a sandboxed test genuinely cannot do for
# real: network (curl, tests/fakes/curl) and RSA signing (openssl, tests/
# fakes/openssl) -- proving the env-var guards, JWT construction, and
# token-extraction logic all run for real.

load helper

@test "mints a token by POSTing a Bearer JWT to the installation's access_tokens endpoint (issue #2867)" {
  export GH_APP_ID=123
  export GH_APP_INSTALLATION_ID=456
  export GH_APP_PRIVATE_KEY_FILE="$BATS_TEST_TMPDIR/key.pem"
  echo fake-key > "$GH_APP_PRIVATE_KEY_FILE"
  export FAKE_CURL_LOG="$BATS_TEST_TMPDIR/curl.log"
  export PATH="$FAKES_DIR:$PATH"

  run bash "$MINT_TOKEN_SCRIPT"
  [ "$status" -eq 0 ]
  [ "$output" = "faked-installation-token" ]

  grep -q 'https://api.github.com/app/installations/456/access_tokens' "$FAKE_CURL_LOG"
  grep -q 'Authorization: Bearer ' "$FAKE_CURL_LOG"
}

@test "fails fast when a required GH_APP_* var is unset (issue #2867)" {
  unset GH_APP_ID
  export GH_APP_INSTALLATION_ID=456
  export GH_APP_PRIVATE_KEY_FILE="$BATS_TEST_TMPDIR/key.pem"
  echo fake-key > "$GH_APP_PRIVATE_KEY_FILE"
  export FAKE_CURL_LOG="$BATS_TEST_TMPDIR/curl.log"
  export PATH="$FAKES_DIR:$PATH"

  run bash "$MINT_TOKEN_SCRIPT"
  [ "$status" -ne 0 ]
  [[ "$output" == *"GH_APP_ID"* ]]
}

@test "propagates a failed token exchange instead of minting a broken token (issue #2867)" {
  export GH_APP_ID=123
  export GH_APP_INSTALLATION_ID=456
  export GH_APP_PRIVATE_KEY_FILE="$BATS_TEST_TMPDIR/key.pem"
  echo fake-key > "$GH_APP_PRIVATE_KEY_FILE"
  export FAKE_CURL_LOG="$BATS_TEST_TMPDIR/curl.log"
  export FAKE_CURL_EXIT_CODE=22
  export PATH="$FAKES_DIR:$PATH"

  run bash "$MINT_TOKEN_SCRIPT"
  [ "$status" -ne 0 ]
  [ "$output" != "faked-installation-token" ]
}
