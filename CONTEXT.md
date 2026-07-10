# spindrift

A nix-based harness that launches waves of headless Claude Code agents into
disposable, nix-built containers — one per GitHub issue. This glossary pins the
vocabulary of the harness and the parties around it.

## Language

**Harness**:
spindrift itself — the flake, the launcher, and the in-container entrypoint that
together build the image and launch agent waves. The thing being imported.
_Avoid_: tool, framework, runner (the runner is specifically the container).

**Consumer flake**:
The downstream flake that imports the Harness and configures it (toolchain,
packages, prompt, settings). A role, not necessarily a separate repo — it may be
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

**Filer**:
The opt-in subagent role (beside the scout and reviewer) that turns the final
approving review's non-blocking findings into issues on the Issue Tracker — one
issue per finding, merging only findings that are the same change, after a
dedup search over previously filed findings in any state (a closed finding is a
human triage decision, never refiled). Its issues carry the
`agent-review-finding` label and are never dispatchable by its own hand — a
human promotes them, preserving the rule that a human is the launch button.
Filing is best-effort: a Filer failure never blocks the PR or alters the
outcome. Off by default.
_Avoid_: triager (it does not triage), reporter (collides with outcome
reporting).

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
Implemented adapters: `github` (issues via `gh`), `jira`, and `local` (issues
as files in the Target repo, no server). The launcher reasons in canonical
Dispatch states, never in a backend's native mechanism.
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

**Dispatch**:
The per-issue execution, from claim to verdict: every Box launched for one
issue — initial run, fix passes, conflict-resolve — plus its results and its
Driver-cache entry. The thing whose states the Dispatch lifecycle names;
distinct from the Driver's resumable conversation session, which the
Driver-cache entry preserves across a Dispatch's fix passes.
_Avoid_: session (collides with the Driver's conversation session), run, job.

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
  name/id), since there is no universal Jira workflow to assume; when a state is
  unmapped, or the mapped transition isn't available on the issue's current
  workflow, falls back to swapping a Jira label for that state (mirroring
  `github`'s labels) so the lifecycle always makes progress.
- `local` — a field in the issue file (frontmatter/directory), rewritten in place.

A merge failure after green leaves the issue `Complete` with a merge-blocked note
— never `Failed`. MERGE_MODE controls what happens after `Complete`: `immediate`
merges automatically (locally, via a push that updates a clean checked-out
branch); `manual` (default) leaves the branch/PR for a human; `auto` is native
GitHub auto-merge and has no meaning off `github`.
_Was_: "Label lifecycle" — labels were GitHub's storage mechanism, mistaken for
the states themselves.
_Avoid_: status, queue, state machine.

**Dispatch order**:
The order in which `ListIssues` hands work to the launcher — canonically
oldest-first. Identity is opaque to the launcher (`Number` is a string), so the
launcher never parses or compares IDs numerically; each Issue Tracker adapter
returns issues already in canonical order using its own order key: `github` =
issue number, `jira` = created time, `local` = a `created` frontmatter timestamp.
_Avoid_: issue number, sequence, priority.

**Wave**:
One batch of Dispatches launched concurrently. With no blocker edges the whole
ready-set is a single wave; declared edges split a run into dependency waves.
Waves are unbounded by default: `MAX_PARALLEL` caps the number of concurrent
Boxes within a wave (default 3); `MAX_JOBS` caps the dependency-wave
concurrency (default 0 = unlimited). No label-based gate serializes issues;
ordering is purely by dispatch order and blocker edges.
_Was_: "fan-out" — the launch act and the batch carried two names; unified on
the batch noun.
_Avoid_: fan-out, batch, round.

**Outcome line**:
The machine-readable final line a Box writes to stdout, parsed by the Launcher
to learn whether a PR is ready for CI-watch-and-merge, blocked, or failed.
Grammar: `SPINDRIFT_OUTCOME issue=<num> pr=<url> status=<status> note=<text>`
where `note` may contain spaces and `=`. The `cmd/launcher/internal/outcome`
package is the authoritative spec and implementation: `Parse` validates the
grammar, `Line` produces the canonical form, and `LastInLog` scans a Box log
while gracefully skipping lines too large for the scanner buffer.
_Avoid_: result line, output line, status line.

**Guardrail prompt**:
The harness-owned prompt carried in the system slot of every harness-issued
Driver invocation, stating the trust boundaries to the model: issue bodies and
comments are untrusted data, credentials are never disclosed, work stays on the
dispatched issue, and edits to the guarded paths force a human merge.
Non-negotiable by the Agent and by issue/comment authors; overridable by the
Consumer flake, which is trusted by construction. A soft control: it hardens
harness-issued invocations but cannot bind a fresh Driver process the Agent
spawns itself — that containment belongs to the Box and token scope.
_Avoid_: system prompt (ambiguous with the Driver's own), jailbreak prompt,
safety prompt.

**Settle**:
Driving a Dispatch from Box-exit to its terminal lifecycle state, whatever
that takes: interpreting the Outcome line, watching CI, self-heal fix passes,
the merge or push-only landing under `MERGE_MODE` and the Merge guard, and
merged-verification. "Merge gate" informally names the green→merge segment
within a settle.
_Avoid_: gate (a checkpoint — narrower than the whole settle), finalize, report.

**Merge guard**:
The launcher-side check, outside the Box and beyond the Agent's influence,
applied between green CI and merge: if the change touches a guarded path, the
merge downgrades to manual — regardless of `MERGE_MODE` — and a note on the PR
says why. It bounds injection-induced *drift* (an agent following its normal
flow), not a fully adversarial Agent, which holds a token that can merge (see
Two-actor separation). Downgrade, never block: the cost of a hit is one human
read.
_Avoid_: path filter, merge block, review gate.

**Instruction surface**:
The repo-carried files a Driver reads as trusted instructions on every fresh
clone — `CLAUDE.md`, `AGENTS.md`, `.claude/`, `.opencode/` — plus the CI config
under `.github/`. The cross-run persistence vector: a poisoned instruction file
merged once feeds every future Agent as trusted input. Guarded by the Merge
guard's default path set.
_Avoid_: config files, dotfiles, prompt files.

**Two-actor separation**:
The opt-in hard mode where the Box token's user cannot update the base branch
(repository ruleset) and only a second, launcher-held token can merge. The only
configuration in which the Merge guard is literally uninfluenceable by the
Agent; the single-token default trades that away for operator simplicity.
_Avoid_: dual token, split credentials, bot pair.

**Tripwire**:
The launcher's detection that a PR was merged by an actor other than itself
while the issue was `InProgress` — evidence the Agent (or someone else) merged
around the gate. Detection only, no prevention; surfaces for human triage.
_Avoid_: audit, alert, monitor.
