# One generic `forgejo` backend, native-HTTP and backend-prefixed

## Context

Spec #1954 brings a `forgejo` backend to full parity with `github` on both
axes of the Backend matrix (`ISSUE_TRACKER=forgejo`, `CODE_FORGE=forgejo`)
and across the whole `PRForge` surface, so an operator hosting on Codeberg —
or any self-hosted Forgejo instance — gets the same label-triggered dispatch,
research verdicts, and settle-to-merge loop GitHub operators already have.
Today those operators are served only by the push-only `git` Code Forge: a
Box can land a branch, but no issues are read, no lifecycle runs, no PR
opens, no CI is watched, and nothing merges.

The full load out is large — token knobs, an in-Box CLI, a `*-forgejo`
prompt-fragment family, a `.forgejo/workflows/` control-plane template set,
doctor and quickstart support, Merge-guard coverage, and both Box access
models — and it will ship across many implementing tickets. Several of its
choices are cross-cutting: they shape seam vocabulary, token naming, and
guard defaults that every later slice inherits and that the next forge
backend (GitLab, Bitbucket; anticipated by [ADR
0013](0013-issue-tracker-and-code-forge-are-independent-seams.md)) will copy.
Re-deciding them per slice would let them drift; deciding them after
implementation would mean the first agents wrote against assumptions no one
had ratified.

This ADR is therefore written **before** any implementation lands, so every
implementing agent inherits the decision set as repo context rather than
re-litigating it. It records only the decisions; the glossary and interface
entries (`Issue Tracker`/`Code Forge`/`PRForge`, the redefinitions of `auto`
and Instruction surface) land with the work that implements them, not with
this ticket. No code ships here.

## Decision

### One generic `forgejo` backend, not a host-named one

The backend is named after the software Codeberg runs, not the instance.
`FORGEJO_BASE_URL` defaults to `https://codeberg.org`, and any
Forgejo/Gitea-compatible instance is the *same* backend pointed at a
different base URL — codeberg.org and a private self-hosted forge share one
adapter with no code difference. It exposes all three seams: an Issue Tracker
adapter, a Code Forge adapter, and the full `PRForge` optional interface —
the second full-parity backend beside `github`, discovered by the standard
type-assertion pattern.

*Rejected:* a `codeberg`-named or `gitea`-named adapter. The former bakes one
host into the seam vocabulary, so a self-hosted operator reads a stranger's
brand in their own config; the latter ages backwards as Forgejo diverges from
its Gitea origin. Naming the backend after the software both instances run
keeps the name true for every instance and stable as the projects drift.

### A native HTTP client, not CLI-exec

The adapter speaks the Forgejo REST API directly from Go, following the
`jira` adapter's pattern — the codebase's first in-process forge HTTP client.
The CI rollup reads the combined commit-status endpoint; mergeability and
native auto-merge use REST; there is no GraphQL dependency.

*Rejected:* the `gh`-exec pattern the `github` backend uses, where the
adapter shells out to a forge CLI and parses its JSON. That pattern was a
convenience `gh` specifically affords — a first-party CLI with stable JSON
output covering the whole surface. `tea`/`fj` do not afford it: their JSON is
patchy and they are missing `PRForge` verbs outright, so a CLI-exec forgejo
adapter would dead-end on the verbs that matter most. The client's shape
(auth header, base-URL joining, error taxonomy) is written as if GitLab will
copy it next, because it will.

### Backend-prefixed token names, as the convention for every future backend

The knobs are `FORGEJO_BASE_URL`, `FORGEJO_TOKEN`, and `BOX_FORGEJO_TOKEN`
(the last for two-actor separation) — each carrying the backend name as a
prefix. This ADR records backend-prefixing as the **naming convention every
future backend follows**, not a one-off for forgejo.

*Rejected:* generic per-axis names such as `FORGE_TOKEN`/`TRACKER_TOKEN`. The
two axes are freely combinable ([ADR
0013](0013-issue-tracker-and-code-forge-are-independent-seams.md)), so a
single generic token cannot serve a mixed-backend run — e.g.
`ISSUE_TRACKER=forgejo` with a different Code Forge, where each backend needs
its own credential. And `GH_TOKEN` is externally load-bearing: `gh` itself
reads it, so it is not spindrift's name to generalize away. Backend-prefixed
names are unambiguous under every matrix cell and leave each backend's
externally-meaningful variables untouched. Aliasing or migrating the existing
`GH_*`/`JIRA_*` knobs to a generic scheme is explicitly rejected here, not
merely deferred.

### No token-refresh analog

Forgejo has no GitHub-App model, and Forgejo PATs do not expire. The
GitHub-App minting/refresher apparatus — `GH_TOKEN_REFRESH_FILE` and the
backgrounded re-mint loop — therefore has no forgejo analog and is simply
absent, not ported. The refresh-file knob stays github-only.

*Rejected:* building a refresh mechanism for forgejo anyway. There is no
expiring-token model to serve, so a refresh loop would rotate a credential
that never goes stale — machinery with no failure mode to prevent.

### Both Box access models

