# Terminate returns the issue to Dispatchable and never un-lands work

The Console (ADR 0023) lets an operator end a live Dispatch by hand, which
needs a landing place in a lifecycle that is a deliberately closed set —
`Dispatchable → InProgress → Complete | Failed` — mapped by all three Issue
Tracker adapters.

**Decision: Terminate is Dispatch-scoped, returns the issue to
`Dispatchable`, and never destroys pushed work.** It is valid anywhere from
claim to verdict — a running initial Box, a CI watch, a fix pass, the merge
gate — because the Dispatch, not the Box, is the thing the operator reclaims;
a Box-scoped control would leave a Dispatch stuck in a 40-minute CI watch
un-reclaimable. Any running Box is reaped, the settle is abandoned wherever it
stands, and the issue's claim is released back to `Dispatchable` — never
`Failed`, because `Failed` means "needs human triage" and the human just made
the decision; there is nothing to triage. The ending is recorded outside the
state machine — a terminal line in the Box log and a comment on the issue —
matching the precedent that unusual endings get notes, not states
(merge-blocked leaves `Complete` with a note).

Pushed artifacts stay put: no branch deletion, no PR close, no force-push.
Terminate abandons *watching*, never un-lands work. The terminate comment
links any dangling branch/PR so nothing is silently orphaned, and a later
re-dispatch of the issue adopts the abandoned PR through the existing settle
adoption path — making terminate-then-repick a clean reclaim loop rather than
a collision.

Terminate is distinct from Unpick, which retracts a queued Pick that never
launched and touches nothing on the tracker.

## Considered Options

- **Land in `Failed`** — treats operator intent like a crash and pollutes the
  triage queue with issues that need no triage.
- **A new `Killed`/`Aborted` lifecycle state** — maximum audit fidelity, but
  it ripples through all three tracker adapters, the label families, the
  dispatch workflows, and every state-driven query, for what a log line and a
  comment record adequately.
- **Cleaning up pushed work on terminate** (close the PR, delete the branch)
  — destroys real work the operator may want to adopt, and adoption already
  exists; leaving artifacts costs one linked comment.

## Consequences

- A terminated Dispatch can leave an open PR with no active claimant by
  design; the issue comment is the pointer that keeps it discoverable.
- The Console's quit dialog distinguishes drain (default) from terminate-all
  (explicit escalation) — Terminate is the only way a running Dispatch dies
  by hand, including implicitly at quit.
- Lowering the live parallelism cap never terminates; it only gates new
  launches.
