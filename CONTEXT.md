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
