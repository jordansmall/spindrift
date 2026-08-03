# Dispatch priority is a label family the launcher sorts the pool by, above oldest-first

Dispatch order today is strictly oldest-first — each Issue Tracker adapter
returns `ListIssues` already sorted (github by issue number, jira/local by
created time), and the pool-draining (`drainMaxJobs`) and continuous-refill
(`nextReady`) selection consume that slice in order, picking the first unblocked
issues for the free slots. An operator self-hosting spindrift keeps a
continuously-hydrated pool of `MAX_PARALLEL` Boxes, and wants the freed slots to
refill with the work that matters *now* without hand-typing a selective list
every time. This ADR adds **Dispatch priority**: a primary sort key over the
dispatchable pool, demoting oldest-first to the within-tier tiebreaker.

The governing shape is that **priority is a canonical launcher concept resolved
from labels — not a native per-tracker field, and not a per-adapter sort.** Every
Issue Tracker already carries a label mechanism (`doctor` creates the triage and
research families through `CreateLabel`), so priority rides a disjoint label
family that each adapter maps into one canonical `Priority` value on the returned
issue — exactly as dispatch-state labels become a canonical Dispatch lifecycle
state. The launcher then sorts the pool **once, centrally** (in the plan), so the
`(priority, then oldest)` comparison lives in a single place and a new tracker
only has to map its labels. Six decisions define the model:

- **Four tiers, three labels, implicit Normal default.** Critical > High >
  **Normal** > Low, where Normal is the tier an *unlabeled* issue occupies.
  Bracketing the default with both a boost (Critical/High) and a bury (Low) tier
  lets an operator raise urgent work and sink noise while the overwhelming
  majority of issues stay Normal and run oldest-first exactly as before. Highest
  label wins if an issue somehow carries more than one.

- **`agent-priority-*` namespaced labels.** `priority-high` / `priority-low` are
  among the most common project-management labels in any repo, and `doctor` only
  *creates missing* labels — a bare name would silently consume a repo's existing
  roadmap label as a dispatch signal. The `agent-` prefix matches the existing
  spindrift families and guarantees no collision. Defined hardcoded in `doctor`'s
  label map like the research family, and **advisory**: a missing priority label
  never fails the `doctor` check.

- **Scope: the discovered pool + Console Backlog only.** Priority sorts the
  headless discovery pool (queue-drain and continuous refill) and the Console
  Backlog (the pick source). A hand-picked selective list (`dispatch 12 15 18`)
  keeps the operator's typed order — an explicit list is an operator-authored
  trust assertion whose ordering is the operator's (ADR 0011) — and priority is a
  no-op on the single-issue GitHub workflow trigger, which has exactly one issue
  to run.

- **Edges always win; no priority inheritance.** Priority orders the *unblocked,
  ready* set only. A blocker runs before its dependent regardless of tier, and a
  blocker does **not** inherit its dependents' priority. This accepts a
  priority-inversion footgun — a `agent-priority-critical` issue blocked by an
  under-labeled blocker waits while higher-tier unblocked work takes the slots —
  in exchange for not walking the dependency graph to compute an effective
  priority. The operator's remedy is the ADR 0011 ethos: bump the blocker's label
  too.

- **Pure reorder; no aging.** Lower tiers starve under sustained higher-tier
  inflow — that *is* the meaning of a low tier ("run only when the pool would
  otherwise idle"), not a bug. No fairness, aging, or minimum-share dimension, so
  the sort stays a pure, explainable `(priority, then number)` comparison. The
  operator paces starvation by not over-labeling.

- **Advisory; never a dispatch trigger.** The priority family is disjoint from
  the lifecycle families and carries no launch authorization. Applying
  `agent-priority-high` launches nothing — `agent-dispatch.yml` fires only on the
  `agent-trigger` label — it only reorders among issues *already* made
  dispatchable by the launch-button label. So it needs no triage trust gate: a
  mislabel's entire blast radius is queue reordering.

## Considered Options

- **Where priority lives: native per-tracker field / per-adapter label sort /
  canonical-from-labels central sort.** A native field (jira Priority, local
  frontmatter) assumes a mechanism not every tracker has and adds a seam per
  backend. A per-adapter label sort makes each tracker re-implement the same
  `(priority, oldest)` sort, lets the tiebreaker drift between adapters, and
  leaves the launcher without the concept it reasons in. Chosen:
  canonical-from-labels — labels are the one mechanism every tracker already has,
  and a single central sort keeps the tiebreaker single-sourced, mirroring how
  the launcher already turns dispatch-state labels into a canonical lifecycle
  state.

- **Priority inversion: inherit vs. not.** Inheritance (a blocker takes the max
  priority of its transitive dependents) is the textbook fix and would use the
  `DepsOf` graph the wave engine already resolves, but it adds an
  effective-priority computation and the "why did this low issue jump ahead"
  surprise. Chosen: no inheritance — simplest, with the manual-bump remedy
  documented; revisit if inversion bites in practice.

- **Starvation: pure reorder vs. aging.** Aging prevents indefinite starvation
  but adds a time dimension to the sort, an age-threshold knob, and operator
  surprise. Chosen: pure reorder — a self-hosting operator already controls the
  label distribution, and starvation is the intended semantics of a low tier.

- **Naming: bare `priority-*` vs. `agent-priority-*`.** Bare names are shorter and
  match an earlier docs sample but collide with repos that already use
  `priority-high`/`-low`. Chosen: namespaced, to never consume a repo's own
  priority labels as a dispatch signal.

- **Definition: hardcoded advisory vs. configurable names.** The work-tier
  dispatch labels are configurable (`env-schema.nix` + flag table); the research
  labels are hardcoded. Chosen: hardcoded advisory (research precedent) — no new
  config knobs in v1, and a missing priority label never fails `doctor`.

## Consequences

- `doctor`'s label map gains three `agent-priority-*` entries; `doctor` creates
  any missing ones but treats them as advisory (never a check failure), like the
  research family.
- The `IssueTracker`'s returned issue gains a canonical `Priority`; the
  `github`/`forgejo` adapters resolve it from labels, while `jira`/`local` default
  every issue to Normal until they map their own labels. The sort lands once
  centrally in the plan (`NewPlan`/`discover`), so `drainMaxJobs` and `nextReady`
  consume an already-sorted slice unchanged.
- `CONTEXT.md`'s **Dispatch order** entry is amended — `priority` leaves its
  `_Avoid_` list and oldest-first becomes the within-tier tiebreaker — and a new
  **Dispatch priority** entry is added.
- **Priority inversion is a known, documented footgun**: a critical issue blocked
  by an under-labeled blocker waits; the remedy is to bump the blocker's label.
  No inheritance in v1.
- **Low (and Normal) tiers can starve indefinitely** under sustained higher-tier
  inflow; this is intended, not a defect.
- Selective-list dispatch is unchanged (operator order wins), and the
  single-issue workflow trigger is unaffected.
