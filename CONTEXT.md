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
A single headless `claude -p … --dangerously-skip-permissions` process working
one issue inside one container.

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
it) → `agent-failed` (the Box exited non-zero; human triage, re-label to
retry). Success needs no terminal label — the merged PR closes the issue.
_Avoid_: status, queue, state machine.

**Outcome line**:
The machine-readable final line a Box writes to stdout, parsed by the Launcher
to learn whether a PR is ready for CI-watch-and-merge, blocked, or failed.
Grammar: `SPINDRIFT_OUTCOME issue=<num> pr=<url> status=<status> note=<text>`
where `note` may contain spaces and `=`. The `cmd/launcher/internal/outcome`
package is the authoritative spec and implementation: `Parse` validates the
grammar, `Line` produces the canonical form, and `LastInLog` scans a Box log
while gracefully skipping lines too large for the scanner buffer.
_Avoid_: result line, output line, status line.
