# The stale-base preflight is opt-in, off by default

Amends [ADR 0026](0026-preflight-stale-base-before-merge.md).

## Context

ADR 0026 made the launcher proactively rebase a green-but-behind PR before
merging under `MERGE_MODE=immediate`, treating a stale base (its own
"Consequences" note) as "a near-constant extra rebase + CI-wait cycle" — since
a PR is behind main almost any time something else merged first. That cost was
accepted there to re-test the combined tree and close the `#670`/`#672`
class of cross-PR semantic break (two individually-green PRs that fail when
combined).

In practice the always-on cost dominates the rare benefit. Serialized
immediate-mode landings each pay the extra rebase + full CI wait, which
serializes throughput and blocks running issues in parallel — the very
parallelism the agent fleet exists for. The break it guards against has been
rare and cheap to clean up when it did occur (a single lingering test failure,
fixed forward), whereas the preflight tax is paid on essentially every
back-to-back landing.

Worse, under high parallelism the cost is not merely constant but
*super-linear*. Every landing advances main, leaving the other in-flight PRs
behind; each one that reaches green is rebased and re-runs CI, and may be
behind again by the time that CI finishes — triggering yet another rebase. A
busy fleet can degrade into near-constant rebase + re-CI churn that burns CI
minutes and agent tokens and starves throughput. The durable fix for that
class of problem is a **merge queue** (GitHub's, or an external one), which
serializes and batches the "test against the current tip, then land" step so
each combined tree is built once rather than repeatedly — but this project's
fine-grained PAT cannot administer one (the same token boundary that rules out
branch protection in ADR 0026). Defaulting the preflight *on* would therefore
impose exactly this thrashing on the tool's primary use case, with no
in-project way to escape it short of the knob itself.

The two enforcement points are not equivalent substitutes, so dropping the
preflight entirely would remove a capability some deployments may still want:
a repo that lands many semantically-entangled PRs, or one without the
worker-side pre-push rebase, might prefer to pay the tax. The decision is
therefore a *default*, not a removal.

## Decision

Gate the stale-base preflight behind a dedicated knob, `PREFLIGHT_STALE_BASE`
(flake option `preflightStaleBase`, `settings.selfHealing.preflightStaleBase`),
**off by default**. When off, `preflightStaleBase` returns immediately without
even querying `NeedsUpdate` — no wasted compare-API round-trip — and a
green-but-behind PR merges as-is, relying on its green CI as the landing gate.
When on, ADR 0026's behavior is restored verbatim: a `NeedsUpdate`-true PR is
rebased and re-greened before the merge, drawing on `MAX_REBASE_ATTEMPTS` for
its budget.

The knob is independent of `MAX_REBASE_ATTEMPTS`, which continues to govern the
*reactive* conflict-retry loop (a rebase triggered by a genuine textual merge
conflict on the `Merge` attempt). Turning the preflight off does not disable
conflict-retry, and `MAX_REBASE_ATTEMPTS=0` still disables both — the preflight
checks the flag first, then the budget.

The branch's freshness guarantee moves to where it is cheapest: the implementor
Box already rebases onto the latest base immediately before every push
(`templates/default/prompts/issue-prompt.md`). That keeps a branch's tested
tree current with siblings that landed while it worked, at push time, without a
post-green launcher rebase cycle. This does not cover a sibling that merges
*after* this PR pushed but before it merges — the residual `#670`/`#672`
window — which is exactly the case the opt-in preflight (or GitHub branch
protection / a merge queue, when the token can administer them) still closes.

## Consequences

- The default deployment merges a green PR even when it is behind its base,
  trading the rare cross-PR semantic break for the throughput of parallel
  landings that never wait on an extra rebase + CI cycle. This reverses ADR
  0026's default, not its mechanism.
- `PREFLIGHT_STALE_BASE` (non-empty = on) restores the ADR 0026 behavior for
  deployments that want it. All of ADR 0026's detection machinery
  (`PRForge.NeedsUpdate` via the compare API's `behind_by`, `preflightStaleBase`,
  and their tests) is retained, exercised under the flag.
- The worker-side pre-push rebase is now the primary, cheapest freshness
  mechanism; its prompt wording is strengthened to say so. It is a
  best-effort narrowing of the stale-base window, not a guarantee — the same
  bound ADR 0026 drew for the launcher preflight.
- Enabling the preflight (`PREFLIGHT_STALE_BASE` non-empty) on a
  highly-parallelized fleet without a merge queue in front of the branch
  invites the thrashing described above. The reference docs carry this as an
  explicit warning; a deployment that wants the guarantee at scale should pair
  the knob with a merge queue rather than rely on the launcher preflight alone.
