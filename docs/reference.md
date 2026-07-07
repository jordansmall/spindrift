# spindrift reference

Full technical reference for the spindrift harness. For first-time setup see
the [README](../README.md); for vocabulary see [`CONTEXT.md`](../CONTEXT.md).

---

## The `spindrift` CLI

`nix develop` (or `direnv allow`) puts a single `spindrift` binary on your PATH
— it is the primary surface, and everything runs through it:

| command                          | what it does                                                                    |
| -------------------------------- | ------------------------------------------------------------------------------- |
| `spindrift dispatch`             | fan out one container per `ready-for-agent` issue, in dependency waves          |
| `spindrift dispatch 42 57`       | dispatch exactly these issues, bypassing the label/barrier gates                |
| `spindrift dispatch --no-build`  | fail fast if the image is absent instead of building it first (split build/run) |
| `spindrift dispatch --yes`       | skip the confirmation prompt when dispatching unlabeled issues (alias `--force`)|
| `spindrift preview [issue...]`   | dry run: show what `dispatch` would pick up, and the wave ordering               |
| `spindrift build`                | realise/load the agent image (or store closures) without running any agent      |
| `spindrift recover <issue>`      | re-run the merge gate for one issue (adopt a stranded `agent-in-progress`)       |
| `spindrift doctor`               | check forge credentials, repository connectivity, and triage label presence; when run interactively (TTY attached) and labels are missing it offers to create them with default colors and descriptions; in CI (no TTY) it reports the missing labels and exits non-zero without prompting |
| `spindrift --help`               | concise usage: subcommands, common flags, and pointers to the full reference    |
| `spindrift --help --all`         | the full flag reference, grouped by category (same content as `man spindrift`)  |
| `man spindrift`                  | the manual page (installed alongside the binary on your PATH)                    |
| `spindrift --version`            | installed version and revision                                                  |

Every runtime knob is also a `--flag`, with **flag > env > default** precedence.
`spindrift --help` stays scannable; the full generated table lives in
`man spindrift` (and `spindrift --help --all` for the same thing in the
terminal). Bare `spindrift` with no subcommand is `spindrift dispatch`.

> **Deprecated (removed in v0.2.0, see [`MIGRATING.md`](../MIGRATING.md)):**
> `nix run .#run` and `nix run .#build` still work but print a notice and forward
> to `spindrift dispatch` / `spindrift build`. `spindrift engage <issue>` is a
> deprecated alias for `spindrift recover <issue>`.

If you use **direnv**, the template's `.envrc` (`use flake`) activates the dev
shell automatically on `cd` — no manual `nix develop` needed.

`spindrift build` **realises** the image derivation and then loads it into your
container runtime. On a host with a Linux builder (any Linux machine, or a Mac
with a Linux builder configured) it realises the image directly. On a stock Mac
— no Linux builder — it transparently falls back to building the image inside an
**ephemeral Nix container** on the same runtime it already requires, keeping a
named `/nix` volume so rebuilds stay incremental. Either way the result is
`spindrift:latest`, loaded and ready for `spindrift dispatch`. If the host has
neither a Linux builder nor a container runtime, `spindrift build` exits with
instructions.

---

## Configuring the harness

### Option surface

Both `mkHarness` and the `perSystem.spindrift.*` module options take the same
knobs. Unset options fall through to `mkHarness`'s own defaults.

| option      | type                        | default            | meaning                                                              |
| ----------- | --------------------------- | ------------------ | -------------------------------------------------------------------- |
| `nixpkgs`   | flake input                 | your `nixpkgs`     | locked nixpkgs the image and host commands build from                |
| `system`    | string                      | perSystem's system | your host system; mapped to its Linux twin for the image            |
| `overlays`  | list                        | `[]`               | overlays applied to the instantiated nixpkgs                         |
| `config`    | attrs                       | `{ allowUnfree = true; }` | nixpkgs config attrs                                          |
| `packages`  | `pkgs -> [pkg]`             | `[]`               | project build/test tools baked into the image (the toolchain surface)|
| `prefetch`  | shell snippet               | `""`               | runs in the work tree after the clone, to warm dependency caches     |
| `prompt`    | string                      | bundled starter    | agent prompt template baked into the image; changing it requires a rebuild (`spindrift build`) |
| `scoutPrompt` / `reviewPrompt` | string           | bundled starters   | system prompts for the read-only scout and reviewer subagents; baked in, overridable via `SPINDRIFT_PROMPT_DIR` |
| `skills`    | list of paths               | `[]`               | skill files baked into the image at `/home/agent/.claude/skills` so the headless agent can `/invoke` them; `SPINDRIFT_SKILLS_DIR` mounts over them at runtime |
| `defaults`  | submodule (all `flakeOption` env knobs) | see below | non-secret run defaults baked into the `spindrift` CLI |
| `runtime`   | `"podman"` \| `"docker"` \| `"bwrap"` | `"podman"` | runner the `spindrift build`/`dispatch` commands drive: an OCI runtime, or the daemonless bubblewrap sandbox (`bwrap`, Linux-only, no image build/load) |
| `nixInBox`  | bool                        | `true`             | bake a usable nix (binary + registered store DB + sandbox-off config) into the box so `nix flake check` / `nix develop` work inside it; set `false` for a lean, nix-free image (ADR 0008) |
| `nixBuilderImage` | string                | `"docker.io/nixos/nix@sha256:bf1d938835ab96312f098fa6c2e9cab367728e0aad0646ee3e02a787c80d8fb8"` | Nix image `spindrift build` uses as a fallback Linux builder when the host can't realise the image; pinned by digest for supply-chain safety (see [Building on macOS](#building-on-macos)) |

