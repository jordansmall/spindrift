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
| `spindrift build`                | realize/load the agent image (or store closures) without running any agent      |
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
> to `spindrift dispatch` / `spindrift build`.

If you use **direnv**, the template's `.envrc` (`use flake`) activates the dev
shell automatically on `cd` — no manual `nix develop` needed.

`spindrift build` **realizes** the image derivation and then loads it into your
container runtime. On a host with a Linux builder (any Linux machine, or a Mac
with a Linux builder configured) it realizes the image directly. On a stock Mac
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
| `settings`  | submodule, grouped by section (see below) | `{}` | non-secret run defaults baked into the `spindrift` CLI |
| `runtime`   | `"podman"` \| `"docker"` \| `"bwrap"` | `"podman"` | runner the `spindrift build`/`dispatch` commands drive: an OCI runtime, or the daemonless bubblewrap sandbox (`bwrap`, Linux-only, no image build/load) |
| `nixInBox`  | bool                        | `true`             | bake a usable nix (binary + registered store DB + sandbox-off config) into the box so `nix flake check` / `nix develop` work inside it; set `false` for a lean, nix-free image (ADR 0008) |
| `nixBuilderImage` | string                | `"docker.io/nixos/nix@sha256:bf1d938835ab96312f098fa6c2e9cab367728e0aad0646ee3e02a787c80d8fb8"` | Nix image `spindrift build` uses as a fallback Linux builder when the host can't realize the image; pinned by digest for supply-chain safety (see [Building on macOS](#building-on-macos)) |

The `settings` submodule bakes run knobs into the `spindrift` CLI; a matching
env var still wins at runtime, so one built command can be re-pointed without a
rebuild. Knobs are grouped by section — the same headings as `spindrift --help
--all` — so the flake surface is self-documenting and stays in lockstep with the
CLI help. Sections and knobs derive from `lib/env-schema.nix`; unknown section
or knob names are rejected at eval time by the NixOS module system.

```nix
settings = {
  issueDiscovery  = { label          = "ready-for-agent"; };
  lifecycleLabels = { inProgressLabel = "agent-in-progress";
                      failedLabel     = "agent-failed";
                      completeLabel   = "agent-complete"; };
  branches        = { baseBranch = "main"; branchPrefix = "agent/issue-";
                      mergeMode  = "manual";
                      mergeGuardPaths = ".github/**,**/CLAUDE.md,**/AGENTS.md,.claude/**,.opencode/**";
                      mergePollInterval = 30; mergePollTimeout = 1800; };
  concurrency     = { maxParallel = 3; maxJobs = 0;
                      depsPollSecs = 30; depsWaitSecs = 7200; };
  models          = { model = "claude-sonnet-5";
                      scoutModel  = "claude-haiku-4-5-20251001";
                      reviewModel = "claude-opus-4-8"; };
  sandbox         = { devShellName = "default"; devShellProbeTimeout = 300;
                      memoryLimit = "4g"; pidsLimit = "512";
                      podmanNetwork = ""; bwrapUnshareNet = ""; };
  selfHealing     = { maxFixAttempts = 3; maxRebaseAttempts = 3;
                      holdJitterSecs = 5; transientBackoffSecs = 30;
                      transientRetryMax = 3; };
  repository      = { repoSlug = "owner/repo";
                      gitUserName = "bot"; gitUserEmail = "bot@example.com"; };
};
```

#### Discovering flake options

Three paths to discover which options exist and what they do:

1. **Generated reference** — [`docs/flake-options.md`](flake-options.md) lists
   every `settings.<section>.<knob>` with its env var, default, and description.
   It is generated from `lib/env-schema.nix` and drift-guarded by `nix flake
   check`; it is always in sync with the schema.

2. **LSP autocomplete** — `nixd` and `nil` read the module option declarations
   that `lib/flakeModule.nix` generates from the same schema.  Opening your
   Consumer flake in an editor with either LSP gives option completions and hover
   documentation for every `settings.<section>.<knob>` inline.

