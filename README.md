# spindrift

[![CI](https://github.com/jordansmall/spindrift/actions/workflows/ci.yml/badge.svg)](https://github.com/jordansmall/spindrift/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/jordansmall/spindrift)](https://github.com/jordansmall/spindrift/releases/latest)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

*A nix-based agent automation harness, consumed as a flake.*

Run headless [Claude Code](https://claude.com/claude-code) agents in
**disposable, nix-built containers** — one per GitHub issue. spindrift is
**imported by your flake**, not cloned. Two ideas carry it (see
[`CONTEXT.md`](CONTEXT.md) for the full vocabulary):

1. **The container is the isolation boundary.** Each issue runs in its own
   throwaway container with a fresh clone, a scoped token, and no host access.
   That is what makes `claude --dangerously-skip-permissions` safe: the agent
   can do anything it likes, but only inside the box.
2. **The toolchain is a nix image.** The image is built with `dockerTools` from
   the *same* pinned nixpkgs your dev shell uses, so the agent's environment and
   yours can never drift. One source of truth, no hand-maintained Dockerfile.

## Prerequisites

- **nix** with flakes enabled.
- **podman** (or set `runtime = "docker"`; `runtime = "rancher"` for Rancher
  Desktop in containerd mode, driven via `nerdctl`; or `runtime = "bwrap"` for
  the daemonless bubblewrap sandbox on Linux, which needs no container runtime).
  On macOS/Windows, podman runs containers inside a VM with its own fixed RAM —
  size it to at least `MEMORY_LIMIT` × `MAX_PARALLEL` plus VM overhead. See
  [Dogfood loop](docs/reference.md#dogfood-loop).
- A **fine-grained single-repo GitHub PAT** — scoped to the Target repo only
  (see [Before you deploy](#before-you-deploy)).
- **Claude Code auth**: run `claude setup-token` on the host, or an API key.

## Quick start

Try the CLI with no clone and no dev shell — `nix run` builds it from this
flake's own pinned toolchain and runs it:

```sh
nix run github:jordansmall/spindrift -- --help
nix run github:jordansmall/spindrift -- --version
```

The fastest path from zero to a dispatching setup is the **Quickstart
wizard** — an interactive nix app that scaffolds, validates, and builds a
Consumer flake in one command:

```sh
mkdir my-agents && cd my-agents
nix run github:jordansmall/spindrift#quickstart
```

It detects what it can (container runtime, git identity, repo slug from `git
remote`, an ambient token) and asks only for the rest — a GitHub token
(audited for over-broad scopes) and Claude auth. Quickstart always
provisions **GitHub** as the Issue Tracker; it never asks. The `jira` and
`local` trackers are experimental and reachable only by hand-editing
`ISSUE_TRACKER` in the generated `flake.nix` — see [Issue Tracker
backends](docs/reference.md#issue-tracker-backends). It then writes a
minimal `flake.nix`, a secrets-only `harness.env`, a `.gitignore` protecting
it, and an `.envrc`; runs the doctor checks (offering to create missing
labels); and kicks off the first image build — leaving `spindrift dispatch`
as the only remaining step. Refuses to clobber an existing flake without
`--force`. See [Quickstart](docs/reference.md#quickstart) for the full flow.

Prefer a fully-commented scaffold you edit by hand instead? Use the bundled
template:

```sh
mkdir my-agents && cd my-agents
nix flake init -t github:jordansmall/spindrift
```

That drops a ready-to-edit starter: a `flake.nix` importing the harness (with
a commented `settings.repository.repoSlug` you uncomment and fill in), a
`prompts/` directory, a `harness.env.example` (secrets only — see
[Runtime configuration](docs/reference.md#runtime-configuration)), a
`.gitignore` for `harness.env`, and an `.envrc` (`use flake` — direnv users
get the dev shell on `cd`). Then:

```sh
$EDITOR flake.nix                        # set settings.repository.repoSlug; tune toolchain/packages
$EDITOR prompts/issue-prompt.md          # tune the agent's workflow
cp harness.env.example harness.env       # fill in GH_TOKEN, Claude auth (secrets only)

nix run github:jordansmall/spindrift -- build      # realize the image, then load it  (slow first time)
nix run github:jordansmall/spindrift -- preview    # dry run: show what dispatch would pick up, and in what order
nix run github:jordansmall/spindrift -- dispatch   # one container per ready-for-agent issue
nix run github:jordansmall/spindrift -- research   # advise-only: one container per agent-research issue
```

Every verb is a `nix run github:jordansmall/spindrift -- <verb>` away: the
binary comes from this flake, while the Consumer flake, `harness.env`, and
per-issue `logs/` are read from `$PWD`. The unpinned `github:` ref tracks
`main`; pin spindrift in your own `flake.lock` (see [Adding spindrift to your
flake](#adding-spindrift-to-your-flake)) for a fixed, reproducible version.

Prefer a persistent shell with `spindrift` on `PATH`? `nix develop` puts it
there, along with tab-completion and the `dogfood-stop` alias:

```sh
nix develop                              # enter the dev shell — puts spindrift on PATH
spindrift build                          # the same verbs, now as a bare command
spindrift dispatch
```

Run commands **from your Consumer flake's directory**: `spindrift build` reads
the flake from `$PWD` for its container fallback, and `spindrift dispatch` reads
`harness.env` from `$PWD` for secrets. Per-issue logs land in
`logs/issue-<n>.log`. The full verb table — including `console` (the
interactive Console, below), `preview`, and `recover <issue>` (re-run the
merge gate for one stranded issue) — is in
[the CLI reference](docs/reference.md#the-spindrift-cli).

`spindrift` ships bash, fish, and zsh tab-completion — subcommands, every flag,
`--*-file` path arguments, and enumerable flag values — generated from the same
schema as `--help`. See [Shell completion](docs/reference.md#shell-completion)
to enable it in your shell.

## Adding spindrift to your flake

If you prefer to wire it by hand rather than `nix flake init`, add spindrift to
your inputs and import the flake-parts module:

```nix
{
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-parts.url = "github:hercules-ci/flake-parts";
    spindrift.url = "github:jordansmall/spindrift";
  };

  outputs = inputs@{ flake-parts, spindrift, ... }:
    flake-parts.lib.mkFlake { inherit inputs; } {
      systems = [ "aarch64-darwin" "aarch64-linux" "x86_64-linux" ];
      imports = [ spindrift.flakeModules.default ];
      perSystem = { config, pkgs, ... }: {
        spindrift = {
          # The Target repo's devShell is used by default — no packages
          # needed for a pure devShell setup. Add packages here only as
          # a speed optimization to pre-bake toolchain closures into the
          # image (see docs/reference.md for the full rationale).
          packages = p: [ p.go p.gnumake ];
          prompt = builtins.readFile ./prompts/issue-prompt.md;
        };

        # Put the spindrift CLI on PATH: `nix develop` → `spindrift dispatch`.
        devShells.default = pkgs.mkShell {
          packages = [ config.packages.spindrift ];
        };
      };
    };
}
```

This yields the **`spindrift` CLI** as `packages.<system>.spindrift` and as
`apps.<system>.default`, plus the Linux-only `agent-image`. The bare form
(`nix run .`) prints help and exits; drain the queue with
`nix run . -- dispatch`. See [`docs/reference.md`](docs/reference.md) for the
`mkHarness`-direct variant and the devShell-targeting pattern (one image,
many differently-toolchained Target repos).

**Lean CI shell.** If the Target repo exposes a `devShells.ci` with only the
tools the agent needs (no LSP, no GUI tooling), set `DEV_SHELL_NAME=ci` — or
bake it as the Consumer default:

```nix
spindrift.settings.sandbox.devShellName = "ci";
```

The Box enters that shell instead of `devShells.default`, giving the agent a
smaller closure and a faster probe. See
[devShell targeting](docs/reference.md#how-a-run-works) for the probe flow
and baked-toolchain fallback.

## Before you deploy

Three non-negotiables before pointing the harness at a live repo:

1. **Branch protection is required.** The token has Contents RW to push
   `agent/issue-N` branches — that same scope allows pushing directly to the base
   branch. Without branch protection **the harness is not safe to deploy**. Block
   direct pushes; require CI status checks; do not require an external approving
   review (a bot cannot approve its own PR). See the full rationale in
   [Security → Threat model](docs/reference.md#threat-model).
2. **Use a fine-grained single-repo PAT.** A broadly-scoped token gives an
   injected agent write access to every repo it reaches. Restrict to one Target
   repo (Issues RW, Contents RW, Pull requests RW, Metadata R). See the
   [token permission table](docs/reference.md#github-token-permissions).
3. **Issue body and every comment are attacker-writable prompt input.** The
   trust boundary is the label, not the issue or comment author. What bounds
   the blast radius is what the token allows and nothing more.

Run `spindrift doctor` as a preflight: it checks forge connectivity, token
validity, and label presence (the four triage labels and the six
`agent-research*` labels). Run interactively, it offers to create missing
labels; in CI it exits non-zero only when a triage label is missing.

## Basic flow

```
spindrift dispatch  ─▶  find ready-for-agent issues
                          └─ one container per issue (up to MAX_PARALLEL)
                               clone repo → run claude → commit → push → open PR
                               └─ SPINDRIFT_OUTCOME issue=N landing=<url> status=ready

host launcher  ─▶  merge gate per issue
                    poll CI → green → merge guard → apply MERGE_MODE → agent-complete
                           → red   → fix boxes (up to MAX_FIX_ATTEMPTS) → re-gate
                           → exhausted → agent-failed (human triage, re-label to retry)
```

The Box implements; the launcher owns the CI-green decision and the merge. A Box
cannot approve or merge its own PR — that is what makes branch protection
meaningful. See [How a run works](docs/reference.md#how-a-run-works) for the full
diagram and label lifecycle.

Optional behaviors, each off unless noted (see [`docs/reference.md`](docs/reference.md)
for configuration):

- **Merge guard.** A green PR whose diff touches a guarded path (`.github/**`,
  `**/CLAUDE.md`, `**/AGENTS.md`, `.claude/**`, `.opencode/**` by default) is
  downgraded to manual regardless of `MERGE_MODE` — see
  [Merge guard](docs/reference.md#merge-guard).
- **Filer.** Set `settings.models.filerModel` to file the non-blocking review
  findings the work loop escalates into `agent-review-finding`-labelled issues
  for human triage — see [Filer](docs/reference.md#filer).
- **Auto-format / auto-lint.** Set `settings.promptSkillIteration.autoFormat` /
  `.autoLint` (or pass `--auto-format` / `--auto-lint`) to format or lint
  changed files before each commit; the tool is detected automatically.
- **Blockers.** An issue's blockers gate its dispatch until each reaches
  `agent-complete`, resolved from the tracker's native dependency relationships
  first and body-text refs (`depends on #N`) as a fallback — see
  [Issue Tracker backends](docs/reference.md#issue-tracker-backends).
- **Touch-set overlap.** An issue's `## Touches` section defers dispatch while
  its paths overlap an in-progress issue's, retrying once the collider completes
  — see [Declared touch-set overlap](docs/reference.md#declared-touch-set-overlap).

## Research dispatch

`spindrift research` (and the selective `research <nums>` form) is a second,
advise-only Dispatch kind: each container reviews one `agent-research` issue
from inside a fresh clone of the Target repo, then posts a single structured
verdict comment. It never edits the issue body, closes it, or promotes it to
`ready-for-agent` — a human always acts on the verdict.

```
spindrift research  ─▶  find agent-research issues
                          └─ one container per issue
                               clone repo → review issue → post verdict comment
                               └─ SPINDRIFT_OUTCOME issue=N landing=<comment-url> status=recommend|reject|unclear|blocked
```

Research maps the launcher's four Dispatch states to its own disjoint
`agent-research*` label family, so an issue can wear a work label and a research
label at once. On GitHub, `.github/workflows/agent-research.yml` fires one
research dispatch per `agent-research` application. See
[Research dispatch](docs/reference.md#research-dispatch) for the label table,
the workflow, and the optional least-privilege research token.

## Console

`spindrift console` opens the interactive Console: an in-terminal loop that
lists every open issue from the Issue Tracker — number, title, labels,
oldest-first — and lets you Pick issues to launch as Dispatches.

```sh
spindrift console
```

Picks launch through the same continuous engine the headless loops use, up to a
live parallelism cap you can resize in-session. You can filter the backlog,
drill into a running Dispatch's rendered transcript, terminate a live Dispatch
by hand, rebuild a stale image without leaving the session, and adopt orphaned
containers left by a crash. See [`docs/console.md`](docs/console.md) for the
full command table and behavior.

## Documentation

| document | what's in it |
| -------- | ------------ |
| [`docs/reference.md`](docs/reference.md) | Full CLI table, the Quickstart wizard, all configuration options, runtime env vars, how a run works, label lifecycle, research dispatch, dogfood loop, shell completion, security model, macOS build notes, design notes |
| [`docs/console.md`](docs/console.md) | The interactive Console — every command and its behavior |
| [`docs/flake-options.md`](docs/flake-options.md) | Schema-generated reference for every `settings.<section>.<knob>` — attr path, env var, default, description; regenerated by `nix flake check` |
| [`CONTEXT.md`](CONTEXT.md) | Vocabulary — Harness, Consumer flake, Target repo, Box, Forge, and the three roles |
| [`MIGRATING.md`](MIGRATING.md) | Deprecated commands and breaking changes by version |
| [`VERSIONING.md`](VERSIONING.md) | Semver policy and the stability guarantees for each surface |
| [`CONTRIBUTING.md`](CONTRIBUTING.md) | Dev workflow (`nix flake check`, the scoped in-box `checks-inbox` target, `nix run .#regen`), where code goes, ADRs, commit/release conventions |
| [`SECURITY.md`](SECURITY.md) | How to report a vulnerability privately, plus the deployment threat model |

## Credits

Heavily inspired by Matt Pocock's
[Sandcastle](https://github.com/mattpocock/sandcastle) project.

## License

MIT — see [LICENSE](LICENSE).
