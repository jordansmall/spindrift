---
title: "Fix a red post-force-push head before settling landing-failed"
state: ready-for-agent
---
## Parent

#5053 — its Landing failed this way: a conflict-resolve force-push went red
on `flakemodule-equivalence` and the issue settled `landing-failed` 13s later
with no fix pass.

## What to build

When `immediate` Landing force-pushes a head — after a clean rebase-retry, a
stale-base preflight rebase, or an agent-dispatched conflict-resolve Box — and
the post-force-push CI re-wait ends red, the host dispatches the existing
red-CI fix pass instead of settling `Failed` on the spot. The fix pass draws
from the same fix-attempt budget the pre-merge red-CI loop uses. A head the
fix pass turns green re-enters the merge path exactly as a first-green head
does (rebase-retry and conflict-resolve budgets included).

The issue still ends `Failed` with `landing-failed` when the fix budget is
exhausted, when the re-wait times out, or when the conflict-resolve dispatch
or its outbox relay itself fails. The failure comment says how many fix
attempts the force-pushed head consumed.

Today's rule ("the force-pushed head never went green, so the issue ends
`Failed`") is recorded in CONTEXT.md's Lifecycle entry and the
`errLandingNeverGreen` doc; amend both, and ADR 0026, to the new behaviour.

## Acceptance criteria

- [ ] Red post-force-push re-wait → fix pass dispatched → green → merged and
      `Complete` (host-side settle test).
- [ ] Red post-force-push re-wait with the fix budget already exhausted →
      `Failed`/`landing-failed`, comment names the attempts used.
- [ ] Re-wait timeout and conflict-resolve dispatch/relay failure still settle
      `Failed` without a fix pass.
- [ ] The fix budget is shared with the pre-merge red-CI loop, not a fresh
      allowance per force-push.
- [ ] CONTEXT.md Lifecycle entry, the error's doc comment, and ADR 0026
      (amendment) describe the new rule.

## Blocked by

None — can start immediately.

