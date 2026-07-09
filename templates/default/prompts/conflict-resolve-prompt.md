# TASK

A `git rebase` onto `${BASE_BRANCH}` left conflicts in the working tree of
branch `${BRANCH}`. Resolve them and complete the rebase.

# WHAT TO DO

1. Run `git status` to see which files have conflicts.
2. For each conflicted file, resolve it — choose the correct version or merge
   both sides as the change history demands.
3. Stage every resolved file: `git add <file>` (or `git add .`).
4. Complete the rebase: `GIT_EDITOR=true git rebase --continue`.
   Repeat if `git status` shows more conflicts from subsequent commits.
5. When the rebase finishes cleanly (`git status` shows no rebase in progress),
   you are done. Do NOT open a PR or push — the caller handles that.

Do not narrate between tool calls; the only text you output is the short
explanation described below if the conflict is unresolvable.

# SIGNALS

- The rebase is complete when `.git/rebase-merge` and `.git/rebase-apply`
  directories no longer exist.
- If the conflict is genuinely unresolvable (e.g. the two changes are
  semantically incompatible), exit and explain in a short message.
