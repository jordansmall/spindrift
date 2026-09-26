# spindrift

[![CI](https://github.com/jordansmall/spindrift/actions/workflows/ci.yml/badge.svg)](https://github.com/jordansmall/spindrift/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/jordansmall/spindrift)](https://github.com/jordansmall/spindrift/releases/latest)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

*A nix-based agent automation harness, consumed as a flake.*

Label an issue `ready-for-agent`, run `spindrift dispatch`, and a headless
coding agent works it inside a **disposable, nix-built Box** — a fresh clone,
a scoped token, no host access — then opens a pull request. The host-side
launcher watches CI and decides whether it merges.

Two ideas carry it (see [`CONTEXT.md`](CONTEXT.md) for the full vocabulary):

1. **The Box is the isolation boundary.** The agent runs with permission
   prompts skipped, which is safe because everything it can touch is
   throwaway. What bounds the blast radius is the token you hand it.
2. **The toolchain is a nix image.** The Box is built from the *same* pinned
   nixpkgs as your dev shell, so the agent's environment and yours never
   drift. No hand-maintained Dockerfile.

spindrift is **imported by your flake** (the *Consumer flake*), not cloned.
Everything around the two ideas is a seam you pick at build time:

| seam | choices |
| ---- | ------- |
| **Driver** — the agent CLI | [Claude Code](https://claude.com/claude-code) (default), [opencode](https://opencode.ai) with any Provider it supports (e.g. GitHub Copilot) |
| **Issue Tracker** — where work comes from | GitHub (default), Forgejo/Codeberg, Jira, local offline files |
| **Code Forge** — where work lands | GitHub (default), Forgejo/Codeberg, a plain git remote, a host-local bundle |
| **Runtime** — what confines the Box | podman (default), docker, Rancher Desktop (`rancher`), bubblewrap (`bwrap`, Linux, daemonless) |

## Prerequisites

- **nix** with flakes enabled.
- **A container runtime** — podman by default; docker, Rancher Desktop, or (on
  Linux) bubblewrap also work. On macOS/Windows, give the podman VM enough RAM
  for your parallel Boxes — see [Podman machine RAM](docs/reference.md#podman-machine-ram).
- **A GitHub credential scoped to the one Target repo** — a fine-grained PAT
  (Issues RW, Contents RW, Pull requests RW, Metadata R), or a
  [GitHub App installation token](docs/reference.md#github-app-installation-token-recommended)
  (recommended for CI).
- **Agent auth** — `claude setup-token` (gives `CLAUDE_CODE_OAUTH_TOKEN`) or an
  `ANTHROPIC_API_KEY`. For opencode + GitHub Copilot instead, see
  [opencode github-copilot credential](docs/reference.md#opencode-driver-github-copilot-provider-credential).
- **Branch protection on the Target repo's base branch** — see
  [Before you deploy](#before-you-deploy). Do not skip this.

## Quick start

### 1. Scaffold a Consumer flake

In an empty directory, run the Quickstart wizard:

```sh
mkdir my-agents && cd my-agents
nix run github:jordansmall/spindrift#quickstart
```

It detects what it can — runtime, git identity, repo slug from `git remote` —
and asks for the rest: your token (audited for over-broad scopes) and agent
auth. Then it:

- writes `flake.nix`, a secrets-only `harness.env` (mode 0600), `.gitignore`,
  and `.envrc`;
- runs `spindrift doctor`, offering to create any missing labels;
- builds the agent image (slow the first time).

It refuses to overwrite an existing `flake.nix` or `harness.env` without
`--force`. A `codeberg.org` remote selects the Forgejo backend automatically.

<details>
<summary>Prefer a hand-edited, fully commented scaffold?</summary>

```sh
mkdir my-agents && cd my-agents
nix flake init -t github:jordansmall/spindrift
$EDITOR flake.nix                    # uncomment forge = { repoSlug = "owner/repo"; }; set your toolchain
$EDITOR prompts/issue-prompt.md      # optional: tune the agent's workflow
cp harness.env.example harness.env   # fill in GH_TOKEN and CLAUDE_CODE_OAUTH_TOKEN
nix develop -c spindrift doctor      # verify, and create the labels
nix develop -c spindrift build       # realize the image
```

The template also ships `skills/` with reference copies of the harness-owned
skills (`auto-format`, `auto-lint`, `check-hygiene`, `code-comments`) that are
baked into every image anyway.

</details>

### 2. Queue an issue and dispatch

Add the `ready-for-agent` label to an issue in the Target repo, then from the
Consumer flake's directory:

```sh
nix develop            # or `direnv allow` — puts your Consumer's spindrift on PATH
spindrift preview      # dry run: what would dispatch pick up?
spindrift dispatch     # one Box per ready-for-agent issue
```

Per-issue logs land in `.spindrift/logs/issue-<n>.log`. When the agent's PR
goes green, the issue is labelled `agent-complete`, and **by default the PR is
left open for you to merge** (`git.merge.policy = "manual"`). See
[Configure](#configure) to have the launcher merge it.

> **Run your Consumer's CLI, not upstream's.** `nix run
> github:jordansmall/spindrift -- dispatch` runs a binary built from
> spindrift's *own* configuration, not yours — your `repoSlug`, toolchain, and
> prompt would be ignored. Use `nix develop` (as above) or `nix run . --
> <verb>`. The upstream ref is fine for `--help`, `--version`, and
> `#quickstart`.

## How a run works

```
spindrift dispatch  ─▶  find ready-for-agent issues (blockers resolved first)
                          └─ one Box per issue, up to dispatch.maxParallel
                               clone → agent implements → commit → push → open PR

launcher (host)     ─▶  merge gate per PR
                          poll CI → green → merge guard → git.merge.policy → agent-complete
                                  → red   → fix Boxes (up to dispatch.retry.maxFix) → re-gate
                                  → exhausted → agent-failed (triage, re-label to retry)
```

The Box implements; the launcher owns the CI verdict and the merge. Labels move
`ready-for-agent` → `agent-in-progress` → `agent-complete` or `agent-failed`.
See [How a run works](docs/reference.md#how-a-run-works) for the full picture.

## Configure

Two places, split by sensitivity:

- **`flake.nix`** — every non-secret knob, under `perSystem.spindrift.<domain>`
  (`agents`, `dispatch`, `forge`, `git`, `infra`, `issues`). Baked in at build
  time; any knob can be overridden for one run with its `--flag` (see
  `spindrift --help --all`).
- **`harness.env`** — secrets only (`GH_TOKEN`, `CLAUDE_CODE_OAUTH_TOKEN`, …).
  Prefer a vault: `GH_TOKEN_CMD="rbw get spindrift-gh-token"` (or `op`, `pass`,
  `vault`) fetches at run time and never writes the value to disk. See
  [Runtime configuration](docs/reference.md#runtime-configuration).

The knobs most people touch first:

```nix
perSystem = { config, pkgs, ... }: {
  spindrift = {
    forge.repoSlug = "owner/repo";                 # the Target repo
    infra.image.packages = p: [ p.go p.gnumake ];  # toolchain baked into the Box
    infra.image.prefetch = "go mod download || true";
    agents.prompt = builtins.readFile ./prompts/issue-prompt.md;

    git.merge.policy = "immediate";   # manual (default) | immediate | auto
    dispatch.maxParallel = 3;         # Boxes at once
    infra.runtime = "podman";         # podman | docker | rancher | bwrap
    # agents.driver = "opencode";     # default: claude
  };

  devShells.default = pkgs.mkShell {
    packages = [ config.packages.spindrift ];  # `nix develop` → `spindrift`
  };
};
```

If the Target repo has its own `devShells`, the Box enters it, so
`infra.image.packages` is only a speed optimization; point it at a leaner shell
with `infra.devShell.name = "ci"`. See
[devShell targeting](docs/reference.md#targeting-repos-that-define-their-own-devshell-toolchain).

Every option, with type and default, is in
[`docs/flake-options.md`](docs/flake-options.md). To wire spindrift into an
existing flake by hand, add `spindrift.url = "github:jordansmall/spindrift";`
to your inputs and `imports = [ spindrift.flakeModules.default ];` to your
flake-parts config — the template's [`flake.nix`](templates/default/flake.nix)
is a complete example. Pin the input in `flake.lock` for reproducible runs.

## Before you deploy

1. **Branch protection is required.** The token that pushes `agent/issue-N`
   branches can also push to the base branch. Block direct pushes and require
   CI status checks. Don't require an approving review: a bot can't approve its
   own PR. Without this, **the harness is not safe to deploy**.
2. **Scope the token to one repo.** A broad token gives a prompt-injected agent
   write access to everything it reaches. See
   [GitHub token permissions](docs/reference.md#github-token-permissions).
3. **Issue bodies and comments are attacker-writable prompt input.** The trust
   boundary is who can apply the label, not who wrote the text.

`spindrift doctor` checks configuration, credentials, connectivity, and labels,
exiting non-zero on anything fatal — use it as a CI preflight. It is silent
when healthy; pass `-v` for the full report. See the
[threat model](docs/reference.md#threat-model).

## Commands

Run `spindrift --help` for the full list; the common ones:

| command | what it does |
| ------- | ------------ |
| `spindrift doctor` | Preflight: config, credentials, connectivity, labels |
| `spindrift build` | Realize the agent image without dispatching |
| `spindrift preview` | Dry run — what `dispatch` would pick up, in order |
| `spindrift dispatch [N…]` | Work every `ready-for-agent` issue (or exactly issues `N…`) |
| `spindrift research [N…]` | Advise-only: review each `agent-research` issue and post one verdict comment |
| `spindrift console` | Interactive backlog: pick issues to dispatch, tail and manage live Boxes |
| `spindrift recover N` | Re-run the merge gate for one issue |
| `nix run .#daemon` | Keep working both queues unattended, picking up newly labelled issues |

Shell completion for bash, fish, and zsh is included — see
[Shell completion](docs/reference.md#shell-completion).

- **Research** never edits code or opens a PR: it posts a
  `recommend`/`reject`/`unclear` verdict under its own `agent-research*` label
  family, and a human acts on it. See [Research dispatch](docs/reference.md#research-dispatch).
- **Console** — see [`docs/console.md`](docs/console.md).
- **Daemon** — see [Daemon](docs/reference.md#daemon).

## Optional behaviors

Each is off by default; details in [`docs/reference.md`](docs/reference.md).

- **Merge guard** *(on)* — a PR touching `.github/**`, `.forgejo/**`,
  `**/CLAUDE.md`, `**/AGENTS.md`, `.claude/**`, or `.opencode/**` is never
  auto-merged. See [Merge guard](docs/reference.md#merge-guard).
- **Read-only Box** — `forge.boxAccess = "read-only"`: the Box gets a read-only
  token and the launcher pushes, opens the PR, and comments on its behalf, so
  the agent structurally cannot merge. See
  [Read-only Box](docs/reference.md#read-only-box-box_forge_and_issue_accessread-only).
- **Two-actor separation** — give the Box a second machine user's token
  (`BOX_GH_TOKEN`) barred from the base branch by a ruleset. See
  [Two-actor separation](docs/reference.md#two-actor-separation-opt-in-hard-mode).
- **Auto-format / auto-lint** — `agents.format.enable` / `agents.lint.enable`
  format or lint changed files before each commit, tool auto-detected.
- **Subagent roster** — tune the model and effort of the scout, reviewer,
  worker, and filer subagents, or add your own, via `agents.models.roster`.
  Enabling the filer turns non-blocking review findings into
  `agent-review-finding` issues. See
  [Subagent roster](docs/reference.md#subagent-roster) and
  [Filer](docs/reference.md#filer).
- **Blockers** *(on)* — an issue waits until its blockers reach
  `agent-complete`, using the tracker's native dependencies or `depends on #N`
  prose. See [Issue Tracker backends](docs/reference.md#issue-tracker-backends).
- **Touch-set overlap** *(on)* — an issue's `## Touches` path list defers it
  while it overlaps an in-progress issue. See
  [Declared touch-set overlap](docs/reference.md#declared-touch-set-overlap).
- **Fully offline** — `issues.tracker = "local"` plus `forge.backend = "local"`
  runs a private loop off Markdown issue files with no network. See
  [Local code forge](docs/reference.md#local-code-forge-code_forgelocal).

## Documentation

| document | what's in it |
| -------- | ------------ |
| [`docs/reference.md`](docs/reference.md) | The full manual: CLI, configuration, runtime env, how a run works, labels, backends, research, daemon, security, macOS notes |
| [`docs/flake-options.md`](docs/flake-options.md) | Every flake option — path, type, default, description (generated) |
| [`docs/console.md`](docs/console.md) | The interactive Console |
| [`CONTEXT.md`](CONTEXT.md) | Vocabulary: Harness, Consumer flake, Target repo, Box, Driver, Issue Tracker, Code Forge |
| [`SECURITY.md`](SECURITY.md) | Reporting a vulnerability; deployment threat model |
| [`MIGRATING.md`](MIGRATING.md) | Deprecations and breaking changes |
| [`VERSIONING.md`](VERSIONING.md) | Semver policy and what each surface guarantees |
| [`CONTRIBUTING.md`](CONTRIBUTING.md) | Working on spindrift itself |
| [`docs/adr/`](docs/adr/) | Architecture decision records |

## Credits

Heavily inspired by Matt Pocock's
[Sandcastle](https://github.com/mattpocock/sandcastle) project.

## License

MIT — see [LICENSE](LICENSE).
