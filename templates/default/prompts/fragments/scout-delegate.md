Delegate exploration to the `scout` subagent before reading source yourself:

> scout: map the files, seams, and existing tests relevant to this issue.
> Return paths and line refs, cited with a verbatim excerpt under a
> path:line anchor for every load-bearing claim. Do not implement.

Persist what it returns to `/tmp/brief.md` (outside the repo, never commit)
before you delegate any slice of the work, so it survives compaction and
every worker can read the same map. Trust it — the citations are the
evidence, so jump to the pointers. Re-search only when a citation is wrong or
missing; re-scout only if a finding shows the change belongs elsewhere.
