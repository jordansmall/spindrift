# spindrift

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
  toolchain, packages, prompt, and run defaults. It produces the `build`/`run`
  commands and the image.
- **Target repo** — the GitHub repo whose issues the agents work, set by
  `REPO_SLUG` at runtime. Always cloned fresh inside the container, never read
  from a host checkout — so it is a distinct role even when it is the same repo
  as the Consumer flake.

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
nix run .#build                          # realise the image, then load it  (slow first time)
nix run .#run                            # fan out one container per ready-for-agent issue
```

Run both commands **from your Consumer flake's directory**: `build` reads the
flake from `$PWD` for its container fallback, and `run` reads `harness.env` from
`$PWD` (the same convention). Per-issue logs land in `logs/issue-<n>.log`.
`.#build`/`.#run` are also exposed as `packages`, so you can drop them into
`devShells.default.packages` instead of using `nix run`.

`build` **realises** the image derivation and then loads it into your container
runtime. On a host with a Linux builder (any Linux machine, or a Mac with a
Linux builder configured) it realises the image directly. On a stock Mac — no
Linux builder — it transparently falls back to building the image inside an
**ephemeral Nix container** on the same runtime it already requires, keeping a
named `/nix` volume so rebuilds stay incremental. Either way the result is
`spindrift:latest`, loaded and ready for `run`. If the host has neither a Linux
builder nor a container runtime, `build` exits with instructions.

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
      perSystem = _: {
        spindrift = {
          # bake your build/test toolchain into the image (a fn of the Linux pkgs)
          packages = p: [ p.go p.gnumake ];
          prompt = builtins.readFile ./prompts/issue-prompt.md;
        };
      };
    };
}
```

This yields `packages.<system>.{build,run}` (plus the Linux-only `spindrift`
image) and matching `apps.<system>.{build,run}`.

### Or call `mkHarness` directly

The module is a thin shim over the engine. Any flake — flake-parts or not — can
call the function itself:

```nix
spindrift.lib.mkHarness {
  inherit (nixpkgs) ...;      # pass your locked nixpkgs input + system
  nixpkgs = inputs.nixpkgs;
  system = "aarch64-darwin";
  packages = p: [ p.go ];
}
# => { image, build, run, packages, apps, imagePath, promptDir, ... }
```

`mkHarness` takes the locked *nixpkgs input* (not a pre-built `pkgs`) so it can
map a darwin `system` to its Linux twin and re-instantiate for the OCI image —
keeping the agent's toolchain and your dev shell from one pin (ADR 0002).

## Option surface

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
| `prompt`    | string                      | bundled starter    | agent prompt template baked into the image; changing it requires a rebuild (`nix run .#build`) |
| `defaults`  | `{ label; baseBranch; maxParallel; branchPrefix; inProgressLabel; failedLabel; model; }` | see below | non-secret run defaults baked into `run` |
| `runtime`   | `"podman"` \| `"docker"`    | `"podman"`         | container runtime the `build`/`run` commands drive                   |
| `nixBuilderImage` | string                | `"docker.io/nixos/nix:latest"` | Nix image `build` uses as a fallback Linux builder when the host can't realise the image |

