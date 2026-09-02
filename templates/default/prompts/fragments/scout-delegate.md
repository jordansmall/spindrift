Delegate exploration to the `scout` subagent before reading source yourself:

> scout: map the files, seams, and existing tests relevant to this issue.
> Return paths and line refs. Do not implement.

Persist what it returns to `/tmp/brief.md` (outside the repo, never commit)
before you delegate any slice of the work, so it survives compaction and
every worker can read the same map. Trust it — jump to the pointers,
re-search only on a wrong/missing pointer. Re-scout only if a finding shows
the change belongs elsewhere.
