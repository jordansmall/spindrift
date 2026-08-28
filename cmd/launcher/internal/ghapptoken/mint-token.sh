#!/usr/bin/env bash
# Single source of truth for minting a GitHub App installation token. Both
# .github/actions/gh-token-refresher (CI) and this package's Mint() (issue
# #2867) invoke this exact recipe rather than maintaining parallel copies of
# it. JWT signing follows GitHub's documented recipe: RS256 over
# base64url(header).base64url(payload), a <=10m exp, and iss set to the
# numeric App ID.
set -euo pipefail

: "${GH_APP_ID:?GH_APP_ID must be set}"
: "${GH_APP_PRIVATE_KEY_FILE:?GH_APP_PRIVATE_KEY_FILE must be set}"
: "${GH_APP_INSTALLATION_ID:?GH_APP_INSTALLATION_ID must be set}"

# Both flow unquoted into a JSON literal (iss, below) and a URL path
# segment (the access_tokens request, below) -- reject anything non-numeric
# here with a clear message naming the bad knob, rather than a malformed
# JSON payload or an opaque jq/curl failure downstream.
case "$GH_APP_ID" in
  '' | *[!0-9]*)
    echo "mint-token.sh: GH_APP_ID must be numeric, got: $GH_APP_ID" >&2
    exit 1
    ;;
esac
case "$GH_APP_INSTALLATION_ID" in
  '' | *[!0-9]*)
    echo "mint-token.sh: GH_APP_INSTALLATION_ID must be numeric, got: $GH_APP_INSTALLATION_ID" >&2
    exit 1
    ;;
esac

b64url() { openssl base64 -A | tr '+/' '-_' | tr -d '='; }

now=$(date +%s)
header=$(printf '{"alg":"RS256","typ":"JWT"}' | b64url)
payload=$(printf '{"iat":%d,"exp":%d,"iss":%s}' "$((now - 60))" "$((now + 540))" "$GH_APP_ID" | b64url)
sig=$(printf '%s.%s' "$header" "$payload" | openssl dgst -sha256 -sign "$GH_APP_PRIVATE_KEY_FILE" | b64url)
jwt="$header.$payload.$sig"

token=$(curl -sf -X POST \
  -H "Authorization: Bearer $jwt" \
  -H "Accept: application/vnd.github+json" \
  "https://api.github.com/app/installations/$GH_APP_INSTALLATION_ID/access_tokens" \
  | jq -er '.token')

printf '%s' "$token"
