A `worker` subagent is provisioned this run, so run IMPLEMENT as its
**coordinator** rather than editing the source yourself. Use the scout brief
to break the issue into an ordered set of small slices, then
delegate each slice **sequentially** to the `worker` with `isolation:
"worktree"` on the Agent call (issue #2058) — the worker commits on its own
branch inside its own git worktree, never the coordinator's own tree:

> worker: implement <one slice, with the brief's relevant pointers>. Work
> test-first, then commit your slice on your own branch. Return a concise
> report — files touched, checks run, outcome, and your branch name —
> not the diffs.

Hand the worker one slice at a time and wait for its report before starting
the next. Keep only that summary in your own context — let the bulk diffs,
file reads, and check logs live in the worker's context, not yours. If a
slice's report shows it went wrong, refine the delegation and re-run that
slice before moving on.

The worker's own report names its worktree branch (its report contract
requires this). If a report's branch name is missing or unclear,
stop and get the worker to confirm it before integrating — never
assume a garbled report means the slice produced no work worth
merging, and silently skip it. The worker's worktree is cut from whatever base the
harness's own worktree-isolation mechanism uses, not your own tree and
not any fixed ref you can assume — which is exactly why the range
below uses a merge-base, not a fixed-ref, form.

Before integrating a confirmed branch, check
`git rev-list $(git merge-base HEAD <branch>)..<branch>`: an empty
result means the slice genuinely made no commits past the merge-base,
and the cherry-pick below would abort on it
(`fatal: empty commit set passed`) — skip the cherry-pick for that
slice and move on to the next one instead. Otherwise, bring the
branch's diff into your own tree with
`git cherry-pick --no-commit $(git merge-base HEAD <branch>)..<branch>`,
resolving any conflict by hand, then author the commit yourself so
it lands in proper Conventional Commits form — do this before
handing out the next slice. This merge-base is computed against your
own current HEAD each time, so it always accounts for every slice
already integrated so far this run.

Do not run the repo's own `checks-inbox` (or any other store build)
after integrating each slice — one store build per slice is exactly
the redundant, heavy round-trip this design exists to avoid.
`checks-inbox` runs exactly once, in CHECK below, on the
fully-integrated tree, after every slice you're dispatching this pass
has landed.

You still own CHECK, COMMIT, REVIEW, and OUTCOME yourself: the worker only
implements and commits each slice inside its own worktree; the coordinator
integrates each worker's branch, keeps the checks green, reviews, and
finalizes commits. The one-slice, test-first Hard rule below is what each
delegated slice must satisfy — you enforce it; the worker performs each
red-green-refactor cycle.
