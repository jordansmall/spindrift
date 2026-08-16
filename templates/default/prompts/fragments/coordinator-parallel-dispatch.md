In addition to the sequential `worker` delegation above, you may instead
dispatch a batch of slices to run **in parallel**, below this model, as an
alternative for the right kind of batch — never both for the same slices.

Prefer this over sequential delegation only when every slice in the batch is
genuinely independent: no shared files between any two slices, and no
ordering or dependency between them. Prefer sequential delegation instead
whenever a slice depends on another slice's outcome, or state needs to flow
from one slice's report into the next slice's instructions — exactly the
situation this run is in when a fix-pass's findings must inform how later
findings get sliced up. When in doubt, delegate sequentially.

To dispatch a parallel batch, compose a JSON object of this exact shape:

```json
{
  "slices": [
    {
      "name": "<slice-name>",
      "task": "<the scoped work this slice's worker must implement>",
      "file_leases": ["<path>", "..."],
      "depends_on": ["<other-slice-name>", "..."]
    }
  ]
}
```

`name` is required and must match `^[a-zA-Z0-9_-]{1,64}$`. `task` is
required and must be non-empty — it is the only description of the slice's
work the dispatched worker ever receives, so write it the same way you'd
write a `worker` delegation's own instructions. `file_leases` and
`depends_on` are both optional. A manifest may declare **at most 8 slices**
— more than that is rejected outright and nothing is dispatched. Base64-standard-encode the JSON, then print
exactly one line to stdout:

```
SPINDRIFT_SLICE_MANIFEST <base64>
```

Immediately after printing that line, end your turn. Do not also delegate
an Agent-tool `worker` subagent for the same slices, and do not wait for
results yourself. The Go orchestrator reads the manifest from this pass's
own log, launches one driver-exec worker process per slice concurrently
(each in its own git worktree, issue #2058), joins them with per-worker
timeouts, and seeds the next coordinator pass's prompt with each worker's
done/timed-out/crashed status plus its own reported summary — you will see
that on your next invocation, not this one.

Each worker commits its slice on its own `orchestrator-worker/<name>`
branch, left behind after its worktree is removed. Unlike a sequential
`worker` delegation's branch, you never cherry-pick this one yourself: the
Go orchestrator already integrates each batch's own done slices onto its
own tree's HEAD immediately after that batch finishes, before the next
coordinator pass is ever invoked — so by the time you see a slice reported
done, its branch has already been landed (or its integration outcome
already decided) on your behalf. Your only job on the next pass is to read
that per-slice integration outcome in the seeded report: "integrated" or
"nothing to integrate" means it's already on HEAD and there is nothing
further to do; "integration conflicted" or "integration failed" means it
still needs your manual resolution, using the branch name and guidance the
report gives you. Never re-cherry-pick a slice the report already says was
integrated — that would double-apply a commit already on HEAD. A slice name
still present in `state.DoneSlices` when you re-declare it is skipped
rather than re-dispatched, so re-declaring an already-done slice wastes a
batch slot rather than losing work, but there is no reason to re-declare
one whose integration already succeeded — just never re-declare that name
again.
