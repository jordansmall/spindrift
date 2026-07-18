# Local issues get a native open/closed axis, closed by a reconcile sweep

## Context

The `local` Issue Tracker (ADR 0013) exists so a solo operator can drive agents
from private, git-ignored Markdown issue files without a real tracker. But it
only ever reported issues as `OPEN` — "Local issues have no closed/open concept
of their own" — so nothing ever closed a local issue once its work landed. On
`github`, issue closure is incidental: GitHub auto-closes from a `Closes #N` in
the merged PR body. `local` has no such forge magic, and we deliberately do
**not** want a `Closes #N` reference by default (see the reference toggle below),
so closure has to be driven explicitly.

Authoring is out of scope: the operator produces ticket files themselves (a
`/to-tickets`-style skill, or by hand). Spindrift owns only the lifecycle. The
frontmatter format is the contract between whatever authors the files and the
lifecycle machinery — there is no create/management CLI.

## Decision

**A local issue gains a native open/closed axis, orthogonal to the canonical
Dispatch lifecycle**, exactly as a GitHub issue is open/closed independent of its
dispatch labels. The four canonical states (`Dispatchable`, `InProgress`,
`Complete`, `Failed`) stay unchanged and launcher-facing; open/closed is a
second, `local`-only axis stored as a boolean `closed:` frontmatter field
(absent/false = open), coexisting with the existing `state:` dispatch marker.
Closed issues stay in the folder, excluded from `ListOpenIssues`/`ListIssues`.

**Closing is driven by a new observational sweep, `reconcile`, which is the sole
closing authority.** Reconcile is `local`-tracker-specific, auto-invoked at the
end of a `dispatch` run and available standalone (`spindrift reconcile`) for the
between-runs cases — a runner that died, or a PR sitting in approval limbo. It
**never lands code**. Per open local issue it:

- **closes** when the recorded `landing` PR is merged;
- **discovers** a PR by agent branch (reusing `ResolveOpenPR`/adopt discovery)
  when no `landing` was recorded — the box died before its outcome line was
  parsed — attaches the landing, then closes if merged;
- **flags abandoned** when the PR was closed unmerged;
- **gated-resets** an orphaned `InProgress → Dispatchable` (see below).

An open PR — green-and-mergeable or in approval limbo — is left untouched;
merging stays with `dispatch` (immediate mode) and the explicit, #600-gated
`recover` gesture. A later sweep closes the issue once the PR merges elsewhere.

**The landing ref is recorded via an optional `IssueTracker` method**, the
PRForge optional-interface pattern (ADR 0013 amendment): only `local` implements
it; `settle` type-asserts and calls it after parsing the outcome line, writing a
`landing:` frontmatter field. Only the immutable ref is stored — merge-state is
never cached; it stays the Code Forge's live truth, re-checked each reconcile.

## Gated auto-reset qualifies #600

#600 established that a bare `InProgress` issue is never auto-adopted or reset,
because durable state "carries no liveness signal, so it cannot be told apart
from an issue a live runner is actively committing to" — the only reset path is
the explicit operator gesture `spindrift recover <n>`. Reconcile's orphan-reset
does not overturn that; it **supplies the missing liveness signal**. It resets
`InProgress → Dispatchable` only behind a strong composite death signal: no
merged/open PR or agent branch, **and** `logs/issue-<num>.log` stale beyond a
threshold, **and** — when the container runtime is reachable on-host — the box
container absent. #600's worry was overlapping runners on a shared GitHub repo;
a solo `local` operator on one host can often authoritatively ask the runtime
whether the box still lives, a stronger signal the general case lacks. Absent
that composite evidence, an open PR or a live/recent log leaves the issue
`InProgress` untouched.

## The PR-reference toggle avoids an auto-close footgun

By default a PR does **not** reference the local issue: the issue lives in a
private, git-ignored folder, and a reference would leak private content and
point at a slug meaningless to the shared remote. A global setting (grouped
settings surface, ADR 0015), default off, turns references on. Even when on, the
reference is a **non-auto-closing** breadcrumb (`Local-issue: <slug>`), never a
`Closes #N`/`Fixes #N` keyword: a local slug can be numeric (`.../42.md` → slug
`42`), and `Closes #42` in a PR body would silently close real GitHub issue #42
on the Target repo.

## Considered Options

- **A new canonical `Closed` lifecycle state** across all trackers — rejected:
  forces every adapter and the whole settle path to grow a state for something
  only `local` needs, the same objection ADR 0022 raised against new lifecycle
  states for one feature.
- **Reuse `Complete` as closed** — rejected: `Complete` in manual/auto merge
  mode means "PR handed off to a human," not "merged," erasing the very
  distinction (agent-finished vs. PR-landed) reconcile exists to track.
- **Inline close in the merge path + a separate reconcile sweep** — rejected in
  favor of reconcile as the *sole* authority: one idempotent code path that
  covers immediate-merge, approval-limbo, and dead-runner uniformly, with no
  second close path to drift.
- **Reconcile also merges mergeable PRs** (reusing recover's adopt-and-gate) —
  rejected: it would land code without the explicit per-issue gesture #600
  requires and could merge a holding-pattern PR before its human approval.
- **Cache merge-state in frontmatter** — rejected: duplicates the forge's
  authoritative truth and risks staleness; reconcile re-checks live.

## Consequences

- `reconcile` is an additive subcommand (MINOR under ADR 0010); `dispatch` gains
  a final `local`-only reconcile step, and `Reconcile` enters the glossary.
- The `IssueTracker` seam grows one optional method for recording the landing;
  `github`/`jira` do not implement it, discovered by type assertion like
  `PRForge`.
- Reconcile is observational and cheap (PRForge checks on open issues carrying a
  landing), so auto-invoking it per dispatch run and cronning the standalone
  verb are both safe.
