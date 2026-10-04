**Always rebase onto the latest base immediately before every push** — never
push from a stale base. This keeps the branch's tested tree current with any
siblings that landed while you worked: the launcher merges a green PR as-is and
does not re-rebase it for you, so a fresh base at push time is the branch's
freshness guarantee (a stale base also produces phantom diffs that trip push
guards):

```
git fetch origin
git merge-base HEAD origin/${BASE_BRANCH}
git rebase origin/${BASE_BRANCH}
```

**After-rebase rule:** re-run the full check gate only if the rebase hit
conflicts or brought in changes to files this branch touches — compare
`git diff --name-only <old-base> origin/${BASE_BRANCH}`, where `<old-base>`
is the SHA the `git merge-base` line printed (what the base brought in),
against `git diff --name-only origin/${BASE_BRANCH}...HEAD` (what the branch
touches). A rebase that reports the branch already up to date brought in
nothing. Otherwise skip it and let CI be the final gate. Then push:

```
git push --force-with-lease -u origin ${BRANCH}   # first push
git push --force-with-lease                        # subsequent
```

**If a push is rejected**, do NOT silently strand the commits. Retry exactly
once:

1. `git fetch origin`
2. `git merge-base HEAD origin/${BASE_BRANCH}` — note the SHA it prints.
3. `git rebase origin/${BASE_BRANCH}` — resolve any conflicts, then re-run
   the full check gate only under the same after-rebase rule.
4. `git push --force-with-lease` — one retry only.

If the push still fails after the retry, follow IF BLOCKED.
