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
| `scoutPrompt` / `reviewPrompt` | string           | bundled starters   | system prompts for the read-only scout and reviewer subagents; baked in, overridable via `SPINDRIFT_PROMPT_DIR` |
| `skills`    | list of paths               | `[]`               | skill files baked into the image at `/home/agent/.claude/skills` so the headless agent can `/invoke` them; `SPINDRIFT_SKILLS_DIR` mounts over them at runtime |
| `defaults`  | submodule (all `flakeOption` env knobs) | see below | non-secret run defaults baked into `run` |
| `runtime`   | `"podman"` \| `"docker"` \| `"bwrap"` | `"podman"` | runner the `build`/`run` commands drive: an OCI runtime, or the daemonless bubblewrap sandbox (`bwrap`, Linux-only, no image build/load) |
| `nixInBox`  | bool                        | `true`             | bake a usable nix (binary + registered store DB + sandbox-off config) into the box so `nix flake check` / `nix develop` work inside it; set `false` for a lean, nix-free image (ADR 0008) |
| `nixBuilderImage` | string                | `"docker.io/nixos/nix@sha256:bf1d938835ab96312f098fa6c2e9cab367728e0aad0646ee3e02a787c80d8fb8"` | Nix image `build` uses as a fallback Linux builder when the host can't realise the image; pinned by digest for supply-chain safety (see below) |

The `defaults` submodule bakes the run knobs into the `run` command; a matching
env var still wins at runtime, so one built command can be re-pointed without a
rebuild. It exposes every consumer-tunable knob from `lib/env-schema.nix` (the
single source of truth for the runtime env surface). Key baked defaults:
`label = "ready-for-agent"`, `baseBranch = "main"`, `maxParallel = 3`,
`branchPrefix = "agent/issue-"`, `inProgressLabel = "agent-in-progress"`,
`failedLabel = "agent-failed"`, `completeLabel = "agent-complete"`,
`model = "claude-sonnet-4-6"`, `scoutModel = "claude-haiku-4-5-20251001"`,
`reviewModel = "claude-opus-4-8"`.
`inProgressLabel`/`failedLabel`/`completeLabel` drive the
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
| `ISSUE_NUMBER`            | — (empty = discover)   | dispatch only this one issue, bypassing the `LABEL` query |
| `BASE_BRANCH`             | `main`                 | branch to cut from and PR into           |
| `MAX_PARALLEL`            | `3`                    | concurrent containers                    |
| `BRANCH_PREFIX`           | `agent/issue-`         | branch name = prefix + issue number      |
| `IN_PROGRESS_LABEL`       | `agent-in-progress`    | label a dispatched issue is swapped to   |
| `FAILED_LABEL`            | `agent-failed`         | label an issue gets when its Box fails or its PR can't merge |
| `COMPLETE_LABEL`          | `agent-complete`       | label the launcher swaps on after a successful rebase-merge |
| `BARRIER_LABEL`           | — (empty = off)        | open issues carrying it fence all higher-numbered issues until they close |
| `MODEL`                   | `claude-sonnet-4-6`    | Claude model the in-container implementor runs |
| `SCOUT_MODEL`             | `claude-haiku-4-5-20251001` | scout subagent model tier (empty drops subagents) |
| `REVIEW_MODEL`            | `claude-opus-4-8`      | reviewer subagent model tier (empty drops subagents) |
| `IMAGE`                   | `spindrift:latest`     | image tag to run                         |
| `SPINDRIFT_PROMPT_DIR`    | baked prompt store path | hot-override the mounted prompt dir     |
| `SPINDRIFT_SKILLS_DIR`    | baked skills store path | hot-override the mounted skills dir     |

Every `defaults`-baked knob above can be re-pointed at runtime; the env var
wins over whatever was baked. Commit identity is **required**: an override wins,
else the host's `git config user.name`/`user.email` is inherited; if neither is
set, `run` exits rather than committing under an arbitrary identity.

### Advanced tuning

These knobs are runtime-only (no `defaults` baking) unless noted, and rarely
need changing. See `lib/env-schema.nix` for the authoritative list.

