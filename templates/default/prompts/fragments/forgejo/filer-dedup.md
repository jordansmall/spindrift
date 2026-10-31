   - Search for the site key over open issues first — the most reliable
     check, since it matches the stable key rather than prose:
       curl -fsS -G -H "Authorization: token ${FORGEJO_TOKEN}" \
         "${FORGEJO_BASE_URL:-https://codeberg.org}/api/v1/repos/${REPO_SLUG}/issues" \
         --data-urlencode "type=issues" --data-urlencode "state=open" \
         --data-urlencode "q=<site key>" \
         | jq -r '.[] | "#\(.number) \(.title)"'
   - Search ALL open issues, regardless of label — an open issue describing
     the same problem means it's already tracked, whether human-filed,
     `ready-for-agent`, filed via `/to-tickets`, or from a prior Filer run:
       curl -fsS -G -H "Authorization: token ${FORGEJO_TOKEN}" \
         "${FORGEJO_BASE_URL:-https://codeberg.org}/api/v1/repos/${REPO_SLUG}/issues" \
         --data-urlencode "type=issues" --data-urlencode "state=open" \
         --data-urlencode "q=<terms>" \
         | jq -r '.[] | "#\(.number) \(.title)"'
   - Search closed issues carrying `agent-review-finding`,
     `agent-research-reject`, `agent-research-finding`, OR
     `agent-butler-finding` — all four mark a deliberate triage decision, and
     none of them is ever refiled. Forgejo ignores a `labels=` name the repo
     does not define and returns every closed issue matching `q=`, so each
     `jq` keeps only results that carry that search's label:
       curl -fsS -G -H "Authorization: token ${FORGEJO_TOKEN}" \
         "${FORGEJO_BASE_URL:-https://codeberg.org}/api/v1/repos/${REPO_SLUG}/issues" \
         --data-urlencode "type=issues" --data-urlencode "state=closed" \
         --data-urlencode "labels=agent-review-finding" --data-urlencode "q=<terms>" \
         | jq -r '.[] | select(any(.labels[]?; .name == "agent-review-finding")) | "#\(.number) \(.title)"'
       curl -fsS -G -H "Authorization: token ${FORGEJO_TOKEN}" \
         "${FORGEJO_BASE_URL:-https://codeberg.org}/api/v1/repos/${REPO_SLUG}/issues" \
         --data-urlencode "type=issues" --data-urlencode "state=closed" \
         --data-urlencode "labels=agent-research-reject" --data-urlencode "q=<terms>" \
         | jq -r '.[] | select(any(.labels[]?; .name == "agent-research-reject")) | "#\(.number) \(.title)"'
       curl -fsS -G -H "Authorization: token ${FORGEJO_TOKEN}" \
         "${FORGEJO_BASE_URL:-https://codeberg.org}/api/v1/repos/${REPO_SLUG}/issues" \
         --data-urlencode "type=issues" --data-urlencode "state=closed" \
         --data-urlencode "labels=agent-research-finding" --data-urlencode "q=<terms>" \
         | jq -r '.[] | select(any(.labels[]?; .name == "agent-research-finding")) | "#\(.number) \(.title)"'
       curl -fsS -G -H "Authorization: token ${FORGEJO_TOKEN}" \
         "${FORGEJO_BASE_URL:-https://codeberg.org}/api/v1/repos/${REPO_SLUG}/issues" \
         --data-urlencode "type=issues" --data-urlencode "state=closed" \
         --data-urlencode "labels=agent-butler-finding" --data-urlencode "q=<terms>" \
         | jq -r '.[] | select(any(.labels[]?; .name == "agent-butler-finding")) | "#\(.number) \(.title)"'
   - A plain closed issue carrying none of these labels does NOT suppress
     filing — a problem that was fixed and later regressed can still be
     refiled.
   Forgejo's keyword search is stricter than GitHub's all-terms search: use a
   few distinctive terms, and run more than one narrow query rather than one
   long phrase.
   Skip any finding that matches an existing issue in any search, by site
   key or by subject.
