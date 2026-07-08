# spindrift

A nix-based harness that fans out headless Claude Code agents into disposable,
nix-built containers — one per GitHub issue. This glossary pins the vocabulary
of the harness and the parties around it.

## Language

**Harness**:
spindrift itself — the flake, the launcher, and the in-container entrypoint that
together build the image and fan out agents. The thing being imported.
_Avoid_: tool, framework, runner (the runner is specifically the container).

**Consumer flake**:
The downstream flake that imports the Harness and configures it (toolchain,
packages, prompt, defaults). A role, not necessarily a separate repo — it may be
the same repo as the Target repo.
_Avoid_: client, user repo, parent flake.

**Target repo**:
The repository whose issues the agents work. Always cloned fresh inside the Box
from a git *remote* — `REPO_SLUG` on GitHub under a `github` Code Forge, or a
plain remote URL under a `git` Code Forge — never read from a host checkout. So
it stays a distinct role from the Consumer flake even when they are the same repo.
_Avoid_: source repo, project repo.

**Agent**:
A single headless Driver process — `claude -p …` or `opencode run …`, run with
`--dangerously-skip-permissions`/`--auto` — working one issue inside one
container. The Agent is the running process; the Driver is which CLI it is.

**Driver**:
The swappable agent CLI baked into the Box: `claude` or `opencode`. A build-time
seam (one Driver per image, picked beside `runtime`), analogous to the Forge and
runner seams. Each Driver normalizes its tool's quirks at its own boundary and
has two coordinated halves keyed by one name — a nix-generated in-box half
(invocation, agent-config, outcome extraction) and a Go host-side strategy in the
launcher (transient classification, heartbeat). _Provisional name_ — may be
renamed (e.g. "agent harness"). _Avoid_: engine, backend, tool.

**Provider**:
The model backend a Driver talks to: Anthropic, GitHub Copilot, OpenAI. Distinct
from the Driver — the `opencode` Driver is provider-flexible, so "GitHub Copilot
support" is the opencode Driver pointed at the `github-copilot` provider, with
`MODEL` provider-namespaced (`github-copilot/…`). The `claude` Driver is
effectively locked to Anthropic. _Avoid_: model host, vendor, backend.

**Box**:
The disposable per-issue podman container — the isolation boundary that makes
`--dangerously-skip-permissions` safe.
_Avoid_: sandbox, runner, worker.

**Harness plumbing**:
The language-agnostic tools every Agent needs regardless of the Target — shell,
git, gh, the Driver CLI, jq, CA certs, nix. Always baked into the image and
always kept on PATH, even when the Agent operates inside the Project toolchain.
Distinct from the Project toolchain: plumbing is spindrift's, the toolchain is
the Target's.
_Avoid_: base image, harness deps, system tools.

**Project toolchain**:
The Target's language/build tools (rustc, node, sqlx, …). Sourced devShell-first:
when the cloned Target has a usable devShell the Agent operates inside it via
`nix develop` (the default, zero-config path); otherwise it falls back to the
baked `packages` list. Baking is an opt-in *speed* knob — a warm store so the
runtime `nix develop` substitutes nothing — not the primary source (ADR 0014).
_Avoid_: packages, baked toolchain, dependencies.

**Issue Tracker**:
The seam that supplies work and carries dispatch state: listing dispatchable
issues, reading an issue's body/title/state, transitioning its Dispatch state,
and posting comments. One of two independent axes (the other is the Code Forge).
Planned adapters: `github` (issues via `gh`), `jira`, and `local` (issues as
files in the Target repo, no server). The launcher reasons in canonical Dispatch
states, never in a backend's native mechanism.
_Avoid_: issue source, ticketing, backlog.

**Code Forge**:
The seam through which the Harness lands code. A second axis independent of the
Issue Tracker, freely combinable with any of them. **A git remote always exists**
— the Box clones from it and pushes to it exactly as it does today; there is no
mounting of a host working copy and no launcher-side git. Two values:

- `github` — the full flow: open a PR, watch the CI rollup, rebase, merge. The
  `forge.Client` `gh`-exec adapter; only this value has a PR, CI-watch, or merge
  gate.