`read-write` is the default, matching github parity: the Box holds a
`FORGEJO_TOKEN` (or a distinct `BOX_FORGEJO_TOKEN`) and writes in-box.
`read-only` rides the landed [ADR
0034](0034-host-mediated-github-forge-and-issue-access.md) host-mediation —
branch as a bundle, PR as a nonce-guarded intent line, comment as a relayed
line — unchanged. Nothing about that host-mediation model is github-specific;
forgejo satisfies the same capability gate by implementing the same
`BundleRelay`/`DraftPRCreator`/`HostPostedCommenter` seams, and two-actor
separation maps to a second machine user's PAT (`BOX_FORGEJO_TOKEN`) plus
Forgejo branch-protection rules barring that user from the base branch — the
same two routes to the "the Box cannot unilaterally update the base branch"
guarantee ADR 0034 documents for github.

*Rejected:* shipping only `read-write` and leaving `read-only` a
github-only capability — treating ADR 0034's host-mediation as
github-specific machinery. Nothing in that model is: forgejo clears the same
capability gate through the same
`BundleRelay`/`DraftPRCreator`/`HostPostedCommenter` seams, so restricting it
to github would strand self-hosted operators on the one access model that
keeps a forge token out of the Box, for no capability reason.

### `fj` over `tea` as the in-Box CLI

The in-Box tool baked into Harness plumbing when the backend is forgejo is
`fj` (forgejo-cli), with curl-against-the-REST-API as the documented fallback
for the rare verbs `fj` lacks. A new `*-forgejo` prompt-fragment family names
these commands and is pinned by the prompt eval-checks exactly as the `gh`
family is, so a drifted `fj` invocation fails evaluation.

*Rejected:* `tea`, and pure curl. `tea` has no CI-status verb — `fj pr status
--wait` is precisely the CI-read verb the fix-pass fragments need and `tea`
cannot provide — and it is Gitea-first, so it ages backwards as Forgejo
diverges, the same reason the backend is not `gitea`-named. Pure curl works
but hand-assembled JSON in every fragment is worse than porcelain for the
common path; it stays the fallback for the uncommon one, not the default.

### `MERGE_MODE=auto` generalizes to the forge's native auto-merge

All three `MERGE_MODE` values keep meaning on forgejo. `manual` and
`immediate` are backend-independent already; `auto` enqueues Forgejo's native
merge-when-checks-succeed, the direct analog of GitHub's auto-merge. The
glossary definition of `auto` generalizes from "native GitHub auto-merge" to
"the forge's native auto-merge — meaningful on any `PRForge` backend," so no
mode silently loses meaning on this backend.

*Rejected:* leaving `auto` github-specific and degrading it to `immediate` (or
erroring) on forgejo. Forgejo has a real native auto-merge, so degrading it
would discard a capability the forge actually offers and break the
[ADR 0012](0012-merge-mode-and-agent-complete-decoupling.md) contract that
every mode carries its meaning across backends.

### `.forgejo/` is guarded unconditionally, on every backend

`.forgejo/` joins the Merge guard's default guarded paths **unconditionally**,
on every backend — not only when the selected forge is forgejo. Forgejo also
reads `.github/workflows` as a compatibility fallback, so on a repo mirrored
across both forges *both* directories are live Instruction surface at once.
The glossary's Instruction surface entry generalizes accordingly.

*Rejected:* a backend-conditional guard that adds `.forgejo/` only under
`CODE_FORGE=forgejo`. That leaves a hole in exactly the mirrored-repo case
that matters: a poisoned `.forgejo/` CI workflow could ride an auto-merge on
the GitHub side of a mirror, where the guard would not be watching it. The
guard bounds drift, not adversaries ([ADR
0016](0016-merge-guard-bounds-drift-not-adversaries.md)), but a guarded-path
set that depends on the active backend is drift the guard should have caught.

## Consequences

- Every later forgejo slice — adapter, CLI plumbing, prompt fragments,
  workflow templates, doctor, quickstart — inherits these decisions as
  ratified repo context instead of re-deciding them, and reads the same
  rejected-alternative rationale when tempted to revisit one.
- Backend-prefixed token naming is now the recorded convention; the next
  forge backend names its knobs `<BACKEND>_TOKEN` / `BOX_<BACKEND>_TOKEN`
  rather than reopening the generic-vs-prefixed question.
- The native-HTTP client establishes the shape (auth header, base-URL
  joining, error taxonomy) a future GitLab/Bitbucket adapter is expected to
  follow, so "the first forge HTTP client" is a template, not a one-off.
- `read-only` is available on forgejo by satisfying ADR 0034's capability
  gate; no new read-only machinery is forgejo-specific.
- The Merge guard's default guarded-path set gains `.forgejo/` on every
  backend, closing the mirrored-repo hole; this is the one guard-surface
  change the decision set implies for backends other than forgejo.
- No refresh-file, no `tea` support, no host-named adapter, and no generic
  token scheme ship for forgejo — each is a recorded rejection a future
  contributor can cite rather than re-argue.
- This ticket ships the ADR only. The glossary edits, interface entries, and
  every wiring change the decisions describe land with the tickets that
  implement them.
