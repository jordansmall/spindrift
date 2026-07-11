# Issue Tracker and Code Forge are independent seams

## Context

The original `forge.Client` interface (`cmd/launcher/internal/forge`) conflated
two concerns under one "Forge" seam: supplying and tracking work (issues, labels,
comments) and landing code (PRs, CI rollup, merge). That was fine while GitHub was
the only backend, but to be adopted beyond GitHub-issues+GitHub-PRs teams — Jira
shops, and solo developers who want to drive agents from private local issues
without publishing to any tracker — the two concerns must vary independently.

## Decision

Split the seam into two independent axes:

- **Issue Tracker** — `github`, `jira`, `local`. Lists dispatchable issues, reads
  an issue, transitions its Dispatch state, posts comments.
- **Code Forge** — `github` (PR + CI-watch + merge) or `git` (push-only: clone
  from a plain git remote URL, commit a per-issue branch, push it, and stop; no
  PR/CI/merge). A git remote to push to is a hard requirement — see Consequences.

The launcher reasons in **canonical Dispatch states** (`Dispatchable`,
`InProgress`, `Complete`, `Failed`); each Issue Tracker adapter maps those to its
native mechanism (GitHub labels, Jira workflow statuses with label fallback, local
file frontmatter). The two axes are **freely combinable and permissive** — every
`ISSUE_TRACKER × CODE_FORGE` cell is allowed; the harness does not reject
"incoherent" pairings.

## Considered Options

- **Keep one `Forge` interface, stub the unused half per adapter** (e.g. a Jira
  adapter whose `Merge` returns "unsupported"). Cheaper immediately, but it lies
  about capabilities and the dispatch logic can't reason about whether a PR
  concept even exists for a given deployment.
- **Curated presets** (a fixed enum of blessed combos). Simpler to reason about,
  but scales multiplicatively as backends grow on either axis, and someone always
  wants the cell that wasn't blessed. Rejected in favor of two independent knobs.

## Consequences

- **A git remote is a hard requirement on the Code Forge axis.** The fully-local,
  no-remote path (mount the operator's working copy, have the trusted launcher
  land the branch back) was considered and cut: it would have punctured the Box
  isolation boundary or required net-new machinery (a copy-out channel + host-side
  git). Keeping "the Box clones from and pushes to a remote" unchanged preserves
  the isolation model that makes `--dangerously-skip-permissions` safe.
- Because a remote always exists, `MERGE_MODE` generalizes purely as remote
  pushes: `manual` (default) pushes the feature branch; `immediate` pushes to the
  target branch; `auto` is native GitHub auto-merge and has no meaning off
  `github`.
- Solo/private use is served not by a local *code* path but by pairing
  `CODE_FORGE=git` with `ISSUE_TRACKER=local`: issues stay private (git-ignored
  `.spindrift/issues/`, Markdown + YAML frontmatter) while code still goes to a
  real remote. This makes "drive agents from private breakout issues without
  polluting the shared tracker" fall out for free.
- The `local` tracker ships standalone; any linkage/sync to an upstream Jira
  parent is a deferred, opt-in enhancement, not part of the seam.
- Dispatch order is adapter-provided (GitHub issue number, Jira created-time,
  local `created` frontmatter); the launcher never compares IDs numerically. The
  `fanout-blocker` barrier was retired as legacy dogfooding scaffolding and is
  not carried into the new backends.

## Amendment (issue #517): narrow Code Forge, optional PRForge, retire Client

The original Code Forge interface still carried 13 methods, and the push-only
`git` adapter stubbed six of them (PR lookup/open, PR state, check state,
failure detail, PR files) plus the auto-merge pair, gated by a `PushOnly()`
capability flag. That flag let capability knowledge leak across the seam
into `internal/settle`, which had to ask "am I push-only?" before touching
any PR/CI method — the exact "lying about capabilities" failure mode this
ADR's Considered Options already rejected for a single stubbed interface.

The fix narrows `CodeForge` to the four methods every adapter honors with
real behavior — `AgentBranch`, `Merge`, `Rebase`, `Probe` — and moves the
PR/CI-rollup/auto-merge surface to a new **`PRForge`** interface
(`OpenPRForBranch`, `PRForBranch`, `PRState`, `CheckState`, `FailureDetail`,
`ListPRFiles`, `CanAutoMerge`, `EnqueueAutoMerge`). Only the `github` adapter
implements `PRForge`; the `git` adapter implements `CodeForge` alone, with no
stub methods at all. Callers discover the PR surface with the standard Go
optional-interface pattern — `pr, ok := cf.(forge.PRForge)` — instead of a
boolean flag, so `internal/settle` branches on whether the concrete adapter
actually has a PR to manage rather than on a self-reported capability.

This also let the combined `Client` interface and its `combinedClient`
wrapper (`forge.NewClient(it, cf)`) retire. A type assertion against
`PRForge` must reach the real adapter value; a wrapper embedding both seams
would still satisfy the assertion even when composed from an IssueTracker
that has nothing to do with the CodeForge's PR capability, defeating the
point. `bootstrap` now wires `IssueTracker` and `CodeForge` as two
independently-typed values (mirroring what `doctor` already did by probing
each seam through its own adapter, per the "git+github" example in
Considered Options), and every consumer takes the exact seam(s) it calls
methods on instead of the combined type.
