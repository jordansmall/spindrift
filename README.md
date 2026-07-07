# spindrift

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

*A nix-based agent automation harness, consumed as a flake.*

Run headless [Claude Code](https://claude.com/claude-code) agents in
**disposable, nix-built containers** — one per GitHub issue. spindrift is
**imported by your flake**, not cloned. Two ideas carry it:

1. **The container is the isolation boundary.** Each issue runs in its own
   throwaway container with a fresh clone, a scoped token, and no host access.
   That is what makes `claude --dangerously-skip-permissions` safe: the agent
   can do anything it likes, but only inside the box.
2. **The toolchain is a nix image.** The image is built with `dockerTools` from
   the *same* pinned nixpkgs your dev shell uses, so the agent's environment and
   yours can never drift. One source of truth, no hand-maintained Dockerfile.

## The three roles

spindrift separates three roles (see `CONTEXT.md` for the full glossary):

- **Harness** — spindrift itself: the engine (`lib.mkHarness`), the
  flake-parts shim (`flakeModules.default`), and the in-container entrypoint.
  The thing you import.
- **Consumer flake** — *your* flake. It imports the Harness and configures the
  toolchain, packages, prompt, and run defaults. It produces the `spindrift`
  CLI and the image.
- **Target repo** — the GitHub repo whose issues the agents work, set by
  `REPO_SLUG` at runtime. Always cloned fresh inside the container, never read
  from a host checkout — so it is a distinct role even when it is the same repo
  as the Consumer flake.

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
spindrift build                          # realise the image, then load it  (slow first time)
spindrift dispatch                       # fan out one container per ready-for-agent issue
```

Run commands **from your Consumer flake's directory**: `spindrift build` reads the
flake from `$PWD` for its container fallback, and `spindrift dispatch` reads `harness.env`
from `$PWD` (the same convention). Per-issue logs land in `logs/issue-<n>.log`.

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
      systems = [ "aarch64-darwin" "x86_64-darwin" "aarch64-linux" "x86_64-linux" ];
      imports = [ spindrift.flakeModules.default ];
      perSystem = { config, pkgs, ... }: {
        spindrift = {
          # bake your build/test toolchain into the image (a fn of the Linux pkgs)
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
`apps.<system>.default` (so `nix run .` == `spindrift dispatch`), plus the
Linux-only `agent-image`. See [`docs/reference.md`](docs/reference.md) for the
`mkHarness`-direct variant and the devShell-targeting pattern (one image,
many differently-toolchained Target repos).

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
                    poll CI → green → agent-complete → apply MERGE_MODE
                           → red   → fix boxes (up to MAX_FIX_ATTEMPTS) → re-gate
                           → exhausted → agent-failed (human triage, re-label to retry)
```

The Box implements; the launcher owns the CI-green decision and the merge. A Box
cannot approve or merge its own PR — that is what makes branch protection
meaningful. See [How a run works](docs/reference.md#how-a-run-works) for the full
diagram and label lifecycle.

## Credits

Heavily inspired by Matt Pocock's
[Sandcastle](https://github.com/mattpocock/sandcastle) project.

## License

MIT — see [LICENSE](LICENSE).