| var                    | default | meaning                                                        |
| ---------------------- | ------- | -------------------------------------------------------------- |
| `MAX_JOBS`             | `0`     | drain at most N unblocked issues then exit (`0` = unlimited / full waves) |
| `MAX_FIX_ATTEMPTS`     | `3`     | fix-box passes when CI is genuinely red before `agent-failed` (`0` disables self-healing) |
| `MAX_REBASE_ATTEMPTS`  | `3`     | rebase-and-retry passes when a green PR conflicts after a sibling merge (`0` disables) |
| `MERGE_POLL_INTERVAL`  | `30`    | seconds between CI-status polls in the merge gate              |
| `MERGE_POLL_TIMEOUT`   | `1800`  | seconds to wait for CI green before abandoning the merge       |
| `DEPS_POLL_SECS`       | `30`    | seconds between dependency-wave poll iterations                |
| `DEPS_WAIT_SECS`       | `7200`  | seconds to wait for a dependency wave before declaring deadlock |
| `TRANSIENT_RETRY_MAX`  | `3`     | retries for transient box exits (529/network backoff; consecutive 429 holds) |
| `TRANSIENT_BACKOFF_SECS` | `30`  | base linear backoff per transient retry                        |
| `HOLD_JITTER_SECS`     | `5`     | jitter added to a 429 hold-until-reset before re-dispatch      |
| `DEV_SHELL_PROBE_TIMEOUT` (baked) | `300` | seconds before the in-box devShell probe is abandoned for the baked toolchain |
| `MEMORY_LIMIT` (baked) | `4g`    | per-container `--memory` cap (OCI only; empty disables)        |
| `PIDS_LIMIT` (baked)   | `512`   | per-container `--pids-limit` cap (OCI only; empty disables)    |
| `PODMAN_NETWORK` (baked) | —     | `--network` value for podman run; set `pasta` to restrict egress |
| `BWRAP_UNSHARE_NET` (baked) | —  | non-empty adds `--unshare-net` to the bwrap runner             |

## Runtime flow

```
nix run .#run   (the nix-built Go launcher, host-side)
  ├─ reconcile any stranded agent-in-progress issues with an open PR (adopt + gate)
  └─ gh issue list --label ready-for-agent        (find the work)
     └─ for each issue, up to MAX_PARALLEL at once:
        podman run  spindrift:latest               (disposable box)
          └─ /agent/entrypoint.sh
             ├─ git clone <REPO_SLUG>  +  git checkout -b agent/issue-N
             ├─ run PREFETCH (optional cache warm-up)
             └─ claude -p "<prompts/issue-prompt.md>" --dangerously-skip-permissions
                └─ implement → check → commit → push → self-review (reviewer subagent)
                   → open PR → wait for CI to register
                   → print  SPINDRIFT_OUTCOME issue=N pr=<url> status=ready
        │
        └─ back on the host, the launcher runs the MERGE GATE for that issue:
           ├─ poll CI on the PR head until green (or red, or timeout)
           ├─ green → rebase-merge the PR → swap issue to agent-complete
           ├─ red   → dispatch fix boxes (up to MAX_FIX_ATTEMPTS), then re-gate
           ├─ merge conflict → rebase the PR (up to MAX_REBASE_ATTEMPTS)
           └─ post an aggregate usage/cost comment to the issue
```

The split is deliberate: the **Box** owns implementing the issue and opening the
PR, but the **launcher** (host-side, the Go binary) owns the CI-green decision,
the rebase-merge, and the terminal label swap — a Box cannot approve or merge its
own PR, and keeping merge authority outside the throwaway container is what makes
branch protection meaningful. The Box's last line is a machine-readable
`SPINDRIFT_OUTCOME` line (grammar in `cmd/launcher/internal/outcome`) that tells
the launcher which PR to gate.

The harness never touches the Target repo's working tree on your host — it all
happens through fresh clones inside containers — so it can drive **any** GitHub
repo you point `REPO_SLUG` at. `Closes #N` in the PR description closes the issue
when the launcher merges it.

## Label lifecycle

`run` uses labels on the Target repo as the dispatch state of each issue, which
is what makes re-running it safe. `run` queries only `LABEL`
(`ready-for-agent`), so the labels below are what keep an issue from being picked
up twice:

