A `worker` subagent is provisioned this run, so run IMPLEMENT as its
**coordinator** rather than editing the source yourself. Use the scout brief
to break the issue into an ordered set of small slices, then
delegate each slice **sequentially** to the `worker`:

> worker: implement <one slice, with the brief's relevant pointers>. Work
> test-first. Return a concise report — files touched, checks run, outcome —
> not the diffs.

Hand the worker one slice at a time and wait for its report before starting
the next. Keep only that summary in your own context — let the bulk diffs,
file reads, and check logs live in the worker's context, not yours. If a
slice's report shows it went wrong, refine the delegation and re-run that
slice before moving on.

You still own CHECK, COMMIT, REVIEW, and OUTCOME yourself: the worker only
implements each slice; the coordinator keeps the checks green, reviews, and
commits.
