# The Dispatch lifecycle stays call-site transitions; no lifecycle module

Issue #139 deferred a `cmd/launcher/internal/lifecycle` module (a transition
table owning the Dispatch lifecycle's valid edges) pending a deletion-test
re-run once self-heal (#136) and the retry work had landed. Re-run 2026-07-10:
the full transition set is four typed edges across seven call sites —
`Dispatchable→InProgress` (claim), `InProgress→Complete` (green CI, push-only
landing), `InProgress→Failed` (self-heal cap, tripwire, box failure),
`Dispatchable→Failed` (failed blocker) — and the growth #139 anticipated never
widened it: transient retry loops without a state change, and every call site
names its typed `from`/`to` inline, so no site can even express an invalid
edge. A module would be a four-row table rejecting transitions nobody can
attempt; deleting it (hypothetically) scatters nothing. The settle module
additionally concentrates all terminal-transition sites in one place, so the
locality a lifecycle module promised arrives without one.

Issue #757 (2026-07-15) moved when `InProgress→Complete` fires — from the
instant CI first confirms green to once the landing path settles — spreading
that edge from one call site (`gateToGreen`) to the four outcomes `selfHeal`'s
green branch can settle on (merge-guard-check-error, merge-guard-hit,
merge-blocked, landed), plus the unchanged push-only-landing site: five call
sites, ten overall. Still the same edge, still every call site naming its
typed `from`/`to` inline — no new edge, no conditional edge, no table.

Issue #758 (2026-07-15) added a new reason for the existing `InProgress→Failed`
edge inside `selfHeal`'s green branch: a force-pushed head (a conflict-resolve
dispatch failure, or a post-force-push re-wait that ends red with no fix
budget left — amended by issue #5095 — or times out) never re-confirms green,
so there is no green PR at the current head — the same "never produced a
green PR" condition `InProgress→Failed` already covers for a box crash or
exhausted fix passes, just discovered later, mid-landing.
Still the same edge, still named inline at its call site — no new edge.

Re-open trigger: a transition added outside the claim or settle paths, or a
conditional edge — one whose legality depends on runtime state rather than
the static table above. Until then, architecture reviews should not
re-suggest this module.

Closes #139.
