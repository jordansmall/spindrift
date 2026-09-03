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
