A `worker` subagent is provisioned this run, so run IMPLEMENT as its
**coordinator** rather than editing the source yourself. Use the scout brief
to break the issue into an ordered set of small slices, then
delegate each slice **sequentially** to the `worker` with `isolation:
"worktree"` on the Agent call (issue #2058) — the worker commits on its own
branch inside its own git worktree, never the coordinator's own tree:

> worker: implement <one slice, with the brief's relevant pointers>. Work
> test-first, then commit your slice on your own branch. Return a concise
> report — files touched, checks run, outcome — not the diffs.

Before your first `git add`, confirm `.claude/` is gitignored in this
repo — worker worktrees materialize under `$WORK_DIR/.claude/worktrees/`
inside your own working tree, and nothing guarantees this repo's
`.gitignore` already excludes `.claude/`. Append it to `.gitignore` if it's
missing, so a worker's whole worktree checkout is never staged into your own
commit.

Hand the worker one slice at a time and wait for its report before starting
the next. Keep only that summary in your own context — let the bulk diffs,
file reads, and check logs live in the worker's context, not yours. If a
slice's report shows it went wrong, refine the delegation and re-run that
slice before moving on.

A completed slice's Agent-tool result names the worker's worktree branch (a
missing branch line means the worker made no changes — nothing to
integrate). The worker's worktree is cut from whatever base the harness's
own worktree-isolation setting uses, not your own tree and not necessarily
`${BASE_BRANCH}` — never assume a specific base ref when integrating, so a
worker never sees an earlier slice's work — scope slices to distinct files
where you can. Bring a named branch's diff into your own tree with
`git cherry-pick --no-commit $(git merge-base HEAD <branch>)..<branch>`,
resolving any conflict by hand, then author the commit yourself so it lands
in proper Conventional Commits form — do this before running that slice's
checks or handing out the next one.

You still own CHECK, COMMIT, REVIEW, and OUTCOME yourself: the worker only
implements and commits each slice inside its own worktree; the coordinator
integrates each worker's branch, keeps the checks green, reviews, and
finalizes commits. The one-slice, test-first Hard rule below is what each
delegated slice must satisfy — you enforce it; the worker performs each
red-green-refactor cycle.
