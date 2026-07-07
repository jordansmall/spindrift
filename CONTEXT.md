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
The GitHub repository whose issues the agents work, identified by `REPO_SLUG`.
Always cloned fresh inside the container, never read from a host checkout — so it
is a distinct role from the Consumer flake even when they are the same repo.
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

**Forge**:
The seam through which the Harness speaks to the Target repo's host. Today's
only adapter is `gh`-exec (the GitHub CLI); the `forge.Client` interface in
`cmd/launcher/internal/forge` isolates all GitHub API calls so the merge gate
and dispatch logic are testable without the real CLI. The name is intentionally
abstract — a future adapter could speak REST or GraphQL directly.
_Avoid_: GitHub adapter, API layer, client wrapper.

**Label lifecycle**:
The dispatch states of an issue, carried as labels on the Target repo:
`ready-for-agent` (a human marks the issue dispatchable — the label is the
launch button) → `agent-in-progress` (a Box has been dispatched; re-runs skip
it) → `agent-complete` (the agent produced a green PR and has nothing left to
do; merge is a separate downstream function gated by MERGE_MODE) or
`agent-failed` (the Box crashed or CI never reached green past
MAX_FIX_ATTEMPTS; human triage, re-label to retry). A merge failure after
green leaves the issue `agent-complete` with a merge-blocked note — never
`agent-failed`. MERGE_MODE controls what happens after green: `immediate`
merges the PR automatically; `manual` (default) leaves the open PR for a
human to approve and merge; `auto` is reserved for native GitHub auto-merge.
_Avoid_: status, queue, state machine.

`fanout-blocker` is a planning gate, orthogonal to the dispatch-state lifecycle
above. It marks an issue that must land before concurrent fan-out is enabled for
any newer issue. The dogfood wrapper exports `BARRIER_LABEL=fanout-blocker` so
the Launcher fences everything numbered above the lowest open `fanout-blocker`
issue; when that issue closes, the fence lifts to the next barrier (or the rest
of the backlog). An issue may carry `fanout-blocker` alongside any dispatch
state — it is not a state the issue transitions through.
_Avoid_: barrier state, fan-out label, serial gate.

**Outcome line**:
The machine-readable final line a Box writes to stdout, parsed by the Launcher
to learn whether a PR is ready for CI-watch-and-merge, blocked, or failed.
Grammar: `SPINDRIFT_OUTCOME issue=<num> pr=<url> status=<status> note=<text>`
where `note` may contain spaces and `=`. The `cmd/launcher/internal/outcome`
package is the authoritative spec and implementation: `Parse` validates the
grammar, `Line` produces the canonical form, and `LastInLog` scans a Box log
while gracefully skipping lines too large for the scanner buffer.
_Avoid_: result line, output line, status line.
