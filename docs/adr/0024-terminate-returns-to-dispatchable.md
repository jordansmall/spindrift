# Terminate returns the issue to Dispatchable and never un-lands work

Three triggers end a claimed Dispatch by hand, all sharing one implementation,
`terminate.Reclaim`: the Console's Terminate gesture ([ADR
0023](0023-console-is-a-picks-only-driving-loop.md)), a second SIGTERM/SIGINT
that, during the launcher's stage-two signalled abort (issue #3521), reaps
every in-flight Box, less those the tracker already shows settled, and the
shutdown gate declining to launch an issue the launcher claimed but never
launched once it has observed a stop or abort (issue #3522). Ending a
Dispatch this way needs a landing place in a lifecycle that is a deliberately
closed set — `Dispatchable → InProgress → Complete | Failed` — mapped by all
three Issue Tracker adapters.

**Decision: Terminate is Dispatch-scoped, returns the issue to
`Dispatchable`, and never destroys pushed work.** It is valid anywhere from
claim to verdict — a running initial Box, a CI watch, a fix pass, the merge
gate — because the Dispatch, not the Box, is the thing the operator reclaims;
a Box-scoped control would leave a Dispatch stuck in a 40-minute CI watch
un-reclaimable. Any running Box is reaped, the settle is abandoned wherever it
stands, and the issue's claim is released back to `Dispatchable` — never
`Failed`, because `Failed` means "needs human triage" and the human just made
the decision; there is nothing to triage. The ending is recorded outside the
state machine — a terminal line in the Box log and a comment on the issue,
save for the drain decline, which declines before any Box exists and so
leaves only the comment — matching the precedent that unusual endings get
notes, not states (merge-blocked leaves `Complete` with a note).

Pushed artifacts stay put: no branch deletion, no PR close, no force-push.
Terminate abandons *watching*, never un-lands work. The terminate comment
links any dangling branch/PR so nothing is silently orphaned, and a later
re-dispatch of the issue adopts the abandoned PR through the existing settle
adoption path — making terminate-then-repick a clean reclaim loop rather than
a collision.

Terminate is distinct from Unpick along the axis of the claim, not the Box:
Unpick retracts a queued Pick before any claim is taken, so there is nothing
on the tracker to hand back, while every Terminate trigger fires after the
claim — including the drain decline, which never launches a Box either.

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
  (explicit escalation), and for the picks a Console session drives itself,
  that escalation is still the only way a running Dispatch dies by hand —
  they run on `RunContinuous` with a zero `waves.Config`, so its stop and
  abort latches are there but short-circuit on the nil channels. The
  stage-two signalled abort is the counterpart on every other Box-launching
  path, reaping the in-flight Dispatches that path's own latch holds —
  `shutdown.Gate`, or `RunContinuous`'s hand-rolled equivalent.
- Lowering the live parallelism cap never terminates; it only gates new
  launches.
