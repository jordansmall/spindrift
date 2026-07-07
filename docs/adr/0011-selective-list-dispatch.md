# Selective list dispatch: a hand-picked issue set overrides the label, honors real edges, and pierces the barrier

`dispatch` today has exactly two selection modes: drain the whole
`ready-for-agent` queue, or run a single issue pinned by `ISSUE_NUMBER`. Neither
fits the operator who, mid-session, wants to run *a few specific* issues — the
common case while self-hosting spindrift, where a failure spawns two or three
new issues and the operator wants to run *those* (to dogfood a fix) without
dispatching the entire backlog and without waiting through them one at a time.
This ADR adds a third mode: a **hand-picked list**, `dispatch 12 15 18`, and
defines what that explicit selection means against the three gates that normally
filter the queue — the `ready-for-agent` label, `blocked by #N` dependency
edges, and the `fanout-blocker` barrier.

The governing principle is that **an explicit issue list is an operator-authored
trust assertion**. When a human types issue numbers into a local CLI they have
already made the authorization decision that the machinery otherwise infers from
labels and pacing gates. So the explicit list overrides the *policy* gates that
exist to constrain *automation*, while still honoring the *real dependencies*
that encode "this work needs that work." The three gates split cleanly along
that line:

- **`ready-for-agent` label — overridden, with a guard.** The label is the trust
  boundary for the *automated*, GitHub-triggered dispatch path (only a triage
  holder can apply it). A human hand-picking issues locally is a different trust
  context, so the explicit-list form dispatches its members whether or not they
  carry the label; bare `dispatch` (queue drain) still requires it. Because this
  is a deliberate override, any listed issue *missing* the label prints a visible
  `⚠ #15 not ready-for-agent; dispatching anyway (explicit)` line and triggers
  **one batched secondary confirmation** (`Dispatch N unlabeled issue(s)? [y/N]`)
  before any Box launches. Non-interactive callers pass `--yes` (alias
  `--force`) or the run aborts non-zero rather than hanging. When every listed
  issue is already labeled, there is no override and no prompt.

- **`blocked by #N` dependency edges — honored (never overridden).** These encode
  real work order, so an explicit list respects them exactly as the queue does.
  A blocker that is *also in the list* satisfies the edge and is auto-ordered
  ahead (`dispatch 15 99` with #15 blocked by #99 runs #99, then #15 — the
  existing wave logic). A blocker that is *not in the list and unmerged* leaves
  the edge unmet, so the dependent is **evicted** from the run with a notice
  (`⚠ #15 blocked by #99 (not in list, unmerged); skipping`); eviction cascades
  to anything that depended on the evicted issue. This is the inverse of the
  motivating scenario read correctly: if #15 needs #99, the operator wants #99
  to run *first*, not to skip past it — so the right move is to include #99, and
  the tooling orders it ahead. A blocker already merged/closed is satisfied and
  the dependent stays. Mechanically this only *relaxes* the launcher's existing
  refuse-if-blocker-unmet rule: an in-list blocker now counts as met.

- **`fanout-blocker` barrier — pierced.** The barrier is a coarse *planning*
  gate, not a real dependency: it fences issues numbered above the lowest open
  `fanout-blocker` so *automatic* fan-out does not race ahead of a foundational
  change. It is specifically a spindrift self-hosting rebuild signal and has no
  meaning for a downstream Target repo. Single-issue `ISSUE_NUMBER` dispatch
  already skips it (`filterByBarrier` is bypassed when the number is pinned), so
  the explicit list — which generalizes single-issue dispatch — pierces it too,
  for the same reason the label is overridden: the operator has already made the
  sequencing call.

`preview` mirrors the new surface so an operator can *see* before they *run*. It
lists the `ready-for-agent` queue as **one flat list with inline blocker
annotations** (`#15  Add opencode adapter  (blocked by #99)`) rather than
partitioning into runnable/blocked — a partition would mislabel #15 as
un-runnable when `dispatch 99 15` runs it fine. Given the same positionals
(`preview 99 15`), it performs a **dry run** of that exact list, printing the
auto-ordering, any cascade evictions, and any label-bypass warnings, without
launching a Box or prompting. That makes `preview` the safe rehearsal for the
override path.

## Considered Options

- **Label: require vs. override.** Requiring the label everywhere keeps one
  uniform trust rule, but it defeats the feature's purpose — the operator's
  motivating case is running freshly-filed, not-yet-triaged issues. Overriding
  with a visible warning and a secondary confirmation preserves the automated
  path's trust model (bare `dispatch` still requires the label) while giving the
  human a deliberate, audited escape hatch. Chosen: override-with-guard.
- **Blocker edges: full override (A) / internal-only (B) / respect-all with
  eviction (C).** (A) run exactly what's typed in parallel, ignoring the graph —
  simplest, but dispatches dependents before their dependencies and wastes runs.
  (B) order in-list pairs but silently override external unmet blockers — "do
  what I mean," but it can launch a dependent whose blocker the operator merely
  forgot. (C) honor every edge: order in-list blockers ahead, evict dependents
  whose blockers are absent. Chosen: (C) — dependencies are *real*, cheap to
  respect via existing wave logic, and eviction surfaces the missing blocker so
  the operator adds it rather than getting a broken run.
- **Barrier: pierce vs. respect.** Respecting it is more consistent with the
  strict-blocker choice, but it contradicts today's single-issue behavior and
  would silently swallow the very issues the operator named. Chosen: pierce —
  the barrier paces automation, not a human's explicit selection, and it is a
  self-hosting artifact with no portable meaning.
- **CLI syntax: variadic positional vs. `--issues` flag vs. both.** A flag is
  unambiguous against future positionals but diverges from the `dispatch [issue]`
  grammar already set (ADR 0010) and needs comma-parsing. Chosen: variadic
  positional (`dispatch [issue...]`) — a pure widening of the existing grammar,
  idiomatic (`git`/`rm`/`kill`), and it keeps `dispatch` one coherent verb:
  "dispatch *these*, or *the queue* if I name none."
- **`preview`: partition vs. flat annotated list.** A runnable/blocked partition
  reads naturally but is actively wrong once blocked issues are dispatchable via
  their in-list blocker. Chosen: a flat list annotating each issue's blockers, so
  the operator sees the dependency landscape and builds the list themselves.

## Consequences

- `dispatch` grows: variadic positional parsing; a confirmation gate (TTY
  detection + `--yes`/`--force`); and a relaxed blocker-admission rule where an
  in-list blocker satisfies an otherwise-unmet edge. The existing single-issue
  (`ISSUE_NUMBER`) and queue-drain paths are unchanged.
- `preview` grows blocker annotations and a positional dry-run that reuses the
  same ordering/eviction/label-check logic as `dispatch` without side effects.
- The trust-boundary note in `CLAUDE.md` gains a caveat: explicit-list *local*
  dispatch is an operator override of the label, distinct from — and not a
  weakening of — the label-gated *automated* dispatch path, whose boundary is
  unchanged.
- This ADR corrects a factual error in ADR 0010, which described the `dispatch`
  positional as absorbing "the hidden `engage`." Single-issue dispatch is the
  `ISSUE_NUMBER` path; `engage` is the unrelated merge-gate/adopt verb (resolve
  an issue's open PR and drive it through CI-watch-and-merge) and remains a
  distinct verb. It is **renamed `recover`** — "engage" named nothing, whereas
  the verb's actual job is to *recover* an already-open PR into the merge gate.
  The rename ships with the app-style deprecation idiom ADR 0010 established:
  `engage` survives one release as a warn-then-exec alias for `recover`. ADR 0010
  is amended accordingly.