```
ready-for-agent ──dispatch──▶ agent-in-progress ──CI green + merge──▶ agent-complete
   (launch button)              (a Box is running,                     (merged; issue
                                 or the merge gate is                   closed via Closes #N)
                                 polling CI; re-runs skip it)
                                       │
                                       ├─ Box exits ≠0 (after retries) ─┐
                                       └─ CI red after MAX_FIX_ATTEMPTS ─┤
                                          or merge otherwise fails       ▼
                                                                    agent-failed
                                                                    (human triage;
                                                                     re-label to retry)
```

- **Dispatch is idempotent.** As `run` hands each issue to a container it swaps
  `ready-for-agent` → `agent-in-progress`. Because the issue query matches only
  `ready-for-agent`, re-running `run` while PRs are still in the merge gate
  re-dispatches nothing — in-progress issues are no longer selected.
- **Success is labelled.** When the merge gate merges the PR it swaps
  `agent-in-progress` → `agent-complete`, then verifies the PR really is merged
  and the label really landed. `Closes #N` in the PR body closes the issue on
  merge. (Dependency ordering keys off this label — a blocker is "ready" once it
  carries `agent-complete` or is closed.)
- **Red CI self-heals before it fails.** If CI goes genuinely red, the launcher
  dispatches up to `MAX_FIX_ATTEMPTS` fix boxes on the same branch and re-gates
  after each. Only once those are exhausted (or the box itself exits non-zero
  after transient retries) does it swap to `agent-failed` and stop. There are
  **no automatic re-dispatches from `ready-for-agent`**: a human inspects
  `logs/issue-<n>.log` and re-labels to retry.
- **Stranded issues are reconciled.** At startup `run` scans open
  `agent-in-progress` issues that already have an open non-draft PR and re-runs
  the merge gate on each ("adopts" them) — so a launcher killed mid-gate picks up
  where it left off on the next run, without a fresh agent pass.

Rename any of these with the `inProgressLabel` / `failedLabel` / `completeLabel`
`defaults` (baked) or the `IN_PROGRESS_LABEL` / `FAILED_LABEL` / `COMPLETE_LABEL`
env vars (runtime).

### Prerequisite: create the labels on the Target repo

`gh issue edit` cannot invent a label, so all four must already exist on the
Target repo. Create them once:

```sh
gh label create ready-for-agent   --repo owner/repo --color 0e8a16 --description "dispatch to a spindrift agent"
gh label create agent-in-progress --repo owner/repo --color fbca04 --description "a spindrift Box is working this issue"
gh label create agent-complete    --repo owner/repo --color 5319e7 --description "the PR was merged by the launcher's merge gate"
gh label create agent-failed      --repo owner/repo --color b60205 --description "the Box failed or the PR could not merge; needs triage"
```

### Caveat: a killed launcher can strand an issue

The label swaps are best-effort. If the launcher is killed mid-run (Ctrl-C, a
crashed host, a laptop closing) an issue can be left in `agent-in-progress` with
no container running. The next `run` **reconciles automatically** for the common
case: it adopts any `agent-in-progress` issue that already has an open non-draft
PR and re-runs the merge gate on it. What it cannot recover on its own is an
issue stranded *before* a PR was opened (or with only a draft PR) — there is
nothing to adopt, and the `LABEL` query skips it. The unstick there is a
**manual label flip**: move it back to `ready-for-agent` to re-dispatch (or to
`agent-failed` to park it).

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
| Issues            | Read and write | read the issue; write to swap the dispatch labels (`agent-in-progress`/`agent-complete`/`agent-failed`) and post the per-issue usage/cost comment |
| Metadata          | Read           | mandatory baseline, auto-selected            |
| Workflows         | Read and write | **off by default** — grant only when an issue edits `.github/workflows/*`; agent branches run in-repo so `pull_request` events carry repository secrets; with this permission an injected agent can rewrite CI or exfiltrate those secrets |

## Threat model

The isolation story leaves a few trust assumptions on the repo side. They are
deliberate, not oversights — write them down so you can honour them:

1. **The label is the launch button.** Anyone who can apply the label on the
   Target repo dispatches an Agent holding a repo-write token. GitHub requires
   the triage role to label, so treat every label-applier (triage and up) as a
   trusted operator — the label *is* the authorization step.
