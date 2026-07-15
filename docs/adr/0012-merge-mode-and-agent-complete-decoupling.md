# `agent-complete` marks agent-done, not merged; merge is a separate `MERGE_MODE`-gated function

Until now the launcher swapped an issue to `agent-complete` **inside** the
merge-success path (`mergeWhenGreen` swaps the label only after `fc.Merge`
returns nil), and immediately force-merged every green PR with `gh pr merge
--rebase`. That conflates two distinct facts — *the agent has nothing left to
do* and *the PR merged* — and it breaks on any Target repo whose branch
protection requires human approval: the check **rollup** (`statusCheckRollup`,
what `CheckState` reads) reports `SUCCESS` from CI alone, so spindrift sees
green, fires the merge, and GitHub rejects it for the unmet approval. That
rejection is a non-conflict merge failure → `(false, false)` → non-retriable →
the issue is swapped to `agent-failed`. A green PR that did exactly what was
asked lands in the failure bucket because a human hadn't clicked approve.

This ADR **decouples the label from the merge** and makes the merge a separate,
policy-gated function.

`agent-complete` is redefined to mean **"the agent has nothing left to do"** — a
PR exists, its pipeline is green, and the landing path has settled — and is
swapped when the landing path settles, in **every** mode, independent of any
merge attempt. (Amended by issue #757: `immediate` mode can still do real
agent work after first green — rebase-retry, an agent-dispatched
conflict-resolve box, a post-force-push CI re-wait — so the swap is held
until that work finishes, not fired at first green while it demonstrably
still has work left. `manual`/`auto`/push-only landings have no post-green
agent work, so the swap point is unchanged for them.) Merge becomes a
downstream step gated by a new three-valued **`MERGE_MODE`** knob:

- **`immediate`** — merge on green (`gh pr merge --rebase --delete-branch`), the
  prior behavior. spindrift's own `harness.env` sets this to preserve live
  dogfood auto-merge.
- **`manual`** — stop at green and hand off; a human approves *and* merges.
- **`auto`** — enqueue GitHub-native auto-merge (`gh pr merge --auto`), so GitHub
  merges the instant the last branch-protection requirement (the approval)
  clears. The human's only manual step is the approval — the irreducible one.

The merge gate splits into **`gateToGreen`** (poll CI, self-heal red as today,
report green with no label swap) and **`applyMergeMode`** (the mode-specific
action); the caller swaps `agent-complete` once `applyMergeMode` returns —
merged, auto-merge enqueued, manual hand-off, or merge-blocked-with-note all
count as settled. A merge that fails *after* green — an unmet approval
in `immediate`, an unresolvable conflict, a disallowed `--auto` — leaves the
issue `agent-complete` with a merge-blocked note and **never** demotes it to
`agent-failed`. `agent-failed` is reserved strictly for **"never produced a green
PR"**: a box crash, or CI genuinely red past `MAX_FIX_ATTEMPTS`. The existing
rebase-retry and agent conflict-resolve behaviors still run inside
`applyMergeMode` for `immediate`; they simply no longer nuke a green PR into
`agent-failed` when unresolvable.

