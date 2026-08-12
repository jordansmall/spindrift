**Always rebase onto the latest base immediately before every push** — never
push from a stale base. This keeps the branch's tested tree current with any
siblings that landed while you worked: the launcher merges a green PR as-is and
does not re-rebase it for you, so a fresh base at push time is the branch's
freshness guarantee (a stale base also produces phantom diffs that trip push
guards):

```
git fetch origin
git rebase origin/${BASE_BRANCH}
```

Re-run the repo's checks after rebasing, then push:

```
git push --force-with-lease -u origin ${BRANCH}   # first push
git push --force-with-lease                        # subsequent
```

**If a push is rejected**, do NOT silently strand the commits. Retry exactly
once:

1. `git fetch origin`
2. `git rebase origin/${BASE_BRANCH}` — resolve any conflicts, re-run checks.
3. `git push --force-with-lease` — one retry only.

If the push still fails after the retry, follow IF BLOCKED.
