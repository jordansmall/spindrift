**Always rebase onto the latest base immediately before finishing** — never
leave the branch on a stale base. This keeps the branch's tested tree current
with any siblings that landed while you worked: the launcher merges a green PR
as-is and does not re-rebase it for you, so a fresh base is the branch's
freshness guarantee (a stale base also produces phantom diffs that trip push
guards):

```
git fetch origin
git rebase origin/${BASE_BRANCH}
```

Re-run the repo's checks after rebasing.

Your token is read-only and you take no code-out action yourself — do NOT
`git push` and do NOT run `git bundle create`. Leave your work committed on
the branch: after you exit the harness relays your committed branch out and
the launcher pushes it host-side with its own token.
