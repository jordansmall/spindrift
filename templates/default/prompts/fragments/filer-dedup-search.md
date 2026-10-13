   - Search for the site key over open issues first — the most reliable
     check, since it matches the stable key rather than prose:
       gh issue list --state open --search '"<site key>" in:body'
   - Search ALL open issues, regardless of label — an open issue describing
     the same problem means it's already tracked, whether human-filed,
     `ready-for-agent`, filed via `/to-tickets`, or from a prior Filer run:
       gh issue list --state open --search "<terms>"
   - Search closed issues carrying `agent-review-finding`,
     `agent-research-reject`, `agent-research-finding`, OR
     `agent-butler-finding` — all four mark a deliberate triage decision, and
     none of them is ever refiled:
       gh issue list --label agent-review-finding --state closed --search "<terms>"
       gh issue list --label agent-research-reject --state closed --search "<terms>"
       gh issue list --label agent-research-finding --state closed --search "<terms>"
       gh issue list --label agent-butler-finding --state closed --search "<terms>"
   - A plain closed issue carrying none of these labels does NOT suppress
     filing — a problem that was fixed and later regressed can still be
     refiled.
   Skip any finding that matches an existing issue in any search, by site
   key or by subject.
