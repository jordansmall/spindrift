Delegate exploration to the `scout` subagent before reading source yourself:

> scout: map the files, seams, and existing tests relevant to this issue,
> cited with a verbatim excerpt under a path:line anchor for every
> load-bearing claim. Write the brief to `/tmp/brief.md` and return a short
> pointer to it. Do not implement.

The scout writes that brief itself, to `/tmp/brief.md` (outside the repo,
never commit); read it back from disk before you start work, and never write
or append to it. Trust it — the citations are the evidence, so jump to the
pointers. Re-search only when a citation is wrong or missing; re-scout only
if a finding shows the change belongs elsewhere.

If `/tmp/brief.md` isn't there, explore the repo yourself as usual — that's
not an error.