2. **Issue body and comments are attacker-writable input.** Reading the issue is
   the Agent's whole job, so prompt injection is inherent to the design, not a
   bug to patch. The label gates *which* issues get dispatched — but once
   labeled, the issue body and **every comment from any GitHub user** feed the
   agent as prompt input. The trust boundary is the label, not the issue or
   comment author. What bounds the blast radius is what the token allows and
   nothing more, because the Box has no host access.
3. **Branch protection is a hard prerequisite, not a nicety.** The token needs
   Contents RW to push its `agent/issue-N` branch, and that same scope permits
   pushing directly to the base branch — bypassing the PR flow entirely. Without
   branch protection **the harness is not safe to deploy**. Enable it on the
   base branch: block direct pushes (the PR is the only path in); require CI
   status checks to pass before merge; **do not require an external approving
   review** — a bot cannot approve its own PR, so that rule deadlocks autonomous
   self-merge. In repository settings, enable rebase merge to keep a linear
   history. Branch protection requires a public repo or a paid GitHub plan —
   **do not point the harness at a private repo on GitHub Free** where branch
   protection is unavailable.
4. **A fine-grained single-repo PAT is required, not recommended.** A
   broadly-scoped classic PAT or a multi-repo fine-grained PAT gives an
   injected agent write access to every repo the token reaches. Use a
   fine-grained PAT restricted to the single Target repo (Issues RW, Contents
   RW, Pull requests RW, Metadata R). That restriction is what turns "the Agent
   can do anything" into "anything, to one repo."
5. **Workflows:RW is off by default and carries elevated risk.** Agent PR
   branches live in-repo (not forks), so `pull_request` workflow events run
   with repository secrets. With Workflows:RW, an injected agent can rewrite
   CI to auto-pass status checks or exfiltrate Actions secrets. Grant it only
   when an issue explicitly edits `.github/workflows/*`, and treat that grant
   as escalated trust. See the token permission table above.

## Prerequisites

- **nix** with flakes enabled.
- **podman** (or set `runtime = "docker"`; or `runtime = "bwrap"` for the
  daemonless bubblewrap sandbox on Linux, which needs no container runtime).
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

The Nix container image the fallback uses is pinned by digest (default:
`docker.io/nixos/nix@sha256:bf1d938835ab96312f098fa6c2e9cab367728e0aad0646ee3e02a787c80d8fb8`).
Digest pinning is a supply-chain safety measure: the container runs with the
consumer's working tree bind-mounted read-write, so an unpinned `:latest` tag
would be a silent code-execution vector. Override with the `nixBuilderImage`
parameter in your `mkHarness` call.

**Bumping the pin:** pull the image you want, inspect its digest, and update
both `mkHarness.nix` and `README.md`:

```bash
podman pull docker.io/nixos/nix:latest
podman image inspect --format '{{index .RepoDigests 0}}' nixos/nix
# → docker.io/nixos/nix@sha256:<new-digest>
```


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

- **Simpler & fewer deps**: nix + a container runtime + Claude Code. The
  orchestration is a small, nix-built Go binary (`cmd/launcher`, ADR 0007); the
  only bash left is the in-box entrypoint. No orchestration library, no Node
  runtime to import.
- **Cross-issue dependency ordering within a run.** The launcher parses
  `depends on #N` / `blocked by #N` (inline or a `## Blocked by` list) from issue
  bodies and dispatches in dependency waves, holding a dependent until its
  blockers reach `agent-complete`; a cycle aborts the run. Independent issues
  still fan out concurrently up to `MAX_PARALLEL`.
- **Reproducible toolchain by construction** via the pinned flake, rather than a
  floating language-runtime base image.

See `docs/adr/` for the architectural decisions (0001–0008), including the
Go launcher (0007), the pluggable OCI/bwrap runner (0006), and nix-in-the-box
(0008).

## Unattended runs

`nix run .#run` is just a command, so wrap it however you schedule things —
`cron`, `launchd`, a systemd timer, or a CI job on a Linux runner (where the
image builds with no Linux-builder dance).

## Credits

Heavily inspired by Matt Pocock's
[Sandcastle](https://github.com/mattpocock/sandcastle) project.

## License

MIT — see [LICENSE](LICENSE).
