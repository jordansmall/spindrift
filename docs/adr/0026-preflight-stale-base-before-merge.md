# Preflight a stale base before merging, instead of gating on branch settings

## Context

#670 and #672 merged roughly 90 seconds apart. `f8d9e9b` (#670) deleted the
`schemaDefaults` symbol; `a463411` (#672) added tests still referencing it.
Each PR was green against its own base branch at the moment its CI ran, and
neither PR's content conflicted with the other's — they touched different
files, so the existing declared `## Touches` overlap gate (which defers
concurrently-*dispatched* issues whose declared touch-sets intersect) never
saw a collision to defer. `launcher-go-vet` only failed once both landed on
`main`, because no check ever compiled the two changes together before that
point.

The root cause is not a missing textual-conflict check — GitHub already
refuses to merge a PR whose content conflicts with its base (`Mergeable` /
`ErrMergeConflict`, already handled by the existing rebase-retry path). The
gap is that a PR can be `MERGEABLE` (no textual conflict) while still
**behind** its base — its tested tree predates a just-merged sibling — and
the launcher's merge gate only asks "is CI green on this commit," never "is
this commit still built from the current base tip." GitHub computes exactly
that fact independent of branch protection, as `mergeStateStatus: BEHIND` on
the PR.

Three enforcement points could close this gap:

1. **GitHub branch protection — "require branches to be up to date before
   merging"** (or a merge queue). This is the standard fix and would have
   caught the incident directly. But `gh api
   repos/{owner}/{repo}/branches/main/protection` 403s under this project's
   fine-grained, single-repo PAT (`Resource not accessible by personal
   access token`) — branch protection administration needs a scope this
   token deliberately does not carry (see [Before you deploy](../../README.md#before-you-deploy)
   and the `workflow`-scope boundary in `CLAUDE.md`). Not implementable by an
   agent working this issue; left as an operator action.
2. **The declared `## Touches` overlap gate.** Already implemented
   (`internal/waves/touches.go`, `internal/forge/touches.go`), but it is a
   *dispatch-time* file-level check between concurrently-running issues. It
   would not have caught #670/#672: they touched different files, and the
   break was a semantic (symbol-level) dependency, not a file overlap.
   Extending it to whole-tree semantic analysis is out of scope for a
   tracer-bullet slice.
3. **A launcher-side preflight before merging** that treats `BEHIND` as
   equivalent to a conflict requiring rebase-and-re-green, before the merge
   itself. This needs no elevated token scope — it reads a PR's
   `mergeStateStatus` (a normal GraphQL read, same auth as every other
   `PRForge` query) and reuses the rebase/re-wait machinery
   `MAX_REBASE_ATTEMPTS` already drives for genuine conflicts.

## Decision

Implement option 3: `Settle.mergeImmediate` now calls a `preflightStaleBase`
step before its first `Merge` attempt. If `PRForge.NeedsUpdate` reports the
PR's head is `BEHIND` its base, the launcher proactively rebases the branch
and re-runs the merge gate's CI wait — forcing the combined tree (this PR's
changes replayed onto the sibling's already-merged changes) through CI
before the merge can complete. A CI failure on that rebased tree now demotes
the same way a genuine red gate result always has; a clean rebase proceeds
to merge as before.

This reuses `MAX_REBASE_ATTEMPTS` rather than introducing a new knob: `0`
already means "no rebase behavior at all," so the semantics compose with the
existing conflict-triggered rebase path instead of adding a second toggle
with its own off-switch.

Explicitly **not** chosen: downgrading a stale-but-green PR to manual (the
merge-guard's pattern, ADR 0016). Under `MERGE_MODE=immediate`, a PR is
almost always behind main by the time it's ready to land — anything merging
before it advances the tip. A guard that downgrades on every `BEHIND` hit
would make immediate mode manual in practice for any repo landing more than
one PR in a row, trading a rare cross-PR semantic break for constant human
toil. Proactively rebasing and re-testing fixes the actual problem (an
untested combined tree) instead of just relocating it to a human.

## Consequences

- A green PR that is merely behind main now costs one extra rebase +
  CI-wait cycle before merging under `MERGE_MODE=immediate` — the same cost
  a genuine conflict already paid, just triggered earlier and by a weaker
  signal (ancestry, not content).
- This is a **launcher-side sanity check, not an adversary-proof gate** —
  the same bound ADR 0016 draws for the merge guard. A concurrent push
  between the preflight check and the merge can still race; that residual is
  accepted rather than chased, since closing it fully needs the branch
  protection or merge-queue configuration this token cannot administer.
- `mergeStateStatus` is GitHub-specific; the push-only `git` Code Forge (no
  `PRForge`) has no equivalent concept and `preflightStaleBase` is a no-op
  there, matching every other PR-only guard in this package.
- The `#670`/`#672` collision itself is reproduced as a test
  (`TestMergeImmediate_StaleBaseCombinedBreakBlocksMerge` in
  `internal/settle/merge_test.go`): a `NeedsUpdate`-true PR whose rebased
  tree then fails CI is blocked from merging, rather than landing on the
  strength of its stale green result.
