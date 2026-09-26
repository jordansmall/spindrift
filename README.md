# spindrift

[![CI](https://github.com/jordansmall/spindrift/actions/workflows/ci.yml/badge.svg)](https://github.com/jordansmall/spindrift/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/jordansmall/spindrift)](https://github.com/jordansmall/spindrift/releases/latest)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

*A nix-based agent automation harness, consumed as a flake.*

Label an issue `ready-for-agent` and run `spindrift dispatch`. A headless coding
agent clones the repo into a disposable, nix-built Box, works the issue with a
token scoped to that one repo, and opens a pull request. The Box has no access
to the host. A launcher on the host watches CI and decides whether the PR
merges.

The design rests on two choices. [`CONTEXT.md`](CONTEXT.md) defines the terms
used below.

1. **The Box is the isolation boundary.** The agent runs with permission
   prompts skipped. That is safe because the Box is thrown away after each
   issue, and the agent can reach only what its token allows.
2. **The toolchain is a nix image.** The Box is built from the same pinned
   nixpkgs as your dev shell, so the agent gets the tools you have. There is
   no Dockerfile to maintain.

You don't clone spindrift. Your own flake, the Consumer flake, imports it and
picks one option for each of these at build time:

| component | choices |
| --------- | ------- |
| Driver, the agent CLI | [Claude Code](https://claude.com/claude-code) (default), or [opencode](https://opencode.ai) with any Provider it supports, such as GitHub Copilot |
| Issue Tracker, where work comes from | GitHub (default), Forgejo or Codeberg, Jira, local offline files |
| Code Forge, where work lands | GitHub (default), Forgejo or Codeberg, a plain git remote, a bundle kept on the host |
| Runtime, what confines the Box | podman (default), docker, Rancher Desktop (`rancher`), bubblewrap (`bwrap`, Linux only, needs no daemon) |

## Prerequisites

- nix with flakes enabled.
- A container runtime. podman is the default, and docker, Rancher Desktop, or
  bubblewrap on Linux also work. On macOS and Windows, the podman VM needs
  enough RAM for all the Boxes you run at once. See
  [Podman machine RAM](docs/reference.md#podman-machine-ram).
- A GitHub credential scoped to the one Target repo. Use a fine-grained PAT
  with Issues RW, Contents RW, Pull requests RW, and Metadata R. In CI, a
  [GitHub App installation token](docs/reference.md#github-app-installation-token-recommended)
  is the recommended alternative.
- Agent auth. Run `claude setup-token` to get a `CLAUDE_CODE_OAUTH_TOKEN`, or
  use an `ANTHROPIC_API_KEY`. To use opencode with GitHub Copilot instead, see
  [opencode github-copilot credential](docs/reference.md#opencode-driver-github-copilot-provider-credential).
- Branch protection on the Target repo's base branch. See
  [Before you deploy](#before-you-deploy), and don't skip it.

## Quick start

### 1. Scaffold a Consumer flake

In an empty directory, run the Quickstart wizard:

```sh
mkdir my-agents && cd my-agents
nix run github:jordansmall/spindrift#quickstart
```

The wizard detects your container runtime, your git identity, and the repo
slug from `git remote`. It asks for your token, checks it for over-broad
scopes, and asks for agent auth. Then it does the following:

- writes `flake.nix`, a secrets-only `harness.env` with mode 0600,
  `.gitignore`, and `.envrc`
- runs `spindrift doctor` and offers to create any missing labels
- builds the agent image, which is slow the first time

The wizard won't overwrite an existing `flake.nix` or `harness.env` unless you
pass `--force`. If the remote is on `codeberg.org`, it selects the Forgejo
backend.

<details>
<summary>Prefer to start from a commented template and edit it by hand?</summary>

```sh
mkdir my-agents && cd my-agents
nix flake init -t github:jordansmall/spindrift
$EDITOR flake.nix                    # uncomment forge = { repoSlug = "owner/repo"; }; set your toolchain
$EDITOR prompts/issue-prompt.md      # optional: tune the agent's workflow
cp harness.env.example harness.env   # fill in GH_TOKEN and CLAUDE_CODE_OAUTH_TOKEN
nix develop -c spindrift doctor      # check the setup and create the labels
nix develop -c spindrift build       # build the image
```

The template's `skills/` directory holds copies of the four skills spindrift
bakes into every image: `auto-format`, `auto-lint`, `check-hygiene`, and
`code-comments`. You don't need to wire them in.

</details>

### 2. Queue an issue and dispatch

Add the `ready-for-agent` label to an issue in the Target repo. Then run these
from the Consumer flake's directory:

```sh
nix develop            # or `direnv allow`; puts your Consumer's spindrift on PATH
spindrift preview      # dry run: lists what dispatch would pick up
spindrift dispatch     # starts one Box per ready-for-agent issue
```

Each issue's log goes to `.spindrift/logs/issue-<n>.log`. When CI passes on
the agent's PR, the launcher labels the issue `agent-complete`. By default it
leaves the PR open for you to merge, because `git.merge.policy` defaults to
`"manual"`. To have the launcher merge it, see [Configure](#configure).

> **Run your Consumer's CLI, not upstream's.** `nix run
> github:jordansmall/spindrift -- dispatch` runs a binary built from
> spindrift's own configuration. It ignores your `repoSlug`, toolchain, and
> prompt. Use `nix develop` as shown above, or `nix run . -- <verb>`. The
> upstream ref is fine for `--help`, `--version`, and `#quickstart`.

## How a run works

```
spindrift dispatch  ─▶  find ready-for-agent issues whose blockers are done
                          └─ one Box per issue, up to dispatch.maxParallel
                               clone → agent implements → commit → push → open PR

launcher (host)     ─▶  merge gate per PR
                          poll CI → green → merge guard → git.merge.policy → agent-complete
                                  → red   → fix Boxes (up to dispatch.retry.maxFix) → re-gate
                                  → exhausted → agent-failed (triage, re-label to retry)
```

The Box writes the code. The launcher decides whether CI passed and whether to
merge. An issue's label moves from `ready-for-agent` to `agent-in-progress`,
then to `agent-complete` or `agent-failed`. See
[How a run works](docs/reference.md#how-a-run-works) for the details.

## Configure

Settings live in two places:

- `flake.nix` holds every setting that isn't a secret, under
  `perSystem.spindrift.<domain>`. The domains are `agents`, `dispatch`,
  `forge`, `git`, `infra`, and `issues`. These values are baked in when you
  build. To change one for a single run, pass its `--flag`. `spindrift --help
  --all` lists them.
- `harness.env` holds secrets only, such as `GH_TOKEN` and
  `CLAUDE_CODE_OAUTH_TOKEN`. You can fetch each one from a vault instead of
  storing it. `GH_TOKEN_CMD="rbw get spindrift-gh-token"` runs that command at
  dispatch time and never writes the token to disk. `op`, `pass`, and `vault`
  work the same way. See
  [Runtime configuration](docs/reference.md#runtime-configuration).

These are the settings people usually change first:

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

If the Target repo defines its own `devShells`, the Box enters that shell.
`infra.image.packages` then only saves the Box from fetching those tools at
start. To use a smaller shell than the default, set
`infra.devShell.name = "ci"`. See
[devShell targeting](docs/reference.md#targeting-repos-that-define-their-own-devshell-toolchain).

[`docs/flake-options.md`](docs/flake-options.md) lists every option with its
type and default. To add spindrift to an existing flake by hand, add
`spindrift.url = "github:jordansmall/spindrift";` to your inputs and
`imports = [ spindrift.flakeModules.default ];` to your flake-parts config.
The template's [`flake.nix`](templates/default/flake.nix) is a complete
example. Pin the input in `flake.lock` so every run uses the same version.

## Before you deploy

1. **Protect the base branch.** The token that pushes `agent/issue-N` branches
   can also push to the base branch. Block direct pushes and require CI status
   checks. Don't require an approving review, because a bot can't approve its
   own PR. Without branch protection, the harness is not safe to deploy.
2. **Scope the token to one repo.** If a malicious issue injects instructions
   into the agent, a broad token lets it write to every repo the token
   reaches. See
   [GitHub token permissions](docs/reference.md#github-token-permissions).
3. **Treat issue bodies and comments as untrusted input.** Anyone who can
   comment can put text in the agent's prompt. The only gate is who can apply
   the label.

`spindrift doctor` checks your configuration, credentials, connectivity, and
labels. It exits non-zero on any fatal problem, so you can run it as a CI
preflight. It prints nothing when everything passes. Pass `-v` for the full
report. See the [threat model](docs/reference.md#threat-model).

## Commands

`spindrift --help` lists every command. These are the common ones:

| command | what it does |
| ------- | ------------ |
| `spindrift doctor` | Checks config, credentials, connectivity, and labels |
| `spindrift build` | Builds the agent image without dispatching |
| `spindrift preview` | Lists what `dispatch` would pick up, in order, without running it |
| `spindrift dispatch [N…]` | Works every `ready-for-agent` issue, or only issues `N…` |
| `spindrift research [N…]` | Reviews each `agent-research` issue and posts one verdict comment |
| `spindrift console` | Browses the backlog, dispatches picked issues, and tails or stops live Boxes |
| `spindrift recover N` | Re-runs the merge gate for one issue |
| `nix run .#daemon` | Keeps working both queues, picking up issues as they are labelled |

The CLI ships tab completion for bash, fish, and zsh. See
[Shell completion](docs/reference.md#shell-completion).

`research` never edits code or opens a PR. It posts a `recommend`, `reject`,
or `unclear` verdict and labels the issue from its own `agent-research*`
family. A human decides what to do next. See
[Research dispatch](docs/reference.md#research-dispatch). The Console has its
own page, [`docs/console.md`](docs/console.md), and the daemon is covered
under [Daemon](docs/reference.md#daemon).

## Optional behaviors

The first three are on by default. The rest are off until you enable them.
[`docs/reference.md`](docs/reference.md) covers each in full.

- **Merge guard.** The launcher never auto-merges a PR that touches
  `.github/**`, `.forgejo/**`, `**/CLAUDE.md`, `**/AGENTS.md`, `.claude/**`,
  or `.opencode/**`. See [Merge guard](docs/reference.md#merge-guard).
- **Blockers.** An issue waits until each issue blocking it reaches
  `agent-complete`. The launcher reads the tracker's own dependency links, or
  `depends on #N` in the issue body. See
  [Issue Tracker backends](docs/reference.md#issue-tracker-backends).
- **Touch-set overlap.** An issue can list the paths it expects to change
  under a `## Touches` heading. The launcher holds it back while another
  in-progress issue lists an overlapping path. See
  [Declared touch-set overlap](docs/reference.md#declared-touch-set-overlap).
- **Read-only Box.** With `forge.boxAccess = "read-only"`, the Box gets a
  read-only token. The launcher pushes the branch, opens the PR, and posts
  comments for it, so the agent has no way to merge. See
  [Read-only Box](docs/reference.md#read-only-box-box_forge_and_issue_accessread-only).
- **Two-actor separation.** Put a second machine user's token in
  `BOX_GH_TOKEN` and bar that user from the base branch with a ruleset. See
  [Two-actor separation](docs/reference.md#two-actor-separation-opt-in-hard-mode).
- **Auto-format and auto-lint.** `agents.format.enable` and
  `agents.lint.enable` run the project's formatter or linter on changed files
  before each commit. spindrift detects which tool to run.
- **Subagent roster.** `agents.models.roster` sets the model and effort for
  the scout, reviewer, worker, and filer subagents, and can add your own. When
  the filer is enabled, it files non-blocking review findings as
  `agent-review-finding` issues. See
  [Subagent roster](docs/reference.md#subagent-roster) and
  [Filer](docs/reference.md#filer).
- **Fully offline.** Set `issues.tracker = "local"` and
  `forge.backend = "local"` to work from Markdown issue files on disk with no
  network. See [Local code forge](docs/reference.md#local-code-forge-code_forgelocal).

## Documentation

| document | contents |
| -------- | -------- |
| [`docs/reference.md`](docs/reference.md) | The full manual, covering the CLI, configuration, runtime env, how a run works, labels, backends, research, the daemon, security, and macOS notes |
| [`docs/flake-options.md`](docs/flake-options.md) | Every flake option with its path, type, default, and description; generated from the source |
| [`docs/console.md`](docs/console.md) | The interactive Console |
| [`CONTEXT.md`](CONTEXT.md) | Definitions of Harness, Consumer flake, Target repo, Box, Driver, Issue Tracker, and Code Forge |
| [`SECURITY.md`](SECURITY.md) | How to report a vulnerability, and the deployment threat model |
| [`MIGRATING.md`](MIGRATING.md) | Deprecations and breaking changes |
| [`VERSIONING.md`](VERSIONING.md) | Semver policy and what stays stable across releases |
| [`CONTRIBUTING.md`](CONTRIBUTING.md) | How to work on spindrift itself |
| [`docs/adr/`](docs/adr/) | Architecture decision records |

## Credits

Heavily inspired by Matt Pocock's
[Sandcastle](https://github.com/mattpocock/sandcastle) project.

## License

MIT. See [LICENSE](LICENSE).
