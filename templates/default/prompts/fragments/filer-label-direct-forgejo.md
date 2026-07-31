1. Ensure the `agent-review-finding` label exists on the repo — idempotent,
   never fail if it already does. fj has no label verb, so use the REST API:
     curl -fsS -X POST -H "Authorization: token ${FORGEJO_TOKEN}" \
       -H "Content-Type: application/json" \
       "${FORGEJO_BASE_URL:-https://codeberg.org}/api/v1/repos/${REPO_SLUG}/labels" \
       -d '{"name":"agent-review-finding","color":"#d4c5f9","description":"Filed from a non-blocking review finding"}' \
       >/dev/null 2>&1 || true