The shipped default is **`manual`** — the generic-safe choice for the open-source
ecosystem, where most repos gate merges on some approval, so an `immediate`
default would fail on first use. This is a behavior change from the prior
always-merge path, but there is no external Consumer yet, and spindrift's own
repo opts back into `immediate` via its `harness.env`; the Jordan-specific value
lives only in this repo's config, not in the shipped default. `validate()`
rejects any `MERGE_MODE` other than `immediate`/`auto`/`manual` at startup (a
typo'd value must not silently auto-merge or silently hand off), and `dispatch`/
`preview` print the effective mode in the startup banner so the operator sees
which behavior is armed before boxes run.

No `agent-awaiting-review` label is introduced. A green-but-unmerged PR is fully
described by `agent-complete` (the agent is done) plus the issue being **open**
(not yet merged); when the merge lands — by human, by GitHub `--auto`, or
immediately — the PR closes the issue, and open-vs-closed *is* the merged
signal. Adoption (automatic, at the time this ADR was written; explicit-only
via `spindrift recover <n>` since #600) keys only on `agent-in-progress`, so a
handed-off `agent-complete` issue is left alone on later runs regardless.

## The dependency-satisfaction fix this forces (ADR is authoritative)

Because `agent-complete` no longer implies "merged into base," **`blockerReady`
must stop keying on the label** — otherwise a dependent would treat a
green-but-unmerged blocker as satisfied and branch off a base missing the
blocker's code. `blockerReady` is rebound to the ground truth: a blocker is
satisfied when its **PR is MERGED** (authoritative), with issue-**CLOSED**
retained as a fallback for work absorbed or handled outside spindrift. The
`containsLabel(completeLabel)` short-circuit — always a soft, removable,
now-inaccurate proxy — is deleted. This ships as a **prefactor** ahead of the
`MERGE_MODE` change (issue #284), because it is the correctness precondition for
the decoupling, and it independently fixes two latent bugs: a human removing the
label off a genuinely-merged blocker no longer strands its dependents, and a
human-handled blocker (merged with no spindrift label ever applied) is now
correctly recognized.

## Considered Options

- **A boolean `AUTO_MERGE` (merge / don't-merge)** — smaller surface, but it
  cannot express the middle ground (`auto`) that the approval-gated case most
  wants, where the human only approves and GitHub merges. Rejected for the
  three-valued `MERGE_MODE`, which maps 1:1 onto the three `gh pr merge`
  invocations (`--rebase` / `--auto --rebase` / none).
- **Default `immediate` (preserve prior behavior)** — non-breaking and keeps the
  autonomous-merge identity out of the box, but it fails on first use for the
  majority of Consumer repos that require approval, and it bakes a
  Jordan-specific assumption into the shipped default. Rejected; the knob is
  right there for the autonomy case (spindrift's own repo sets it), and a
  green-safe default matters more for a stranger's first run.
- **A dedicated `agent-awaiting-review` terminal label for the handoff state** —
  an earlier draft of this design. Rejected: it names a state (`agent-complete`
  + issue-open) that open/closed already distinguishes, adds a contract/glossary
  term, and — most tellingly — was only ever needed to keep `blockerReady`
  correct, which the `blockerReady` rebind solves at the root. Fixing the binding
  deletes the reason for the label.
- **Reuse `agent-complete` *and* keep `blockerReady` keyed on it** — the
  minimal-vocabulary option, but it makes "blocker satisfied" fire before the
  blocker's code is in base (a green-but-unmerged blocker carries the label),
  launching dependents against a stale base. Rejected as a correctness bug; the
  label is a proxy, base-branch content is the truth.
- **Merge-failure keeps demoting to `agent-failed`** — preserves today's single
  terminal-failure bucket, but it is exactly the reported defect: it buries a
  green PR under `agent-failed` because a human hasn't approved. Rejected;
  `agent-failed` now means only "no green PR was produced."

## Consequences

- The merge gate is restructured into `gateToGreen` (reports green, no label
  swap) + `applyMergeMode` (mode-specific), replacing `mergeWhenGreen`'s
  `(merged, genuineRed)` tuple's overloaded `(false, false)`-means-failed
  contract. The caller swaps `agent-complete` once `applyMergeMode` returns,
  i.e. once the landing path settles (issue #757). `MERGE_MODE` is honored at
  this single choke point, so `dispatch`, single-issue dispatch, and `recover`
  (ADR 0011) all respect it identically.
- `MERGE_MODE` is added to `lib/env-schema.nix` (enum, default `manual`),
  regenerating the flag table; it joins the versioned CLI/flake contract
  (ADR 0010). `validate()` gains an enum check; the `dispatch`/`preview` banner
  gains an effective-mode line.
- `blockerReady` is rebound to PR-merged (authoritative) + issue-closed
  (fallback), and the `completeLabel` check is removed — landed as a prefactor
  (#284) before the `MERGE_MODE` slice (#285); `auto` follows (#286).
- CONTEXT.md's Label lifecycle is updated: `agent-complete` reads as "the agent
  is done — a green PR exists," with merge described as a separate
  `MERGE_MODE`-gated function; `agent-failed` reads as "no green PR was
  produced." (The `fanout-blocker` entry was subsequently retired in #329.)
- spindrift's own `harness.env` sets `MERGE_MODE=immediate` so live dogfooding
  keeps auto-merging; the shipped env-schema default stays `manual`.
- Deferred: a per-issue merge-mode override (e.g. a `hold-merge` label to force
  hand-off on one issue inside an `immediate` repo) is additive on top of the
  global knob and out of scope here; and relabeling a GitHub-`auto`-merged issue
  from `agent-complete` is unnecessary — the merge closes the issue, and no logic
  keys on a post-merge label swap.
