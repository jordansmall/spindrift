   - Search for the site key over open issues first — the most reliable
     check, since it matches the stable key rather than prose:
       driver-exec signal issue-search -state open '<site key>'
   - Search ALL open issues, regardless of label — an open issue describing
     the same problem means it's already tracked, whether human-filed,
     `ready-for-agent`, filed via `/to-tickets`, or from a prior Filer run:
       driver-exec signal issue-search -state open '<terms>'
   - Search closed issues carrying `agent-review-finding`,
     `agent-research-reject`, `agent-research-finding`, OR
     `agent-butler-finding` — all four mark a deliberate triage decision, and
     none of them is ever refiled:
       driver-exec signal issue-search -state closed -label agent-review-finding '<terms>'
       driver-exec signal issue-search -state closed -label agent-research-reject '<terms>'
       driver-exec signal issue-search -state closed -label agent-research-finding '<terms>'
       driver-exec signal issue-search -state closed -label agent-butler-finding '<terms>'
   - A plain closed issue carrying none of these labels does NOT suppress
     filing — a problem that was fixed and later regressed can still be
     refiled.
   Forgejo's keyword search is stricter than GitHub's all-terms search: use a
   few distinctive terms, and run more than one narrow query rather than one
   long phrase.
   Skip any finding that matches an existing issue in any search, by site
   key or by subject. A `rejected (...)` reply means the search did not run:
   treat it as no search, and rely on the host's site-key dedup.
