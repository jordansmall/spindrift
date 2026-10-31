**Always rebase onto the latest base immediately before finishing** — never
leave the branch on a stale base. The launcher lands your branch by rebasing
it onto the Integration branch's tip and fast-forwarding, without re-running
any checks, and a branch that conflicts with that tip does not land at all:
the landing is blocked, and no Box is sent to resolve it. Rebasing here
surfaces any conflict while you can still resolve it, and keeps your checks
running against the current base rather than a stale one:

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
nothing. Otherwise skip it.

The repo you cloned from is mounted read-only — do NOT `git push` and do NOT
run `git bundle create`. Leave your work committed on the branch: after you
exit the harness bundles your committed branch out and the launcher lands it
host-side.
