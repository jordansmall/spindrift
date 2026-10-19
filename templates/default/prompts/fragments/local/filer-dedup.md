   - ISSUE_TRACKER=local has no in-Box issue search, and `gh` would query the
     wrong tracker, so run no tracker search at all. The host dedups each
     relayed intent against the backlog by exact site key and overlapping
     line range only — never by subject — so the batch self-dedup above and
     a stable site key are the whole dedup.