- `git` — **push-only** to a plain git remote URL (self-hosted git, gitea,
  GitLab-without-MRs, a bare server repo): clone, commit to a per-issue branch,
  push, and stop. No PR, no CI, no merge gate. `MERGE_MODE` maps to remote pushes
  — `manual` pushes the feature branch; `immediate` pushes straight to the target
  branch; `auto` is native GitHub auto-merge and has no meaning here.

The no-remote / fully-local code path (mount the operator's repo, launcher lands
the branch) was considered and **cut**: a git remote to push to is a hard
requirement. Solo/private use is served by pairing `git` here with an
`ISSUE_TRACKER=local` (private issues, published code).
_Was_: "Forge" (a single seam over the Target repo host); split into Issue
Tracker + Code Forge once issues and code host became independent axes.
_Avoid_: GitHub adapter, API layer, client wrapper.

**Backend matrix**:
Issue Tracker and Code Forge are two independent, freely-combinable axes
(`ISSUE_TRACKER` × `CODE_FORGE`). All cells are permitted — the harness does not
reject "incoherent" pairings (e.g. github-issues + no-code-forge); an operator
who selects one owns the consequences.
_Avoid_: preset, profile, mode.

**Dispatch lifecycle**:
The canonical dispatch states the launcher reasons in, independent of how any
one Issue Tracker stores them: `Dispatchable` (a human marked the issue ready —
the launch button) → `InProgress` (a Box has been dispatched; re-runs skip it) →
`Complete` (the agent finished its work — a green PR on `github`, or a landed
branch when the Code Forge is push-only/absent) or `Failed` (the Box crashed or
never reached green past MAX_FIX_ATTEMPTS; human triage, re-transition to retry).
Each Issue Tracker adapter maps these states to its native mechanism:

- `github` — labels (`ready-for-agent` → `agent-in-progress` →
  `agent-complete`/`agent-failed`), swapped atomically. This is the original,
  unchanged mechanism.
- `jira` — a configurable status mapping (state → that project's workflow status
  name/id), since there is no universal Jira workflow to assume; supported out of
  the gate, not label-mirrored.
- `local` — a field in the issue file (frontmatter/directory), rewritten in place.

A merge failure after green leaves the issue `Complete` with a merge-blocked note
— never `Failed`. MERGE_MODE controls what happens after `Complete`: `immediate`
merges automatically (locally, via a push that updates a clean checked-out
branch); `manual` (default) leaves the branch/PR for a human; `auto` is native
GitHub auto-merge and has no meaning off `github`.
_Was_: "Label lifecycle" — labels were GitHub's storage mechanism, mistaken for
the states themselves.
_Avoid_: status, queue, state machine.

`fanout-blocker` is a planning gate, orthogonal to the dispatch-state lifecycle
above. It marks an issue that must land before concurrent fan-out is enabled for
any newer issue. The dogfood wrapper exports `BARRIER_LABEL=fanout-blocker` so
the Launcher fences everything numbered above the lowest open `fanout-blocker`
issue; when that issue closes, the fence lifts to the next barrier (or the rest
of the backlog). An issue may carry `fanout-blocker` alongside any dispatch
state — it is not a state the issue transitions through.
_Deprecated_: this was initial dogfooding scaffolding and is slated for removal
in its own cleanup task. It is GitHub-only and is **not** extended to the `jira`
or `local` Issue Trackers, which rely on canonical dispatch order alone (see
Dispatch order below).
_Avoid_: barrier state, fan-out label, serial gate.

**Dispatch order**:
The order in which `ListIssues` hands work to the launcher — canonically
oldest-first. Identity is opaque to the launcher (`Number` is a string), so the
launcher never parses or compares IDs numerically; each Issue Tracker adapter
returns issues already in canonical order using its own order key: `github` =
issue number, `jira` = created time, `local` = a `created` frontmatter timestamp.
_Avoid_: issue number, sequence, priority.

**Outcome line**:
The machine-readable final line a Box writes to stdout, parsed by the Launcher
to learn whether a PR is ready for CI-watch-and-merge, blocked, or failed.
Grammar: `SPINDRIFT_OUTCOME issue=<num> pr=<url> status=<status> note=<text>`
where `note` may contain spaces and `=`. The `cmd/launcher/internal/outcome`
package is the authoritative spec and implementation: `Parse` validates the
grammar, `Line` produces the canonical form, and `LastInLog` scans a Box log
while gracefully skipping lines too large for the scanner buffer.
_Avoid_: result line, output line, status line.
