**Always rebase onto the latest base immediately before finishing** — never
leave the branch on a stale base. This keeps the branch's tested tree current
with any siblings that landed while you worked: the launcher merges a green PR
as-is and does not re-rebase it for you, so a fresh base is the branch's
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
nothing. Otherwise skip it and let CI be the final gate.

Your token is read-only and you take no code-out action yourself — do NOT
`git push` and do NOT run `git bundle create`. Leave your work committed on
the branch: after you exit the harness relays your committed branch out and
the launcher pushes it host-side with its own token.