The `defaults` submodule bakes the run knobs into the `run` command; a matching
env var still wins at runtime, so one built command can be re-pointed without a
rebuild. Baked defaults: `label = "ready-for-agent"`, `baseBranch = "main"`,
`maxParallel = 3`, `branchPrefix = "agent/issue-"`,
`inProgressLabel = "agent-in-progress"`, `failedLabel = "agent-failed"`,
`model = "claude-sonnet-4-6"`, `scoutModel = "claude-haiku-4-5-20251001"`,
`reviewModel = "claude-opus-4-8"`. `inProgressLabel`/`failedLabel` drive the
[label lifecycle](#label-lifecycle); `model` is the Claude model the in-container
implementor agent runs, threaded into the container as `MODEL` so `MODEL=...`
switches models at runtime with no image rebuild. `scoutModel`/`reviewModel` tier
the read-only scout and reviewer subagents the same way; setting either to `""`
drops both subagents from the `claude` invocation.

The **prompt is baked into the image**: changing `prompts/issue-prompt.md`
requires an image rebuild (`nix run .#build`). Point `SPINDRIFT_PROMPT_DIR`
at any directory to override it at runtime for zero-rebuild iteration.

## Runtime configuration

The target, secrets, and commit identity are **runtime env** (in `harness.env`
or your shell) — never Nix options — so one image drives any Target repo without
a rebuild (ADR 0001):

| var                       | default                | meaning                                  |
| ------------------------- | ---------------------- | ---------------------------------------- |
| `REPO_SLUG`               | — (required)           | target repo, `owner/repo`                |
| `GH_TOKEN`                | — (required)           | GitHub token for `gh` inside containers  |
| `CLAUDE_CODE_OAUTH_TOKEN` | — (one auth required)  | from `claude setup-token`                |
| `ANTHROPIC_API_KEY`       | —                      | alternative to the OAuth token           |
| `GIT_USER_NAME`           | host `git config` (required) | commit author name                 |
| `GIT_USER_EMAIL`          | host `git config` (required) | commit author email                |
| `LABEL`                   | `ready-for-agent`      | issues to pick up                        |
| `BASE_BRANCH`             | `main`                 | branch to cut from and PR into           |
| `MAX_PARALLEL`            | `3`                    | concurrent containers                    |
| `BRANCH_PREFIX`           | `agent/issue-`         | branch name = prefix + issue number      |
| `IN_PROGRESS_LABEL`       | `agent-in-progress`    | label a dispatched issue is swapped to   |
| `FAILED_LABEL`            | `agent-failed`         | label an issue gets when its Box fails   |
| `MODEL`                   | `claude-sonnet-4-6`    | Claude model the in-container implementor runs |
| `SCOUT_MODEL`             | `claude-haiku-4-5-20251001` | scout subagent model tier (empty drops subagents) |
| `REVIEW_MODEL`            | `claude-opus-4-8`      | reviewer subagent model tier (empty drops subagents) |
| `IMAGE`                   | `spindrift:latest`     | image tag to run                         |
| `SPINDRIFT_PROMPT_DIR`    | baked prompt store path | hot-override the mounted prompt dir     |

`LABEL`/`BASE_BRANCH`/`MAX_PARALLEL`/`BRANCH_PREFIX`/`IN_PROGRESS_LABEL`/`FAILED_LABEL`/`MODEL`
override whatever was baked
via `defaults`. Commit identity is **required**: an override wins, else the
host's `git config user.name`/`user.email` is inherited; if neither is set,
`run` exits rather than committing under an arbitrary identity.

## Runtime flow

```
nix run .#run
  └─ gh issue list --label ready-for-agent        (find the work)
     └─ for each issue, up to MAX_PARALLEL at once:
        podman run  spindrift:latest               (disposable box)
          └─ /agent/entrypoint.sh
             ├─ git clone <REPO_SLUG>  +  git checkout -b agent/issue-N
             ├─ run PREFETCH (optional cache warm-up)
             └─ claude -p "<prompts/issue-prompt.md>" --dangerously-skip-permissions
                └─ implement → check → commit → push → open PR → review → merge (rebase)
```

The harness never touches the Target repo's working tree on your host — it all
happens through fresh clones inside containers — so it can drive **any** GitHub
repo you point `REPO_SLUG` at. After its own review and green CI, the agent
merges the PR via rebase; `Closes #N` in the PR description closes the issue on
merge.

## Label lifecycle

`run` uses labels on the Target repo as the dispatch state of each issue, which
is what makes re-running it safe. `run` queries only `LABEL`
(`ready-for-agent`), so the labels below are what keep an issue from being picked
up twice:

```
ready-for-agent ──dispatch──▶ agent-in-progress ──Box exits ≠0──▶ agent-failed
   (launch button)              (a Box is running;                  (human triage;
                                 re-runs skip it)                    re-label to retry)
                                       │
                                  Box exits 0
                                       ▼
                              (no terminal label — the
                               merged PR's `Closes #N`
                               closes the issue)
```

- **Dispatch is idempotent.** As `run` hands each issue to a container it swaps
  `ready-for-agent` → `agent-in-progress`. Because the issue query matches only
  `ready-for-agent`, re-running `run` while PRs are still in review re-dispatches
  nothing — in-progress issues are no longer selected. (Without this, every
  re-run would burn a full agent run per issue before dying on a non-fast-forward
  push.)
- **Failures surface for triage.** If the container exits non-zero, `run` swaps
  `agent-in-progress` → `agent-failed` and stops. There are **no automatic
  retries**: a human inspects `logs/issue-<n>.log`, and re-labelling the issue
  `ready-for-agent` re-arms it for the next run.
- **Success is silent.** A successful Box leaves the labels untouched; the merged
  PR's `Closes #N` closes the issue.

Rename either label with the `inProgressLabel` / `failedLabel` `defaults` (baked)
or the `IN_PROGRESS_LABEL` / `FAILED_LABEL` env vars (runtime).

### Prerequisite: create the labels on the Target repo

`gh issue edit` cannot invent a label, so all three must already exist on the
Target repo. Create them once:

```sh
gh label create ready-for-agent   --repo owner/repo --color 0e8a16 --description "dispatch to a spindrift agent"
gh label create agent-in-progress --repo owner/repo --color fbca04 --description "a spindrift Box is working this issue"
gh label create agent-failed      --repo owner/repo --color b60205 --description "the Box exited non-zero; needs triage"
```

### Caveat: a killed launcher can strand an issue

The swaps are best-effort and there is **no reconciliation loop**. If the
launcher is killed mid-run (Ctrl-C, a crashed host, a laptop closing), an issue
can be left in `agent-in-progress` with no container running — so `run` will keep
skipping it. The unstick is a **manual label flip**: move it back to
`ready-for-agent` to re-dispatch (or to `agent-failed` to park it).

```sh
gh issue edit <n> --repo owner/repo --add-label ready-for-agent --remove-label agent-in-progress
```

## GitHub token

