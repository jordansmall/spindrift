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
- **podman** (or set `runtime = "docker"`; or `runtime = "bwrap"` for the
  daemonless bubblewrap sandbox on Linux, which needs no container runtime).
- A **fine-grained single-repo GitHub PAT** — scoped to the Target repo only
  (see [Before you deploy](#before-you-deploy)).
- **Claude Code auth**: run `claude setup-token` on the host, or an API key.

## Quick start

Scaffold a Consumer flake from the bundled template:

```sh
mkdir my-agents && cd my-agents
nix flake init -t github:jordansmall/spindrift
```

That drops a ready-to-edit starter: a `flake.nix` importing the harness, a
`prompts/` directory, a `harness.env.example`, and a `.gitignore` for
`harness.env`. Then:

```sh
$EDITOR flake.nix                        # tune the toolchain/packages for your stack
$EDITOR prompts/issue-prompt.md          # tune the agent's workflow
cp harness.env.example harness.env       # fill in REPO_SLUG, GH_TOKEN, Claude auth
nix develop                              # enter the dev shell — puts spindrift on PATH
spindrift build                          # realize the image, then load it  (slow first time)
spindrift dispatch                       # launch one container per ready-for-agent issue
```

Run commands **from your Consumer flake's directory**: `spindrift build` reads the
flake from `$PWD` for its container fallback, and `spindrift dispatch` reads `harness.env`
from `$PWD` (the same convention). Per-issue logs land in `logs/issue-<n>.log`.

`spindrift` ships bash tab-completion, generated from the same schema as
`--help` and the man page: subcommands (`dispatch`, `preview`, `build`,
`recover`, `doctor`) complete as the first word, every flag (including the
`--issue` alias and the secret `--*-file` flags) completes anywhere after
it, and a `--*-file` flag's argument completes as a filesystem path. `nix
develop` puts the completion script on `share/bash-completion/completions`
under `spindrift`'s store path; source it directly to enable it in your
shell:

```sh
source "$(dirname "$(command -v spindrift)")/../share/bash-completion/completions/spindrift"
```

`spindrift` also ships fish tab-completion, with the same coverage as the
bash slice plus a one-line description on every flag. `nix develop` puts the
completion script on `share/fish/vendor_completions.d` under `spindrift`'s
store path; fish's `vendor_completions.d` convention loads it automatically
once that directory is on `$fish_complete_path`, or copy/symlink it into
`~/.config/fish/completions/spindrift.fish`.

It also ships zsh tab-completion with the same coverage, plus a per-flag
description drawn from the same `doc` string, so `spindrift --<TAB>` shows
each flag's one-line purpose alongside its name. `nix develop` puts the
completion function on `share/zsh/site-functions/_spindrift` under
`spindrift`'s store path; add that directory to `fpath` before `compinit`
runs to enable it:

```sh
fpath=("$(dirname "$(command -v spindrift)")/../share/zsh/site-functions" $fpath)
autoload -Uz compinit && compinit
```

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
spindrift.devShellName = "ci";
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
validity, and that all four triage labels exist on the Target repo. When run
interactively (TTY attached) and labels are missing, it offers to create them;
in CI (no TTY) it reports missing labels and exits non-zero.

## Basic flow

```
spindrift dispatch  ─▶  find ready-for-agent issues
                          └─ one container per issue (up to MAX_PARALLEL)
                               clone repo → run claude → commit → push → open PR
                               └─ SPINDRIFT_OUTCOME issue=N pr=<url> status=ready

host launcher  ─▶  merge gate per issue
                    poll CI → green → agent-complete → merge guard → apply MERGE_MODE
                           → red   → fix boxes (up to MAX_FIX_ATTEMPTS) → re-gate
                           → exhausted → agent-failed (human triage, re-label to retry)
```

The Box implements; the launcher owns the CI-green decision and the merge. A Box
cannot approve or merge its own PR — that is what makes branch protection
meaningful. See [How a run works](docs/reference.md#how-a-run-works) for the full
diagram and label lifecycle.

A green PR whose diff touches a guarded path (`.github/**`, `**/CLAUDE.md`,
`**/AGENTS.md`, `.claude/**`, `.opencode/**` by default) is downgraded to
manual regardless of `MERGE_MODE` — see
[Merge guard](docs/reference.md#merge-guard).

Set `FILER_MODEL` to opt in an optional Filer subagent that turns a review's
non-blocking findings into `agent-review-finding`-labelled issues instead of
leaving them in the PR body — never the dispatch label, so a human still
promotes each one before an agent can pick it up. Off (empty) by default. See
[Filer](docs/reference.md#filer).

Set `AUTO_FORMAT=1` (or `settings.promptSkillIteration.autoFormat = true` in
your Consumer flake) to have the implementor auto-format changed files before
each commit. The formatter is detected automatically: a `format`/`fmt` script
in `package.json`, `Makefile`, or `justfile`, otherwise the language's standard
formatter. Never `nix fmt` — evaluating the flake in-box would copy the dirty
work tree into `/nix/store`, which the agent user cannot write to. Runs only
on changed files; skips silently when none is found. Off by default.

Set `AUTO_LINT=1` (or `settings.promptSkillIteration.autoLint = true` in your
Consumer flake) to have the implementor lint changed files before each commit,
applying the linter's auto-fix mode and then manually resolving any remaining
findings. The linter is detected automatically: a `lint` target in
`package.json`, `Makefile`, or `justfile`, or the language's standard linter
(e.g. `eslint`, `ruff`, `golangci-lint`, `clippy`, `statix`). Runs only on
changed files; skips silently when none is found. Off by default.

An issue may also declare a `## Touches` section listing the paths it expects
to change; dispatch defers it while its touch-set overlaps an already
in-progress issue's, retrying once the collider completes — see [Declared
touch-set overlap](docs/reference.md#declared-touch-set-overlap).

## Dogfood loop

`dogfood.sh` drives spindrift building itself, with `CONTINUOUS_DISPATCH=1`
on by default (#528): instead of draining one bounded batch and returning,
the launcher runs a long-lived slot-refill loop — as each Box finishes, it
re-discovers the queue and refills the freed slot immediately, re-applying
blocker readiness, the Touches overlap gate, and blocker-failed cascade —
gated by the image-freshness probe (#526) before every launch. An operator
can still set `CONTINUOUS_DISPATCH=` (empty) in `harness.env` to fall back to
the older one-wave-and-exit shape (#527).

The freshness boundary is no longer every iteration: a refill launches
straight onto the already-loaded image so long as it's still fresh, and
`dogfood.sh` only pulls and rebuilds when the launcher reports the image has
actually gone stale (build is a no-op unless the merged diff changed the
image hash).

**Parallel by default.** `MAX_JOBS` defaults to `MAX_PARALLEL` (default 3),
so the slot pool holds that many Boxes at once. Set `MAX_JOBS` explicitly to
run a larger or unbounded pool.

**Termination.** The loop is driven entirely by the launcher's exit code:

| exit | meaning | loop action |
|------|---------|-------------|
| 0    | dispatched work | pull + rebuild, then continue |
| 2    | queue empty (no open issues with the dispatch label) | exit cleanly |
| 3    | open issues exist but none are dispatchable | stop and print a triage message — typically a failed blocker needs re-labeling before the queue can drain |
| 4    | `CONTINUOUS_DISPATCH` mode: the image-freshness probe found the loaded image would be rebuilt against the current base-branch tip; in-flight Boxes finished, no new ones launched | pull + rebuild, then re-invoke — the same boundary exit 0 runs |

Set `CONTINUOUS_DISPATCH=1` to opt into the slot-refill dispatch mode (#527)
in a driving loop other than `dogfood.sh`; see `lib/env-schema.nix`'s
`continuousDispatch` entry for the full behavior.

**Baked skill, on by default.** The dogfood Box bakes the pinned upstream
[`caveman` skill](https://github.com/juliusbrussee/caveman), advertised
in-box as `/caveman`, and the rendered issue-pass and fix-pass prompts
direct the agent to default to it for narration and prose — so agents
draining this loop compress narration ~65% in output tokens without
touching code, commands, error messages, or commit messages. The pin is a
non-flake `caveman` input in `flake.nix` (`flake.lock` owns the rev); see
[Contributing](CONTRIBUTING.md) for how it's wired.

To opt out, don't bake the skill: drop `caveman` from the consumer's
`skills` list (see `nix/dogfood-skills.nix`). The instruction is rendered
only when `caveman.md` is actually present at the baked skills path, so a
consumer that skips it gets prompts with zero caveman residue.

## Documentation

| document | what's in it |
| -------- | ------------ |
| [`docs/reference.md`](docs/reference.md) | Full CLI table, all configuration options, runtime env vars, how a run works, label lifecycle, security model, macOS build notes, design notes |
| [`docs/flake-options.md`](docs/flake-options.md) | Schema-generated reference for every `settings.<section>.<knob>` — attr path, env var, default, description; regenerated by `nix flake check` |
| [`CONTEXT.md`](CONTEXT.md) | Vocabulary — Harness, Consumer flake, Target repo, Box, Forge, and the three roles |
| [`MIGRATING.md`](MIGRATING.md) | Deprecated commands and breaking changes by version |
| [`VERSIONING.md`](VERSIONING.md) | Semver policy and the stability guarantees for each surface |
| [`CONTRIBUTING.md`](CONTRIBUTING.md) | Dev workflow (`nix flake check`, `nix run .#regen`), where code goes, ADRs, commit/release conventions |
| [`SECURITY.md`](SECURITY.md) | How to report a vulnerability privately, plus the deployment threat model |

## Credits

Heavily inspired by Matt Pocock's
[Sandcastle](https://github.com/mattpocock/sandcastle) project.

## License

MIT — see [LICENSE](LICENSE).
