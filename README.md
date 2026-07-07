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
many differently-toolchained Target repos). → [reference](docs/reference.md#configuring-the-harness)

## Runtime flow

```
spindrift dispatch   (the nix-built Go launcher, host-side)
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
           ├─ green → swap issue to agent-complete, then apply MERGE_MODE:
           │           manual    → leave the green PR for a human (default)
           │           immediate → rebase-merge the PR now
           │           auto      → enqueue GitHub native auto-merge
           ├─ red   → dispatch fix boxes (up to MAX_FIX_ATTEMPTS), then re-gate
           ├─ merge conflict (immediate) → rebase the PR (up to MAX_REBASE_ATTEMPTS)
           └─ post an aggregate usage/cost comment to the issue
```

The split is deliberate: the **Box** owns implementing the issue and opening the
PR, but the **launcher** (host-side, the Go binary) owns the CI-green decision,
the merge, and the terminal label swap — a Box cannot approve or merge its own
PR, and keeping merge authority outside the throwaway container is what makes
branch protection meaningful. `agent-complete` marks CI green (the agent's work
is done); **whether the PR then merges is the `MERGE_MODE` policy**, decoupled so
the same run can land PRs automatically or hand green PRs to a human reviewer. The
Box's last line is a machine-readable `SPINDRIFT_OUTCOME` line (grammar in
`cmd/launcher/internal/outcome`) that tells the launcher which PR to gate.

The harness never touches the Target repo's working tree on your host — it all
happens through fresh clones inside containers — so it can drive **any** GitHub
repo you point `REPO_SLUG` at. `Closes #N` in the PR description closes the issue
when the PR merges — by the launcher (`immediate`), by GitHub (`auto`), or by a
human (`manual`).

## Label lifecycle

`spindrift dispatch` uses labels on the Target repo as the dispatch state of each
issue, which is what makes re-running it safe. It queries only `LABEL`
(`ready-for-agent`), so the labels below are what keep an issue from being picked
up twice:

```
ready-for-agent ──dispatch──▶ agent-in-progress ─────CI green─────▶ agent-complete
   (launch button)              (a Box is running,                   (agent done; PR is
                                 or the merge gate is                  green — then merged
                                 polling CI; re-runs skip it)         per MERGE_MODE)
                                       │
                                       ├─ Box exits ≠0 (after retries) ─┐
                                       └─ CI red after MAX_FIX_ATTEMPTS ─┤
                                          or merge otherwise fails       ▼
                                                                    agent-failed
                                                                    (human triage;
                                                                     re-label to retry)
```

- **Dispatch is idempotent.** As `dispatch` hands each issue to a container it
  swaps `ready-for-agent` → `agent-in-progress`. Because the issue query matches
  only `ready-for-agent`, re-running `dispatch` while PRs are still in the merge
  gate re-dispatches nothing — in-progress issues are no longer selected.
- **Green is labelled; merge is a separate policy.** When CI confirms green the
  merge gate swaps `agent-in-progress` → `agent-complete` — the agent's work is
  done and the PR is mergeable. What happens next is `MERGE_MODE`: `immediate`
  rebase-merges the PR (then verifies it really is merged and the label landed);
  `auto` enqueues GitHub's native auto-merge; `manual` (the default) leaves the
  green PR open for a human. `Closes #N` in the PR body closes the issue whenever
  the PR merges. (Dependency ordering keys off this label — a blocker is "ready"
  once it carries `agent-complete` or is closed, so waves advance on green even in
  `manual` mode.)
- **Red CI self-heals before it fails.** If CI goes genuinely red, the launcher
  dispatches up to `MAX_FIX_ATTEMPTS` fix boxes on the same branch and re-gates
  after each. Only once those are exhausted (or the box itself exits non-zero
  after transient retries) does it swap to `agent-failed` and stop. There are
  **no automatic re-dispatches from `ready-for-agent`**: a human inspects
  `logs/issue-<n>.log` and re-labels to retry.
- **Stranded issues are reconciled.** At startup `spindrift dispatch` scans open
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
no container running. The next `spindrift dispatch` **reconciles automatically** for the common
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
token is used by `gh` inside each container and by `spindrift dispatch` to list
issues on the host.

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

OCI images are Linux-only, so the `agent-image` package is a *Linux* derivation even
on a Mac. The launcher commands (`spindrift build`/`dispatch`) are native and only *reference*
the image path, so `nix flake check` never forces a Linux build. Realising the
image is `spindrift build`'s job, and it handles the Mac case for you:

- **Out of the box**: with no Linux builder, `spindrift build` builds the image
  inside an **ephemeral Nix container** on your `podman`/`docker` runtime (the
  machine that can *run* the Box can always *build* it), reusing a named `/nix`
  volume so rebuilds are incremental. Nothing to configure beyond the runtime
  you already need — just run it from your Consumer flake's directory.
- **Faster with a real Linux builder** (skips the container round-trip):
  - **nix-darwin**: enable `nix.linux-builder.enable = true;` (a small Linux VM
    nix uses automatically). `spindrift build` then realises the image directly.
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

`spindrift dispatch` is just a command, so wrap it however you schedule things —
`cron`, `launchd`, a systemd timer, or a CI job on a Linux runner (where the
image builds with no Linux-builder dance). In non-interactive contexts invoke the
CLI by its store path or via `nix run .#default -- dispatch` rather than relying
on a dev-shell PATH.

## Credits

Heavily inspired by Matt Pocock's
[Sandcastle](https://github.com/mattpocock/sandcastle) project.

## License

MIT — see [LICENSE](LICENSE).