3. **CLI reference** — `spindrift --help --all` (or `man spindrift`) prints the
   full flag table grouped by section.  Every `settings` knob maps 1:1 to a
   `--<flag>` in the same section heading, so the CLI reference doubles as a
   guide to what is settable in the flake.

`inProgressLabel`/`failedLabel`/`completeLabel` drive the
[label lifecycle](#how-a-run-works); `mergeMode` is the post-green
[merge policy](#how-a-run-works) (`manual`/`immediate`/`auto`); `mergeGuardPaths`
is the [merge guard](#merge-guard)'s glob list, downgrading a green PR to
manual regardless of `mergeMode` when it touches a guarded path; `model` is the
Claude model the in-container implementor agent runs, threaded into the container
as `MODEL` so `MODEL=...` switches models at runtime with no image rebuild.
`scoutModel`/`reviewModel` tier the read-only scout and reviewer subagents the
same way; setting either to `""` drops both subagents from the `claude`
invocation.

The **prompt is baked into the image**: changing `prompts/issue-prompt.md`
requires an image rebuild (`spindrift build`). Point `SPINDRIFT_PROMPT_DIR`
at any directory to override it at runtime for zero-rebuild iteration.

### Cold-run toolchain nudge

When a Box runs **without a configured `prefetch`** and the cloned Target
contains a recognized dependency lockfile, the entrypoint logs a one-time
informational hint after the clone:

```
==> hint: go mod project detected; set 'prefetch' to warm dependency caches per run, or 'packages' to bake a toolchain into the image
```

The hint names the detected ecosystem and the two knobs that help:

| knob       | effect                                                                  |
| ---------- | ----------------------------------------------------------------------- |
| `prefetch` | shell snippet that runs in the work tree after each clone; use it to download and cache dependencies so the agent doesn't fetch them cold on every tool invocation |
| `packages` | bakes a toolchain into the image itself; pre-warmed across runs (no per-run network fetch needed) |

Detection covers the following lockfiles (first match wins):

| lockfile                                              | reported ecosystem |
| ----------------------------------------------------- | ------------------ |
| `Cargo.lock`                                          | cargo              |
| `package-lock.json`, `pnpm-lock.yaml`, `yarn.lock`   | npm/pnpm/yarn      |
| `go.sum`                                              | go mod             |

Unrecognized ecosystems emit no hint. The hint is suppressed entirely when
`prefetch` is already configured, so it is ignorable once you have acted on it.

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
   `nix develop ".#<DEV_SHELL_NAME>" --command true` under a
   `DEV_SHELL_PROBE_TIMEOUT`-second timeout (default 300 s, baked from
   `lib/env-schema.nix`).
2. If the probe succeeds, the prefetch hook and the Driver (claude invocation)
   run inside `nix develop ".#<DEV_SHELL_NAME>" --command bash <wrapper>` so
   the agent operates in the Target's exact pinned environment — tools, env
   vars, and shellHook included. If `nix develop` fails to exec the Driver
   (nix rc ≠ 0 and empty stream), the entrypoint relaunches once in the baked
   env rather than dying.
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

---

## Runtime configuration

The target, secrets, and commit identity are **runtime env** (in `harness.env`
or your shell) — never Nix options — so one image drives any Target repo without
a rebuild (ADR 0001):

| var                       | default                | meaning                                  |
| ------------------------- | ---------------------- | ---------------------------------------- |
| `REPO_SLUG`               | — (required; baked via `settings.repository.repoSlug`) | target repo, `owner/repo` |
| `GH_TOKEN`                | — (required)           | GitHub token for `gh` inside containers (secret; env only) |
| `CLAUDE_CODE_OAUTH_TOKEN` | — (one auth required)  | from `claude setup-token` (secret; env only) |
| `ANTHROPIC_API_KEY`       | —                      | alternative to the OAuth token (secret; env only) |
| `GIT_USER_NAME`           | host `git config`; baked via `settings.repository.gitUserName` | commit author name |
| `GIT_USER_EMAIL`          | host `git config`; baked via `settings.repository.gitUserEmail` | commit author email |
| `CODE_FORGE`              | `github` (baked)       | code-landing backend: `github` (open PR, watch CI, merge) or `git` (push-only to `CODE_FORGE_REMOTE_URL`; no PR, CI-watch, or merge gate — see [ADR 0013](../docs/adr/0013-issue-tracker-and-code-forge-are-independent-seams.md)) |
| `CODE_FORGE_REMOTE_URL`   | — (required when `CODE_FORGE=git`) | plain git remote URL to clone from and push to (self-hosted git, gitea, GitLab-without-MRs, a bare server repo) |
| `LABEL`                   | `ready-for-agent` (baked) | issues to pick up                     |
| `ISSUE_NUMBER`            | — (empty = discover)   | dispatch only this one issue, bypassing the `LABEL` query (per-run only; not bakeable) |
| `ISSUE_TRACKER`           | `github` (baked)       | IssueTracker backend: `github`, `local` (private Markdown + YAML frontmatter files — see [Local issue tracker](#local-issue-tracker-issue_trackerlocal)), or `jira` (see [Issue Tracker backends](#issue-tracker-backends)) |
| `LOCAL_ISSUES_DIR`        | `.spindrift/issues` (baked) | directory scanned for issue files when `ISSUE_TRACKER=local`; git-ignored by default |
| `BASE_BRANCH`             | `main` (baked)         | branch to cut from and PR into           |
| `MAX_PARALLEL`            | `3` (baked)            | concurrent containers                    |
| `BRANCH_PREFIX`           | `agent/issue-` (baked) | branch name = prefix + issue number      |
| `IN_PROGRESS_LABEL`       | `agent-in-progress` (baked) | label a dispatched issue is swapped to |
| `FAILED_LABEL`            | `agent-failed` (baked) | label an issue gets when its Box fails or its PR can't merge |
| `COMPLETE_LABEL`          | `agent-complete` (baked) | label the launcher swaps on when CI reaches green (agent is done; the merge is a separate step) |
| `MERGE_MODE`              | `manual` (baked)       | post-green merge policy: `manual` (leave the green PR for a human), `immediate` (rebase-merge on green), `auto` (enqueue GitHub native auto-merge — repo must have *Allow auto-merge* on). Under `CODE_FORGE=git`, `manual`/`immediate` map to remote pushes instead (leave the pushed branch / push straight to the target branch); `auto` has no meaning off `github` and fails fast at startup. |
| `MERGE_GUARD_PATHS`       | `.github/**,**/CLAUDE.md,**/AGENTS.md,.claude/**,.opencode/**` (baked) | comma-separated globs; a green PR touching a matched path downgrades to manual regardless of `MERGE_MODE` (`github` Code Forge only; empty disables — see [Merge guard](#merge-guard)) |
| `MODEL`                   | `claude-sonnet-5` (baked) | Claude model the in-container implementor runs |
| `SCOUT_MODEL`             | `claude-haiku-4-5-20251001` (baked) | scout subagent model tier (empty drops the scout entry from `--agents`) |
| `REVIEW_MODEL`            | `claude-opus-4-8` (baked) | reviewer subagent model tier (empty drops the reviewer entry from `--agents`) |
| `IMAGE`                   | `spindrift:latest`     | image tag to run                         |
| `SPINDRIFT_PROMPT_DIR`    | baked prompt store path | hot-override the mounted prompt dir (not bakeable) |
| `SPINDRIFT_SKILLS_DIR`    | baked skills store path | hot-override the mounted skills dir (not bakeable) |

Every `settings`-baked knob above can be re-pointed at runtime; the env var
wins over whatever was baked. `(baked)` marks knobs whose defaults are baked
into the `spindrift` CLI via `settings`; `(not bakeable)` marks knobs
deliberately kept off the flake surface (secrets, per-run overrides, and
dev-iteration host-path mounts). Commit identity is **required**: an override
wins, else the host's `git config user.name`/`user.email` is inherited; if
neither is set, `spindrift dispatch` exits rather than committing under an
arbitrary identity.

### Advanced tuning

These knobs are rarely changed. All except `SPINDRIFT_PROMPT_DIR`,
`SPINDRIFT_SKILLS_DIR`, and `ISSUE_NUMBER` can be baked via `settings` (see
[Option surface](#option-surface)) or overridden at runtime via env var or CLI
flag — whichever wins at runtime takes precedence. See `lib/env-schema.nix` for
the authoritative list.

| var                    | default | `settings` section | meaning                                                |
| ---------------------- | ------- | ------------------ | ------------------------------------------------------ |
| `MAX_JOBS`             | `0`     | `concurrency`      | drain at most N unblocked issues then exit (`0` = unlimited / full waves) |
| `MAX_FIX_ATTEMPTS`     | `3`     | `selfHealing`      | fix-box passes when CI is genuinely red before `agent-failed` (`0` disables self-healing) |
| `MAX_REBASE_ATTEMPTS`  | `3`     | `selfHealing`      | rebase-and-retry passes when a green PR conflicts after a sibling merge (`0` disables) |
| `MERGE_POLL_INTERVAL`  | `30`    | `branches`         | seconds between CI-status polls in the merge gate      |
| `MERGE_POLL_TIMEOUT`   | `1800`  | `branches`         | seconds to wait for CI green before abandoning the merge |
| `DEPS_POLL_SECS`       | `30`    | `concurrency`      | seconds between dependency-wave poll iterations        |
| `DEPS_WAIT_SECS`       | `7200`  | `concurrency`      | seconds to wait for a dependency wave before declaring deadlock |
| `TRANSIENT_RETRY_MAX`  | `3`     | `selfHealing`      | retries for transient box exits (529/network backoff; consecutive 429 holds) |
| `TRANSIENT_BACKOFF_SECS` | `30`  | `selfHealing`      | base linear backoff per transient retry                |
| `HOLD_JITTER_SECS`     | `5`     | `selfHealing`      | jitter added to a 429 hold-until-reset before re-dispatch |
| `DEV_SHELL_NAME`       | `default` | `sandbox`        | which devShell to enter; set `ci` to use a lean headless shell distinct from the interactive `default` |
| `DEV_SHELL_PROBE_TIMEOUT` | `300` | `sandbox`        | seconds before the in-box devShell probe is abandoned for the baked toolchain |
| `MEMORY_LIMIT`         | `4g`    | `sandbox`          | per-container `--memory` cap (OCI only; empty disables) |
| `PIDS_LIMIT`           | `512`   | `sandbox`          | per-container `--pids-limit` cap (OCI only; empty disables) |
| `PODMAN_NETWORK`       | —       | `sandbox`          | `--network` value for podman run; set `pasta` to restrict egress |
| `BWRAP_UNSHARE_NET`    | —       | `sandbox`          | non-empty adds `--unshare-net` to the bwrap runner      |

---

## How a run works

### Runtime flow

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

**`CODE_FORGE=git`** (push-only, [ADR 0013](../docs/adr/0013-issue-tracker-and-code-forge-are-independent-seams.md))
replaces everything from *open PR* onward: the Box pushes its branch to
`CODE_FORGE_REMOTE_URL` and prints `SPINDRIFT_OUTCOME ... pr=agent/issue-N status=ready`
— no PR, no CI-watch. The launcher skips the CI-poll entirely (there is
nothing to poll) and swaps the issue straight to `agent-complete`, then
applies `MERGE_MODE` as a plain push: `manual` leaves the branch as pushed,
`immediate` merges it onto the target branch. `auto` has no meaning off
`github` and is rejected at startup.

`immediate`'s merge-and-push to `CODE_FORGE_REMOTE_URL` runs on the
**launcher host** (a throwaway local clone, not inside a Box), reusing
`GIT_USER_NAME`/`GIT_USER_EMAIL` as the merge commit's identity. The host
needs its own push credentials for that remote (e.g. an SSH key or
credential helper covering `CODE_FORGE_REMOTE_URL`) — separate from the
Box's `GH_TOKEN`, which only covers the Issue Tracker. A push auth failure
surfaces as a `merge-blocked` comment on the issue, not a crash.

### Label lifecycle

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
knobs under `settings.lifecycleLabels` (baked) or the
`IN_PROGRESS_LABEL` / `FAILED_LABEL` / `COMPLETE_LABEL` env vars (runtime).

#### Issue Tracker backends

Per [ADR 0013](adr/0013-issue-tracker-and-code-forge-are-independent-seams.md),
the Issue Tracker (where issues live) and the Code Forge (where code and CI
live) are independent axes. `ISSUE_TRACKER` selects the tracker; the Code
Forge stays `github` regardless (Jira issues, GitHub PRs).

- **`github`** (default) — the label lifecycle described above.
- **`local`** — a private, file-based tracker; see [Local issue
  tracker](#local-issue-tracker-issue_trackerlocal) below.
- **`jira`** — dispatch state maps to the project's native workflow via a
  configurable status mapping. `JIRA_STATUS_MAPPING` is a JSON object from
  canonical dispatch state to Jira status name, e.g.:

  ```json
  { "dispatchable": "To Do", "inProgress": "In Progress", "complete": "Done", "failed": "Blocked" }
  ```

  `TransitionState` performs the matching workflow transition. When a state
  has no entry in the mapping, or the mapped transition is not available on
  the issue's current workflow (its next-status editor screen doesn't offer
  it), the launcher **falls back to swapping the same lifecycle label**
  (`ready-for-agent` / `agent-in-progress` / `agent-complete` / `agent-failed`,
  same knobs as the `github` tracker) so the lifecycle always makes progress
  even on an unmapped or restrictive workflow. `ListIssues` matches either the
  mapped status or the fallback label, so issues stuck on the label are never
  lost, and orders results by Jira's `created` timestamp (the canonical order
  for this backend, in place of GitHub's issue-number order).

  Dependencies resolve from **native Jira issue links** (the built-in
  `Blocks` link type's "is blocked by" direction) rather than prose parsing —
  unlike `github`'s `## Blocked by` / `depends on #N` body conventions.

  By default the agent's prompt input is the issue's summary and description
  only; set `JIRA_INCLUDE_COMMENTS` (non-empty) to also append the comment
  thread — opt-in, to keep the prompt-injection surface tight.

  Config: `JIRA_BASE_URL` (site base URL), `JIRA_PROJECT_KEY`, and
  `JIRA_STATUS_MAPPING` / `JIRA_INCLUDE_COMMENTS` are non-secret, set via
  `settings.repository` / `settings.lifecycleLabels` / `settings.issueDiscovery`
  (baked) or their env vars (runtime) — see the [flake options
  reference](flake-options.md). `JIRA_TOKEN` is a secret env var alongside
  `GH_TOKEN`: a Jira API token used alone as a Bearer PAT (Server/Data
  Center), or paired with the non-secret `JIRA_EMAIL` for Basic auth (Cloud).
  `spindrift doctor`'s `Probe()` check validates Jira auth and reachability
  independently of the GitHub Code Forge probe.

#### Merge guard

Between CI going green and the merge itself, the launcher checks the PR's
changed file paths against `MERGE_GUARD_PATHS` — a comma-separated glob list,
matched against every added, modified, and deleted path. A hit **downgrades
that merge to manual, regardless of `MERGE_MODE`**: no merge happens, a PR
comment names the matched path(s) and the knob, and the issue lands at
`agent-complete` with a merge-blocked-style note — the same outcome as a
merge failure after green, never a demotion to `agent-failed`. The guard
downgrades, it never blocks: the cost of a hit is one human read.

The default is:

```
.github/**,**/CLAUDE.md,**/AGENTS.md,.claude/**,.opencode/**
```

— CI config plus the instruction surface (`CLAUDE.md`, `AGENTS.md`,
`.claude/`, `.opencode/`). Those files are a cross-run persistence vector: a
poisoned instruction file merged once feeds every future Agent as trusted
input on its next fresh clone, so the default set is deliberately broad.
Setting `MERGE_GUARD_PATHS=""` disables the guard entirely — an explicit
opt-out; the operator owns the consequences.

The changed-path list is read **host-side**, the same way the merge gate
reads CI state — never from anything the Box produced — so an injected
Agent following its normal flow cannot make the guard see a clean diff. It
does not, however, defend against a fully adversarial Agent: the GitHub
token that opens the PR is the same token that can merge it, so an agent
willing to `gh pr merge` its own PR can bypass the launcher-side check
entirely. See [ADR 0016](adr/0016-merge-guard-bounds-drift-not-adversaries.md)
for that boundary and the two-actor-separation hard mode.

The guard exists **only on the `github` Code Forge merge path**. The
push-only `git` forge has no launcher in the merge path and therefore no
guard at all.

Configure it via `settings.branches.mergeGuardPaths` (baked) or the
`MERGE_GUARD_PATHS` env var (runtime) — see the [flake options
reference](flake-options.md) for the full knob surface.

#### Create the labels on the Target repo

`gh issue edit` cannot invent a label, so all four must already exist on the
Target repo. `spindrift doctor` checks this and, in interactive mode, offers to
create any missing labels. To create them manually:

```sh
gh label create ready-for-agent   --repo owner/repo --color 0e8a16 --description "dispatch to a spindrift agent"
gh label create agent-in-progress --repo owner/repo --color fbca04 --description "a spindrift Box is working this issue"
gh label create agent-complete    --repo owner/repo --color 5319e7 --description "the PR was merged by the launcher's merge gate"
gh label create agent-failed      --repo owner/repo --color b60205 --description "the Box failed or the PR could not merge; needs triage"
```

#### Caveat: a killed launcher can strand an issue

The label swaps are best-effort. If the launcher is killed mid-run (Ctrl-C, a
crashed host, a laptop closing) an issue can be left in `agent-in-progress` with
no container running. The next `spindrift dispatch` **reconciles automatically**
for the common case: it adopts any `agent-in-progress` issue that already has an
open non-draft PR and re-runs the merge gate on it. What it cannot recover on its
own is an issue stranded *before* a PR was opened (or with only a draft PR) —
there is nothing to adopt, and the `LABEL` query skips it. The unstick there is a
**manual label flip**: move it back to `ready-for-agent` to re-dispatch (or to
`agent-failed` to park it).

```sh
gh issue edit <n> --repo owner/repo --add-label ready-for-agent --remove-label agent-in-progress
```

### Local issue tracker (`ISSUE_TRACKER=local`)

`ISSUE_TRACKER=local` swaps the GitHub-backed Issue Tracker for a private,
file-based one (per [ADR 0013](adr/0013-issue-tracker-and-code-forge-are-independent-seams.md)):
issues are Markdown files with YAML frontmatter in `LOCAL_ISSUES_DIR` (default
`.spindrift/issues/`, git-ignored by default — see the template's
`.gitignore`), scanned host-side by the launcher. There is no webhook, no CI
trigger, and nothing about a local issue is ever published; this is how a
solo developer drives agents from private breakout issues without polluting a
shared tracker. The Code Forge (PR/CI/merge, or push-only) is a separate axis
and still needs a real git remote — pair `ISSUE_TRACKER=local` with a `git`
Code Forge for the fully private loop, or with `github` to keep opening PRs
against a real repo while keeping the issue backlog itself private.

Each issue is one file, named `<slug>.md`, where `<slug>` is the issue's ID
(used anywhere the GitHub backend would use an issue number — dependency
refs, branch names, log file names):

```markdown
---
title: Fix the thing
state: ready-for-agent
labels: [bug, priority-high]
created: 2026-07-09T12:00:00Z
parent: some-upstream-slug
---
## What to build

...

## Blocked by

- some-other-issue-slug
```

- `title`, `labels`, `created` (RFC 3339) mirror the GitHub adapter's fields.
- `state` is the dispatch-state marker the launcher swaps in place —
  `ready-for-agent` / `agent-in-progress` / `agent-complete` / `agent-failed`
  by default (same names as `LABEL`/`IN_PROGRESS_LABEL`/`COMPLETE_LABEL`/
  `FAILED_LABEL`, which still apply — the local adapter uses them as the
  frontmatter value instead of a GitHub label).
- `parent` is optional and purely informational; the local tracker is
  standalone — any linkage to an upstream tracker is out of scope (ADR 0013).
- **Canonical order is ascending `created`** — the local analogue of GitHub's
  ascending issue-number order.
- **Dependencies** come from a `## Blocked by` section: one issue slug per
  bullet, no `#N` refs (local issues aren't numbered).
- `spindrift doctor`'s label-presence check always passes for the local
  adapter — there is no separate label registry to check; the four dispatch
  markers above always exist as values the `state` field can take.

---

## Security

### GitHub token permissions

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

### Threat model

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
   as escalated trust. See the [token permission table](#github-token-permissions)
   above.

---

## Building on macOS

OCI images are Linux-only, so the `agent-image` package is a *Linux* derivation
even on a Mac. The launcher commands (`spindrift build`/`dispatch`) are native
and only *reference* the image path, so `nix flake check` never forces a Linux
build. Realizing the image is `spindrift build`'s job, and it handles the Mac
case for you:

- **Out of the box**: with no Linux builder, `spindrift build` builds the image
  inside an **ephemeral Nix container** on your `podman`/`docker` runtime (the
  machine that can *run* the Box can always *build* it), reusing a named `/nix`
  volume so rebuilds are incremental. Nothing to configure beyond the runtime
  you already need — just run it from your Consumer flake's directory.
- **Faster with a real Linux builder** (skips the container round-trip):
  - **nix-darwin**: enable `nix.linux-builder.enable = true;` (a small Linux VM
    nix uses automatically). `spindrift build` then realizes the image directly.
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
both `mkHarness.nix` and `docs/reference.md`:

```bash
podman pull docker.io/nixos/nix:latest
podman image inspect --format '{{index .RepoDigests 0}}' nixos/nix
# → docker.io/nixos/nix@sha256:<new-digest>
```

---

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

---

## Design notes & ADRs

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

See [`docs/adr/`](adr/) for the full architectural decision records (0001–0012),
including the Go launcher ([ADR 0007](adr/0007-runtime-logic-is-a-nix-built-go-binary.md)),
the pluggable OCI/bwrap runner ([ADR 0006](adr/0006-box-isolation-is-a-pluggable-runner.md)),
and nix-in-the-box ([ADR 0008](adr/0008-nix-is-a-first-class-default-in-the-box.md)).

---

## Unattended runs

`spindrift dispatch` is just a command, so wrap it however you schedule things —
`cron`, `launchd`, a systemd timer, or a CI job on a Linux runner (where the
image builds with no Linux-builder dance). In non-interactive contexts invoke the
CLI by its store path or via `nix run .#default -- dispatch` rather than relying
on a dev-shell PATH.
