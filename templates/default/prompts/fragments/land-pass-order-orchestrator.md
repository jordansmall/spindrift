If the seeded "## Run-state handoff" section's `Last reviewer verdict:`
line reads `APPROVE`, this pass is the land pass, and its work follows a
fixed order below rather than the general COMMIT/REVIEW guidance's own
ordering.

1. `git fetch origin`, then `git rebase origin/${BASE_BRANCH}`, resolving
   any conflicts, before applying a single non-blocking fix and before
   running any check. Under immediate-merge landing the base moving while
   the earlier passes ran is the ordinary case, not an exception, and
   conflict resolution rewrites the very tree a gate run would have
   certified — a gate that precedes this rebase is certifying a tree that
   is about to change, so it buys nothing.
2. Apply the non-blocking fixes the triage above keeps, folding each into
   the commit it logically belongs to exactly as the COMMIT section's own
   fold-into-logical-commits mechanism already describes.
3. Run the repo's full check gate once, over the resulting tree.
4. Finish the pass: FILE ISSUES, LAND THE CHANGE, OUTCOME.

Two kinds of edit surface across those steps, and step 2 covers only
one of them. Folds are the reviewer's own listed non-blocking
findings, plus the mechanical churn a rebase conflict or a
formatter/linter run leaves behind. Gate-discovered work is anything
else: a code or test change not traceable back to a reviewer finding,
ordinarily surfaced when step 3's check gate first runs red. The two
are not interchangeable — a fold still folds into the commit it belongs
to as usual, but gate-discovered work is governed by the default below.

The default for gate-discovered work is file, don't fix. When step 3's
failure is proven pre-existing on the base — the REVIEW section's own
clean-checkout proof, not a guess — file it through the FILE ISSUES step
and say so plainly in the outcome note, then leave the change out of the
landing. A tree the reviewer approved is what lands; a pre-existing
failure belongs to whichever branch introduced it, not to this one to
absorb silently.

Fixing gate-discovered work inline is sometimes unavoidable — the gate
will not go green otherwise — but doing so owes a declaration, not a
silent commit. Record it in both the outcome note and this pass's own
`/tmp/decisions.md` — the decisions record the REVIEW section already
has every pass write — naming the files touched, together with a
one-line why. Burying the change in the PR body's own prose does not
satisfy this — a human, and any later delta gate, needs to see
post-review work without diffing the branch.

This ordering supersedes the COMMIT section's "rebase onto the latest base
immediately before finishing" for this pass — the rebase already happened
at step 1, and step 3's gate already covers it.

Fetching again before finishing is still worth doing, but re-run the gate
only when the tree actually changed since it last ran: a later rebase that
really moved the branch, or a fix applied after the gate. A rebase that
reports the branch already up to date changed nothing and earns no second
run. Never re-run the gate for reassurance — over an unchanged tree it is
the single largest wall-clock item in the pass, and it tells you nothing
the first run did not.