Use a **fine-grained personal access token** with access to **only the Target
repository**. That scoping is what bounds `--dangerously-skip-permissions`: even
if an agent misbehaves, the token can touch nothing but that one repo. The same
token is used by `gh` inside each container and by `run` to list issues on the
host.

| permission        | level          | why                                          |
| ----------------- | -------------- | -------------------------------------------- |
| Contents          | Read and write | clone the repo + push the branch             |
| Pull requests     | Read and write | open PRs (including drafts) + merge via rebase |
| Issues            | Read and write | read the issue; **write only** for the "if blocked" path that comments on it — drop that fallback and Read suffices |
| Metadata          | Read           | mandatory baseline, auto-selected            |
| Workflows         | Read and write | **only if** an issue edits `.github/workflows/*` — omit otherwise |

## Threat model

The isolation story leaves a few trust assumptions on the repo side. They are
deliberate, not oversights — write them down so you can honour them:

1. **The label is the launch button.** Anyone who can apply the label on the
   Target repo dispatches an Agent holding a repo-write token. GitHub requires
   the triage role to label, so treat every label-applier (triage and up) as a
   trusted operator — the label *is* the authorization step.
2. **Issue content is untrusted input.** Reading the issue is the Agent's whole
   job, so prompt injection is inherent to the design, not a bug to patch. What
   bounds it is the blast radius: exactly what the token allows and nothing
   more, because the Box has no host access.
3. **Branch protection is the required backstop.** The token needs Contents RW
   to push its `agent/issue-N` branch, and that same scope permits pushing to
   the base branch. Enable branch protection on the base branch with these
   settings: block direct pushes (the PR is the only path in); require CI
   status checks to pass before merge; and **do not require an external
   approving review** — a bot cannot approve its own PR, so that rule deadlocks
   autonomous self-merge. In repository settings, enable rebase merge (and
   optionally disable merge commits and squash) to keep a linear history.
4. **Scope the token.** Use a fine-grained PAT restricted to the single Target
   repo (Issues RW, Contents RW, Pull requests RW, Metadata R). Repo scoping is
   what turns "the Agent can do anything" into "anything, to one repo."

## Prerequisites

- **nix** with flakes enabled.
- **podman** (or set `runtime = "docker"`).
- A **GitHub token** scoped to the Target repo (see above).
- **Claude Code auth**: run `claude setup-token` on the host, or an API key.

## Building on macOS

OCI images are Linux-only, so the `spindrift` image is a *Linux* derivation even
on a Mac. The launcher commands (`build`/`run`) are native and only *reference*
the image path, so `nix flake check` never forces a Linux build. Realising the
image is `build`'s job, and it handles the Mac case for you:

- **Out of the box**: with no Linux builder, `nix run .#build` builds the image
  inside an **ephemeral Nix container** on your `podman`/`docker` runtime (the
  machine that can *run* the Box can always *build* it), reusing a named `/nix`
  volume so rebuilds are incremental. Nothing to configure beyond the runtime
  you already need — just run it from your Consumer flake's directory.
- **Faster with a real Linux builder** (skips the container round-trip):
  - **nix-darwin**: enable `nix.linux-builder.enable = true;` (a small Linux VM
    nix uses automatically). `build` then realises the image directly.
  - **Remote builder**: point nix at any Linux box via
    `nix.buildMachines` / `--builders`.
  - **Just build on Linux / CI** and load the result on the Mac.

The Nix container image the fallback uses is `docker.io/nixos/nix:latest` by
default; override it with the `nixBuilderImage` option.

## Customizing the template

The starter is a minimal Go example. To retarget it:

- **`packages` in `flake.nix`** — the toolchain baked into the image is one line
  (`p: [ p.go ]`), straight from nixpkgs. Swap it for your node/python/rust
  stack; add an `overlays` entry and matching input only if your stack needs one
  (e.g. `rust-overlay` for pinned Rust channels). The engine carries nothing
  language-specific (ADR 0003).
- **`prompts/issue-prompt.md`** — tune the agent's workflow (test commands,
  commit conventions, PR etiquette). If the Target repo ships a `commit` skill
  or `CLAUDE.md`, the agent picks it up from the clone automatically.

## Design notes

The harness reproduces the part that matters for isolation — *containerize the
runner, fan out one box per issue* — and leans on nix for the toolchain instead
of a Dockerfile. The trade-offs:

- **Simpler & fewer deps**: bash + nix + a container runtime + Claude Code. No
  orchestration library, no Node runtime to import.
- **No cross-issue dependency unblocking within a run.** Each container is
  independent and opens its own PR; ordering is left to humans (or a future
  planner phase). Good when issues are largely independent.
- **Reproducible toolchain by construction** via the pinned flake, rather than a
  floating language-runtime base image.

See `docs/adr/` for the architectural decisions.

## Unattended runs

`nix run .#run` is just a command, so wrap it however you schedule things —
`cron`, `launchd`, a systemd timer, or a CI job on a Linux runner (where the
image builds with no Linux-builder dance).

## Credits

Heavily inspired by Matt Pocock's
[Sandcastle](https://github.com/mattpocock/sandcastle) project.