The `defaults` submodule bakes the run knobs into the `spindrift` CLI; a matching
env var still wins at runtime, so one built command can be re-pointed without a
rebuild. It exposes every consumer-tunable knob from `lib/env-schema.nix` (the
single source of truth for the runtime env surface). Key baked defaults:
`label = "ready-for-agent"`, `baseBranch = "main"`, `maxParallel = 3`,
`branchPrefix = "agent/issue-"`, `inProgressLabel = "agent-in-progress"`,
`failedLabel = "agent-failed"`, `completeLabel = "agent-complete"`,
`mergeMode = "manual"`, `model = "claude-sonnet-4-6"`,
`scoutModel = "claude-haiku-4-5-20251001"`, `reviewModel = "claude-opus-4-8"`.
`inProgressLabel`/`failedLabel`/`completeLabel` drive the
[label lifecycle](#how-a-run-works); `mergeMode` is the post-green
[merge policy](#how-a-run-works) (`manual`/`immediate`/`auto`); `model` is the
Claude model the in-container implementor agent runs, threaded into the container
as `MODEL` so `MODEL=...` switches models at runtime with no image rebuild.
`scoutModel`/`reviewModel` tier the read-only scout and reviewer subagents the
same way; setting either to `""` drops both subagents from the `claude`
invocation.

The **prompt is baked into the image**: changing `prompts/issue-prompt.md`
requires an image rebuild (`spindrift build`). Point `SPINDRIFT_PROMPT_DIR`
at any directory to override it at runtime for zero-rebuild iteration.

### Calling `mkHarness` directly

The flake-parts module is a thin shim over the engine. Any flake — flake-parts
or not — can call the function itself:

```nix
spindrift.lib.mkHarness {
  inherit (nixpkgs) ...;      # pass your locked nixpkgs input + system
  nixpkgs = inputs.nixpkgs;
  system = "aarch64-darwin";
  packages = p: [ p.go ];
}
# => { image, spindrift, build, run, packages, apps, imagePath, promptDir, skillsDir, ... }
# packages.spindrift is the CLI; add it to a devShell so `spindrift` is on PATH.
```

`mkHarness` takes the locked *nixpkgs input* (not a pre-built `pkgs`) so it can
map a darwin `system` to its Linux twin and re-instantiate for the OCI image —
keeping the agent's toolchain and your dev shell from one pin (ADR 0002).

### Targeting repos that define their own devShell toolchain

If the Target repos define their own build toolchain in a `flake.nix` devShell,
the Consumer can keep `packages` minimal and let each Target's devShell drive
checks at runtime — one Consumer image serves many differently-toolchained
Target repos.

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
      perSystem = _: {
        spindrift = {
          # Minimal toolchain — shared utilities only.
          # The Target repo's own devShell drives the real build/test commands.
          packages = p: [ p.git p.gh p.gnused ];

          prompt = builtins.readFile ./prompts/issue-prompt.md;

          # nixInBox is true by default — nix is already in the box, no
          # override needed for the devShell probe to work.
        };
      };
    };
}
```

**How this works.** The Consumer image is built once from the Consumer's locked
nixpkgs and is Target-agnostic: the Target repo's `flake.nix` is never evaluated
at image-build time (ADR 0001, ADR 0002). After the Target repo is cloned inside
the box, the entrypoint probes for a devShell:

1. If `flake.nix` is present in the cloned repo and `nix` is on PATH (it is,
   because `nixInBox = true` is the default per ADR 0008), the entrypoint runs
   `nix develop --command true` under a `DEV_SHELL_PROBE_TIMEOUT`-second timeout
   (default 300 s, baked from `lib/env-schema.nix`).
2. If the probe succeeds, the agent is guided to run checks via
   `nix develop -c <cmd>`, using the Target's exact toolchain.
3. If `flake.nix` is absent, `nix` is unavailable, the probe fails, or the
   timeout fires, the box degrades gracefully to the baked toolchain.

**`nixInBox` interplay.** The probe requires a working `nix` CLI inside the box,
which `nixInBox = true` (the default, [ADR
0008](adr/0008-nix-is-a-first-class-default-in-the-box.md)) provides.
Setting `nixInBox = false` (see [option surface](#option-surface)) produces a
lean, nix-free image — there is no in-box `nix`, so the devShell probe is
skipped and only the baked `packages` toolchain is available. **The devShell
pattern requires `nixInBox = true`.**

**Practical upshot.** Keeping `packages` to a minimal set (`git`, `gh`, and the
like) means the Consumer image stays small and works across Target repos with
different language stacks. The `DEV_SHELL_PROBE_TIMEOUT` guard (default 300 s,
overridable at runtime via env var) ensures a broken or unusually heavy devShell
eval cannot stall the box. See the `nixInBox` [option](#option-surface) and
[ADR 0008](adr/0008-nix-is-a-first-class-default-in-the-box.md) for the full
rationale and the lean-image escape hatch.
