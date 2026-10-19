3. File one issue per surviving finding: `fj issue create "<title>" --body
   "<body>"`. fj issue create has no label flag, so attach the
   `agent-review-finding` label to the new issue over the REST API:
   `curl -fsS -X POST -H "Authorization: token ${FORGEJO_TOKEN}" -H "Content-Type: application/json" "${FORGEJO_BASE_URL:-https://codeberg.org}/api/v1/repos/${REPO_SLUG}/issues/<new-issue-number>/labels" -d '{"labels":["agent-review-finding"]}'`.
   Merge findings into a single issue only when they are the same change (e.g.
   the same file/function/fix) — never merge unrelated findings just to reduce
   issue count. You file with your own token, so the launcher never sees this
   intent and cannot dedup it host-side — end the body with one
   `<!-- spindrift-dedup: <site key> -->` line carrying step 2's site keys
   joined by `, `, so a later run's dedup search finds it.
