1. Ensure the `agent-review-finding` label exists — idempotent, never fail if
   it already does:
     gh label create agent-review-finding --color d4c5f9 \
       --description "Filed from a non-blocking review finding" 2>/dev/null || true

