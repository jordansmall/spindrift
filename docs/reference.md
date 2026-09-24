# spindrift reference

Full technical reference for the spindrift harness. For first-time setup see
the [README](../README.md); for vocabulary see [`CONTEXT.md`](../CONTEXT.md).

---

## The `spindrift` CLI

`nix develop` (or `direnv allow`) puts a single `spindrift` binary on your PATH
— it is the primary surface, and everything runs through it:

| command                          | what it does                                                                    |
| -------------------------------- | ------------------------------------------------------------------------------- |
| `spindrift dispatch`             | launch one container per `ready-for-agent` issue, in dependency waves; exits with a distinct exit code (6) before claiming any issue if the required config-check tier fails, rather than silently marking issues agent-failed (issue #2568) |
| `spindrift dispatch 42 57`       | dispatch exactly these issues, bypassing the label/barrier gates                |
| `spindrift dispatch my-slug`     | same, for a local-tracker slug ID — see [Local issue tracker](#local-issue-tracker-issue_trackerlocal) |
| `spindrift dispatch --no-build`  | fail fast if the image is absent instead of building it first (split build/run) |
| `spindrift dispatch --yes`       | skip the confirmation prompt when dispatching unlabeled issues (alias `--force`)|
| `spindrift dispatch --continuous`| **deprecated**, superseded by [Daemon](#daemon) (issue #3547) — run dispatch as a continuous slot-refill loop; bare-flag alias for the `--continuous-dispatch` bool |
| `spindrift research`             | advise-only research dispatch: launch one container per `agent-research` issue, post a verdict comment, apply the terminal label — see [Research dispatch](#research-dispatch) |
| `spindrift research 42 57`       | research exactly these issues, same selective semantics as `dispatch <nums>`    |
| `spindrift preview [issue...]`   | dry run: show what `dispatch` would pick up, and the wave ordering               |
| `spindrift build`                | realize/load the agent image (or store closures) without running any agent      |
| `spindrift recover <issue>`      | re-run the merge gate for one issue (adopt a stranded `agent-in-progress`)       |
| `spindrift doctor`               | check configuration validity, forge credentials, repository connectivity, container runtime readiness (advisory only — issue #2561), base-branch protection (fatal if `MERGE_MODE` isn't `manual` and the base branch isn't protected, advisory otherwise — issue #2570; a fatal result is reported inline and then deferred rather than ending the report, so the recoverable-issues row, label rows, and interactive create-label offer below still run — issue #2798), and label presence — the four triage labels (required) and the seven `agent-research*`/priority/ambiguous-spec labels (ADR 0022, ADR 0040, ADR 0041, advisory only); every failing row rendered through `Reporter.Results` (`cmd/launcher/internal/doctor/report.go`), plus the failing label rows, which share the same `rowPrefix` helper, is prefixed `MISSING:` when its Tier is Required or `advisory:` when its Tier is Advisory (every line written outside those two paths spells its own prefix or none — for example the launch-gate rows below, the runtime row, the label-section summary and label-creation lines, the label-section error a deferred repo-state failure re-prints, and the connectivity row the fail-fast path drops from the printed set, whose `remedy:` line, when it has one, is all that reaches stdout), so a fatal gap reads apart from an informational one without waiting for the aggregate summary line (issue #2723, issue #3362) — the one exception is a Required-tier check whose probe couldn't determine the answer (say, a permission error reaching the thing being probed, rather than a definitive absence): that row still renders `advisory:` and does not fail the run, because doctor never reports a hard failure it isn't sure of (issue #2962); when run interactively (TTY attached) and labels are missing it offers to create them, with the prompt itself stating the required/advisory tier counts and what declining each means; in CI (no TTY) missing required labels are fatal; it also reports the same required-knob/driver/cross-knob checks `dispatch` gates on (issue #2559), one status line per check (plus a remedy line for a failing one, unless its remedy just repeats the error); a distinct exit code per failure class (below) and, on any failure, a stderr summary that stands alone even with stdout redirected (issue #2569); when the configured runner kind (`RUNNER_KIND`) is bwrap, it also reports three capability checks — `bwrap-overlay-support`, `bwrap-network-isolation`, `bwrap-cgroup-delegation` — each Tier mirroring the real launch-time gate for the current config, reporting Required vs Advisory severity for operator visibility (these rows are informational only and never affect `spindrift doctor`'s exit code), except `bwrap-cgroup-delegation`, which is always Advisory since bwrap degrades gracefully without cgroup delegation (ADR 0042, issue #2671); for every non-bwrap runner kind (never under bwrap) it also reports a `podman-machine-memory` row, Required tier — unlike the bwrap rows above, which stay informational, this row's failure does affect `spindrift doctor`'s exit code: it is classified alongside the required-knob checks, so a podman machine sized below `MEMORY_LIMIT` × `MAX_PARALLEL` plus a fixed 512MiB VM overhead fails `spindrift doctor` with exit 2 (issue #3544) — the VM's own OOM-killer fires before any single container's `--memory` cgroup cap ever bites — and reporting `not applicable` rather than a failure for a non-podman runtime, an empty `MEMORY_LIMIT` (the schema's deliberate opt-out), or no active podman machine, with a remedy naming all three fixes (lower `MAX_PARALLEL`, raise the machine's RAM, or lower `MEMORY_LIMIT`) alongside the exact `--memory` figure that would satisfy it (issue #3537); it also renders the ordered launch-gate registry (issue #2942) as five `ok: <name>` / `MISSING: <name>: <error>` pass/fail rows — `read-only-capability`, `network-mode-runtime`, `signal-carrier-network-mode` (the gate that refuses `BOX_SIGNAL_CARRIER=socket` under `NETWORK_MODE=none`, the one pairing the socket carrier can never work under — see [Signal socket](#signal-socket-box_signal_carriersocket)), `read-only-token-github`, `read-only-token-forgejo` — the same gates `dispatch`'s bootstrap and `preview` enforce before launching a Box; when `REGISTRY_PROXY_ROUTES_FILE` (ADR 0045) is set it also reports a `registry-route-credential[<match-host>]` and a `registry-route-origin[<match-host>]` row per declared route, so a broken credential or a malformed `upstream-origin` names the offending route directly; it also reports an advisory `registry-route-drift` row reporting any host the repo names that no route covers (ADR 0045) — under `CODE_FORGE=local` sourced from the Accumulation repo's own `BASE_BRANCH` ref (ADR 0033), the same dispatch snapshot a Box would clone, skipping the row when that repo or ref isn't available; under every other Code Forge, only when invoked inside a checkout whose `origin` remote matches the configured Target repo (the same-repo/dogfood case) (issue #3311) — see [Registry route discovery](#registry-route-discovery); and it also reports a `registry-proxy-transport` row naming which transport (`unix socket` or `tcp`) a dispatch would use to reach the launcher-side registry proxy, probed through the identical seam a dispatch itself calls (issue #3111) so the reported answer can't drift from what a real dispatch does — `not configured`, with no probe run at all, when `REGISTRY_PROXY_ROUTES_FILE` is unset, and always Advisory, since both transports are working outcomes (issue #3114); and finally, last of all the rows, when `BOX_SIGNAL_CARRIER=socket` (never under `log`, where the row does not print at all) it reports a `signal-socket-transport` row naming the same transport verdict (`unix socket` or `tcp`) probed through the identical seam a dispatch uses for its Signal socket, always Advisory so it never affects the exit code, degrading under `NETWORK_MODE=none` regardless of transport (no probe run at all), under a TCP verdict paired with `NETWORK_MODE=no-host-loopback`, or on an indeterminate probe — see [Signal socket](#signal-socket-box_signal_carriersocket); quiet by default (issue #3777): a healthy run writes nothing to stdout and exits 0, and without the flag the only things that still print are `MISSING:` rows and their `remedy:` lines, the interactive create-label prompt (with the advisory labels it offers re-listed immediately above it, since their own rows were suppressed), the `created:` lines, and the still-missing-after-creation lines; `ok:` rows and `advisory:` rows — including a Required row demoted to advisory by an indeterminate probe (issue #2962) — print only under `--verbose` or its short form `-v`, as do the read-only token gates' (`read-only-token-github`, `read-only-token-forgejo`) own `WARNING:` lines on a passing gate under `BOX_FORGE_AND_ISSUE_ACCESS=read-only` when the Box token's write capability couldn't be introspected — suppressed only in this report, since the same gates still print those lines during `dispatch`'s and `preview`'s own enforcement, which carries no quiet mode of its own — and together `--verbose`/`-v` reproduce the full pre-#3777 report; exit codes and the on-failure stderr summary are unchanged, so a redirected stdout still loses nothing; an unrecognised flag or argument is a usage error on stderr with exit 1 |
| `spindrift reconcile`            | local-tracker bookkeeping sweep: close issues whose recorded `landing` PR merged (ADR 0029) — a clear no-op on `github`/`jira`; also auto-invoked at the end of a `dispatch` run when `ISSUE_TRACKER=local` — see [`reconcile`: closing a local issue](#reconcile-closing-a-local-issue) |
| `spindrift registry discover <repo-dir> <routes-file>` | write a registry routes file (ADR 0045) by scanning the Target repo's own committed registry config, setup-time only, by the operator — see [Registry route discovery](#registry-route-discovery) |
| `spindrift --help`               | concise usage: subcommands, common flags, and pointers to the full reference    |
| `spindrift --help --all`         | the full flag reference, grouped by category (same content as `man spindrift`)  |
| `man spindrift`                  | the manual page (installed alongside the binary on your PATH)                    |
| `spindrift --version`            | installed version and revision                                                  |

**`spindrift doctor` exit codes** (issue #2569). A scriptable, distinct code
per failure class — never a flat 0/1 — so automation can branch without
parsing stderr strings; on any non-zero exit, stderr alone (independent of
stdout, which by default carries only the `MISSING:` rows and the other
still-printed passthrough lines, or the full `ok`/`MISSING`/`advisory` report
under `--verbose`/`-v` — issue #3777) explains the
failure, and, for missing labels, names every missing label. A configuration
problem (exit 2) enumerates every simultaneously-broken required knob, not
just the first — none of those checks are network probes, so doctor runs
every one instead of stopping at the first failure the way `dispatch`'s own
fail-fast gate does, and each enumerated required-row failure carries its
row's `remedy:` line too, subject to the same
suppress-if-it-repeats-the-error rule —
`dispatch`'s own fail-fast configuration abort carries that remedy line as
well (issue #2886); the connectivity probes (exit 3) stay fail-fast, since
each is a live network call and a later one is moot once an earlier one has
already failed; the fail-fast scope stops there, though — once connectivity
is confirmed, the branch-protection and recoverable-issue rows both always
run, and a failing branch-protection row is reported inline and then
*deferred* rather than returned immediately, so the
recoverable-issue count, the label rows, and the interactive create-label
offer all still run in the same invocation, letting a first-time-setup
operator fix the labels from the same run that told them the base is
unprotected; the run still exits non-zero for the unprotected base, with the
deferred branch-protection (or recoverable-issue) failure taking precedence
over any later failure in the same run — a missing-label failure or a later
connectivity failure (exit 3) alike — with that later failure still reported
on stdout rather than silently dropped (issue #2798). A bare deferred branch-protection
failure carries no exit-code sentinel of its own, so today it lands on exit 1
("reserved for internal/unclassified errors" below) rather than a
dedicated code:

These codes are scoped to `doctor` alone and deliberately disagree with the
numbers `dispatch`'s own exit codes use for its unrelated exit 2/3/4 (queue
empty, none dispatchable, image stale; see [Dispatch exit
codes](#dispatch-exit-codes)) — the two vocabularies are never compared to
each other, so the collision is not a conflict to resolve.

| exit | meaning |
|------|---------|
| 0    | healthy — required checks passed; advisory findings (missing research/priority/ambiguous-spec labels, runtime not ready, etc.) are allowed, and their rows print only under `--verbose`/`-v` — except the advisory labels re-listed above the interactive create-label prompt and the still-missing-after-creation lines, which print either way (see the `spindrift doctor` row above) |
| 1    | reserved for internal/unclassified errors |
| 2    | configuration invalid — the same required-knob/driver/cross-knob validation `dispatch` gates on, minus runtime readiness (advisory here, per exit 0 above, even though `dispatch` itself still requires it before launching a Box); also fires when `podman-machine-memory` fails (the podman machine is undersized for `MEMORY_LIMIT` × `MAX_PARALLEL`, issue #3544), since that row is classified alongside the required-knob checks |
| 3    | auth or connectivity — the issue tracker or code forge could not be reached, or a work-tier label create call failed (an advisory-tier label create failure does not fail the check and still exits 0; its row prints only under `--verbose`/`-v`) |
| 4    | required checks failed or declined — one or more of the four triage labels are missing and were not created, whether declined at the interactive prompt, missing in non-interactive (CI) mode, or still missing after a create attempt |

Every runtime knob is also a `--flag`. Precedence is **flag > flake `settings`
> baked default** (ADR 0020): nix renders the resolved `settings` values (plus
image/agent-file artifacts) into one Launcher input document, passed to the
binary via `--input`; an explicit flag overrides it. A knob env var still
wins over the flake setting this release — deprecated, and the launcher warns
with the flag/settings equivalent when it finds one — but env's role shrinks
to secrets and internal launcher→Box plumbing from here on; see
[`MIGRATING.md`](../MIGRATING.md).
`spindrift --help` stays scannable; the full generated table lives in
`man spindrift` (and `spindrift --help --all` for the same thing in the
terminal). Bare `spindrift` with no subcommand — or an unrecognized
subcommand — prints this concise help instead of dispatching; `dispatch`
remains the sole way to drain the work queue, `research` the sole way to
drain the research queue.

> **Removed in v0.5.0 (see [`MIGRATING.md`](../MIGRATING.md)):** `nix run
> .#run` and `nix run .#build` no longer exist as flake outputs; use
> `spindrift dispatch` / `spindrift build` instead.

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

`mkHarness` is the engine; `perSystem.spindrift.*` is a flake-parts shim that
exposes most — not all — of its arguments as declared options (`lib/flakeModule.nix`).
The NixOS module system rejects undeclared options, so the **scope** column
below marks which knobs only exist as `mkHarness` function arguments. Unset
shared options fall through to `mkHarness`'s own defaults. `system` is neither
a declared option nor an `mkHarness`-only argument: it's **auto-supplied** by
flake-parts itself and passed through, so setting
`perSystem.spindrift.system` errors the same way an `mkHarness`-only option
would. For the options the shim does declare, the `spindrift` CLI it
produces is byte-identical to the one from the equivalent direct
`mkHarness` call — the shim forwards them into that same call, and two
checks compare the two resulting store paths for the configs they
instantiate; that guarantee covers the CLI, not every app a harness
exposes.

The table below documents `mkHarness`'s named *parameters*, plus `settings`
— a **flake-module only** option with no `mkHarness` parameter counterpart
(`mkHarness` takes the domain-tree knobs through its `defaults` argument
instead) — together making up the versioned Consumer contract (ADR 0010).
The function's *return value* is a separate surface: only `image`,
`spindrift`, `packages`, and `apps` are in-contract, and everything else
lives under the out-of-contract `internals` attrset (see [Calling
`mkHarness` directly](#calling-mkharness-directly)).

<!-- BEGIN GENERATED OPTION SURFACE TABLE -- nix run .#regen -- DO NOT EDIT -->
| option      | domain path | scope          | type                        | default            | meaning                                                              |
| ----------- | ----------- | -------------- | --------------------------- | ------------------ | -------------------------------------------------------------------- |
| `nixpkgs`   | `perSystem.spindrift.infra.nixpkgs` | shared         | flake input                 | your `nixpkgs`     | locked nixpkgs the image and host commands build from                |
| `system`    | — | **auto-supplied** | string                   | perSystem's system | your host system; mapped to its Linux twin for the image; see the note above (`lib/flakeModule.nix`) |
| `overlays`  | `perSystem.spindrift.infra.overlays` | shared         | list                        | `[]`               | overlays applied to the instantiated nixpkgs                         |
| `config`    | `perSystem.spindrift.infra.config` | shared         | attrs                       | `{ allowUnfree = true; }` | nixpkgs config attrs                                          |
| `packages`  | `perSystem.spindrift.infra.image.packages` | shared         | `pkgs -> [pkg]`             | `[]`               | project build/test tools baked into the image (the toolchain surface)|
| `prefetch`  | `perSystem.spindrift.infra.image.prefetch` | shared         | shell snippet               | `""`               | runs in the work tree after the clone, to warm dependency caches     |
| `prompt`    | `perSystem.spindrift.agents.prompt` | shared         | string                      | bundled starter    | agent prompt template baked into the image; changing it requires a rebuild (`spindrift build`). The SPINDRIFT_OUTCOME contract is harness-owned: `spindrift build` appends it automatically if a custom `prompt` omits it (idempotent — a prompt that already has it is untouched) |
| `scoutPrompt` / `reviewPrompt` / `filerPrompt` | — | **`mkHarness` only** | string | bundled starters | system prompts for the read-only scout and reviewer subagents and the opt-in filer subagent (see [Filer](#filer)); not settable on `perSystem.spindrift.*` — override at runtime via `SPINDRIFT_PROMPT_DIR` regardless of which caller baked the image |
| `skills`    | `perSystem.spindrift.agents.skills` | shared         | list of path/derivation/`{ name; src; }` | `[]`  | skills baked into the image at the fixed `/agent/skills` path alongside the harness-owned skills (e.g. `auto-format`, `auto-lint`, `check-hygiene`, `code-comments`, baked regardless of this list), each as a `<name>/SKILL.md` directory (the only layout Claude Code discovers — a flat `<name>.md` is ignored) so the headless agent can `/invoke` them; a `{ name; src; }` content entry (name + SKILL.md body) is realized with the image's own Linux `pkgs` rather than copied from a pre-built host derivation, keeping the agent-image drvPath host-independent (issue #597); `agent/entrypoint.sh` copies both into the Driver's actual runtime skills dir at box startup, then copies `SPINDRIFT_SKILLS_DIR` (staged at `/operator-skills`) over the top so runtime overrides win without erasing the baked set (issue #2489) |
| `settings`  | — | **flake-module only** | submodule, grouped by section (see below) | `{}` | **deprecated** compat shim (ADR 0037): every schema-generated knob is now a first-class option in the domain tree below; this submodule still works pre-1.0 (forwards with an eval warning) but new config should use the domain paths directly |
| `runtime`   | `perSystem.spindrift.infra.runtime` | shared         | `"podman"` \| `"docker"` \| `"rancher"` \| `"bwrap"` | `"podman"` | runner the `spindrift build`/`dispatch` commands drive: an OCI runtime (`"rancher"` is an alias for Rancher Desktop's containerd mode, driven via `nerdctl`), or the daemonless bubblewrap sandbox (`bwrap`, Linux-only, no image build/load) |
| `driver`    | `perSystem.spindrift.agents.driver` | shared         | string                      | `"claude"`         | the agent CLI Driver baked into the image and threaded to the launcher (ADR 0009); `"claude"` (default) and `"opencode"` are the Drivers today. A non-`claude` Driver realises its own `spindrift-<driver>` image (e.g. `spindrift-opencode`) so per-Driver artifacts never collide |
| `nixInBox`  | `perSystem.spindrift.infra.nix.inBox` | shared         | bool                        | `true`             | bake a usable nix (binary + registered store DB + sandbox-off, `cores = 4`-bounded config) into the box so `nix flake check` / `nix develop` work inside it; set `false` for a lean, nix-free image (ADR 0008) |
| `nixStoreWritable` | `perSystem.spindrift.infra.nix.storeWritable` | shared  | bool                 | `false`            | self-test mode (ADR 0018): make `/nix/store` itself (not its existing contents) agent-writable so in-box `nix flake check` can substitute/build new paths instead of hitting EACCES; new paths live only in the container's ephemeral copy-on-write layer. Not hermetic — the entrypoint prints a loud `==> WARNING`; both runners support it (ADR 0042) — bwrap overlays an ephemeral tmpfs upper on the store instead |
| `extraClosures` | `perSystem.spindrift.infra.image.extraClosures` | shared     | `pkgs -> [pkg]`         | `[]`               | extra derivations, as a function of the (Linux) `pkgs` (like `packages`), whose closures are baked into the image and registered in the store DB alongside the runtime closure, so in-box nix sees them as already present (ADR 0018) |
| `nixBuilderImage` | — | **`mkHarness` only** | string        | `"docker.io/nixos/nix@sha256:bf1d938835ab96312f098fa6c2e9cab367728e0aad0646ee3e02a787c80d8fb8"` (pinned reference — the real default lives in `lib/build-constants.nix`) | Nix image `spindrift build` uses as a fallback Linux builder when the host can't realize the image; pinned by digest for supply-chain safety (see [Building on macOS](#building-on-macos)) |
| `roster`    | `perSystem.spindrift.agents.models.roster` | shared         | list of subagent-entry attrs | `lib/roster.nix`'s `defaultRoster` | supersedes the four legacy model knobs; see [Subagent roster](#subagent-roster) |
| `byName`    | `perSystem.spindrift.agents.models.byName` | shared         | attrset of `{ model?; effort?; }` keyed by roster entry name | `{}` (this row is the `mkHarness` parameter; the flake option, `agents.models.byName`, defaults to `null`) | name-keyed model/effort shorthand (issue #2560), forwarded into `defaultRoster`; only takes effect when `roster` is unset; no flat `perSystem.spindrift.byName` alias — see [Subagent roster](#subagent-roster) |
<!-- END GENERATED OPTION SURFACE TABLE -->

Every knob settable on `perSystem.spindrift.*` — schema-generated run
default or hand-declared structural option alike — is a first-class option
directly under `perSystem.spindrift.<domain>.*` (ADR 0037): `agents`,
`git`, `issues`, `forge`, `dispatch`, `infra`. Every remaining row above
has no domain path, across three different reasons: the **`mkHarness`
only** rows (the combined `scoutPrompt`/`reviewPrompt`/`filerPrompt` row
and `nixBuilderImage`) aren't reachable from the flake module at all;
**auto-supplied** `system` binds to flake-parts' own per-system value, not
a domain-tree knob; and **flake-module only** `settings` is itself the
deprecated compat shim (ADR 0037) that forwards *into* the domain tree
below, so it has no domain path of its own to show. The flake surface bakes
run knobs into the Launcher input document the `spindrift` CLI passes to
the launcher binary via `--input`; an explicit `--flag` at dispatch time
re-points a value without a rebuild. Domain names are the same headings as
`spindrift --help --all`, so the flake surface stays in lockstep with the
CLI help. Unknown domain or knob names are rejected at eval time by the
NixOS module system.

Every option on the domain tree — schema-generated or structural alike —
has its own entry in [`docs/flake-options.md`](flake-options.md), with its
type, default, and description. The old `settings.<section>.<knob>`
spelling still works pre-1.0 as a deprecated alias (`lib/flakeModule.nix`'s
`settingsOption`, forwarded with an eval warning) but is retired at the 1.0
boundary — new config should use the domain paths directly.

Each binding below sets a domain-tree path under your Consumer flake's own
`perSystem.spindrift`:

```nix
# BEGIN GENERATED SETTINGS EXAMPLE LABELS -- nix run .#regen -- DO NOT EDIT
issues.labels.dispatch   = "ready-for-agent";
issues.labels.inProgress = "agent-in-progress";
issues.labels.failed     = "agent-failed";
issues.labels.complete   = "agent-complete";
# END GENERATED SETTINGS EXAMPLE LABELS
# BEGIN GENERATED SETTINGS EXAMPLE CONFIG -- nix run .#regen -- DO NOT EDIT
git.baseBranch         = "main";
git.branchPrefix       = "agent/issue-";
git.merge.policy       = "manual";
git.merge.guardPaths   = ".github/**,.forgejo/**,**/CLAUDE.md,**/AGENTS.md,.claude/**,.opencode/**";
git.merge.pollInterval = 30;
git.merge.pollTimeout  = 3600;
dispatch.maxParallel   = 3;
dispatch.maxJobs       = 0;
# END GENERATED SETTINGS EXAMPLE CONFIG
# BEGIN GENERATED SETTINGS EXAMPLE MODELS -- nix run .#regen -- DO NOT EDIT
agents.models.default = "claude-sonnet-5";
agents.models.scout   = "claude-haiku-4-5-20251001";
agents.models.review  = "claude-opus-5";
agents.models.filer   = "";
# END GENERATED SETTINGS EXAMPLE MODELS
git.user.name  = "bot";
git.user.email = "bot@example.com";
dispatch.retry.maxFix           = 3;
dispatch.retry.maxRebase        = 3;
dispatch.retry.holdJitter       = 5;
dispatch.retry.transientBackoff = 30;
dispatch.retry.transientMax     = 3;
infra.devShell.name         = "default";
infra.devShell.probeTimeout = 300;
infra.limits.memory  = "5g";
infra.limits.pids    = "512";
infra.network.podman = "";
infra.network.bwrapUnshare = false;
forge.repoSlug = "owner/repo";
```

#### Discovering flake options

Three paths to discover which options exist and what they do:

1. **Generated reference** — [`docs/flake-options.md`](flake-options.md) lists
   every flake option: schema-generated knobs grouped by domain, with their
   env var, default, and description, followed by a structural-options
   section covering the rest, with their type, default, and doc but no env
   var, since they have none. Both classes are generated and drift-guarded
   by `nix flake check`, so the reference is always in sync.

2. **LSP autocomplete** — `nixd` and `nil` read the module option declarations
   that `lib/flakeModule.nix` generates from the same schema.  Opening your
   Consumer flake in an editor with either LSP gives option completions and hover
   documentation for every flake option inline.

3. **CLI reference** — `spindrift --help --all` (or `man spindrift`) prints the
   full flag table grouped by domain.  Every schema-generated flake option
   maps 1:1 to a `--<flag>` in the same domain heading, so the CLI reference
   doubles as a guide to what is settable in the flake for schema-generated
   knobs. Structural options (`roster`, `skills`, `driver`, ...) are flake-only
   and have no CLI flag counterpart; conversely some flags (e.g.
   `--skills-dir`) have no flake option at all.

`issues.labels.inProgress`/`issues.labels.failed`/`issues.labels.complete`
drive the [label lifecycle](#how-a-run-works); `git.merge.policy` is the
post-green [merge policy](#how-a-run-works)
(`manual`/`immediate`/`auto`); `git.merge.guardPaths` is the [merge
guard](#merge-guard)'s glob list, downgrading a green PR to manual
regardless of `git.merge.policy` when it touches a guarded path;
`agents.models.default` is the Claude model the in-container implementor
agent runs, threaded into the container as `MODEL` so `MODEL=...` switches
models at runtime with no image rebuild. `agents.models.scout`/
`agents.models.review` tier the read-only scout and reviewer subagents the
same way; each is composed into `--agents` independently by its own knob,
so emptying one drops only that subagent, never both.
`agents.models.filer` is the same shape but opt-in — empty by default, so
the filer is not provisioned at all until a model is set; see
[Filer](#filer). `agents.models.scout`/`agents.models.filer`/
`agents.models.worker` are all **deprecated** in favor of the structural
[`roster`](#subagent-roster) option. `agents.models.review` is
**deprecated for non-orchestrator use** only — superseded by `roster`
there, but under `ORCHESTRATOR` the roster reviewer entry is itself
superseded by the code-owned review pass, which binds its model from
`agents.models.review` instead (falling back to the coordinator model when
unset).

#### Subagent roster

`roster` (issue #264) is a *structural* option, declared as a real
`mkOption` in `lib/flakeModule.nix`, that a flakeModule Consumer sets
directly at its flake path:

<!-- BEGIN GENERATED ROSTER FLAKE PATH -- nix run .#regen -- DO NOT EDIT -->
`perSystem.spindrift.agents.models.roster`
<!-- END GENERATED ROSTER FLAKE PATH -->

`mkHarness` also takes a `roster` argument, forwarded from the Consumer's
value only when the Consumer sets it (`lib/flakeModule.nix`) — unlike
`scoutPrompt`/`reviewPrompt`/`filerPrompt`/`nixBuilderImage`, which
are declared only as `mkHarness` arguments with no flake-module path at
all, `roster` is reachable from the flake module itself, and appears in
[`docs/flake-options.md`](flake-options.md) with its own entry, the same
as every other domain-tree option.
`roster` takes a list of subagent entries, each shaped `{ name; model; mode;
description; tools; promptFile; prompt; effort }`. It supersedes the four fixed
`scoutModel`/`reviewModel`/`filerModel`/`workerModel` args: instead of one
knob per hardcoded agent, a Consumer flake can pass any number of roster
entries, including a custom Nth agent beyond the historical four. When
`roster` is omitted, `mkHarness` falls back to `lib/roster.nix`'s
`defaultRoster`, built from the four legacy model args (or their
`agents.models.*` defaults) and reproducing today's
scout/reviewer/filer/worker composition exactly, plus a fifth `review-axis`
entry (issue #3447) that has no legacy model arg of its own — see
[ADR 0049](adr/0049-role-capability-profiles-are-provider-neutral.md).

Every roster — explicit or default — passes through `lib/roster.nix`'s
`normalizeRoster` before either Driver renders it, and `normalizeRoster` is
strict (issue #2571): the entry key set is closed, so an unrecognized key
throws at eval time the same way an unknown `defaults` key does. `model`
must be present as a *key* on every entry — an explicit `model = ""` is
still the supported opt-out for dropping that entry (#392, below), but
omitting the `model` key entirely throws. `normalizeRoster` itself only
validates: it never drops the `model = ""` entry, which passes through as
an ordinary, valid entry. The drop is a separate step —
`rosterLib.dropOptedOut` — that `lib/mkHarness.nix` applies once, right
after `normalizeRoster` succeeds and before either Driver ever sees the
roster (see below). `promptFile` must resolve to a
real file on disk under `templates/default/prompts/`, or the entry must
carry a non-null, non-empty inline `prompt`; an entry with neither — a
typo'd `promptFile` and no inline `prompt` — throws instead of silently baking an
agent with no resolvable prompt (issue #2555 user story 23). This applies
to hand-authored `roster` entries as much as to `defaultRoster`'s own
built-in entries, though in practice those always ship a real
`promptFile` under `templates/default/prompts/`.

This promptFile check runs at eval time against the checked-in
`templates/default/prompts/` directory only, so it cannot see a file that's
only ever supplied via a *runtime* prompt-dir override
(`SPINDRIFT_PROMPT_DIR` env var / `--prompt-dir` flag /
`perSystem.spindrift.agents.promptDir` flake option — see the
`SPINDRIFT_PROMPT_DIR` row further down and the
`scoutPrompt`/`reviewPrompt`/`filerPrompt` row above): that directory
doesn't exist yet at build time. A custom roster entry whose real prompt
file lives only in a runtime prompt-dir override must instead supply an
inline `prompt` (or ship a real file under the checked-in
`templates/default/prompts/`) to pass eval.

`defaultRoster`'s own roster-native surface for setting models is its
`models` argument (issue #2426): an attrset keyed by roster entry name
(`scout`/`reviewer`/`filer`/`worker`/`review-axis`), e.g. `defaultRoster {
models = { filer = "your-model-id"; }; }`. A name absent from `models`
inherits that agent's `lib/env-schema.nix` default (issue #2434) —
the same default `mkHarness`'s no-roster fallback path resolves through
`mergedDefaults` — so `defaultRoster { }` composes spindrift's own
defaults (scout/reviewer/worker provisioned; `filerModel`'s own schema
default stays empty, so the filer is still opt-in). Setting `models.<name>
= ""` explicitly is a distinct, still-supported opt-out (#392): it drops
that entry's model (see below) even though the name would otherwise
inherit a non-empty schema default — only an *unmentioned* name inherits.
A name in `models` that isn't one of the five roster entries throws at
eval time, the same way an invalid `roster` entry name does. The four
legacy positional knobs still work as a lower-precedence fallback per
name — `models` wins over the matching legacy knob, which wins over the
schema default, when more than one is set for the same name — since
`mkHarness` resolves its deprecated
`scoutModel`/`reviewModel`/`filerModel`/`workerModel` args through
`defaultRoster` unchanged. A caller relying on the old blank-by-default
behavior for an unmentioned name must switch to explicit
`models.<name> = ""`.

`agents.models.byName` (issue #2560) is the versioned, Consumer-facing flake
option for the same name-keyed override `models` gives `defaultRoster`
callers directly:

```nix
perSystem.spindrift.agents.models.byName = {
  filer = {
    model = "claude-haiku-4-5-20251001";
    effort = "high";
  };
};
```

a three-line flake edit, no deprecation warning (unlike the legacy
`scoutModel`/`reviewModel`/`filerModel`/`workerModel` knobs documented later
in this same section). Each key must name a roster entry
(scout/reviewer/filer/worker/review-axis); each value is a *closed*
`{ model?; effort?; }` attrset — any other field (and any unknown key) fails
eval.
`mode`/`tools`/`prompt` stay roster-only (only reachable by hand-authoring a
full `roster` list), keeping this a shorthand, not a parallel roster
surface. It also carries `effort`, unlike the older `models` argument, which
is model-only. An explicit `roster` drops `byName` before its unknown-name/
unknown-field validation runs, the same way it drops `models` and the four
legacy knobs — a typo'd key or field in `byName` alongside an explicit
`roster` evaluates clean rather than throwing, since the whole shorthand is
moot once `roster` wins. It's declared as a real domain-tree option in
`lib/flakeModule.nix` (`byNameOption`/`byNameTreeEntries`), forwarded into
`mkHarness`'s `byName` arg, which threads into `lib/roster.nix`'s
`defaultRoster` the same way `agents.models.roster` threads its own
`roster` argument in — ignored when an explicit `roster` is supplied (same
precedence the legacy per-agent knobs already have, stated earlier in this
section). Per name, `models.<name>` still outranks `byName.<name>.model`
when both are set — the same precedence order `models` already has over the
legacy per-agent knobs above. `byName.reviewer.effort` is likewise
overridden, after the fact, by the `reviewEffort` legacy knob when that
knob is set (`lib/mkHarness.nix` applies `reviewEffort` as a post-processing
step on the fully resolved roster, downstream of `defaultRoster` and
everything it produces, `byName` included).

An entry with an empty `model` is dropped upstream of both Drivers, by
`rosterLib.dropOptedOut` (`lib/roster.nix`), which `lib/mkHarness.nix` applies
once right after `normalizeRoster` succeeds — the same per-agent omission the
four legacy knobs give today (#392), just resolved once before either Driver
sees the roster rather than inside each Driver's own render function.
`lib/drivers/claude.nix` renders the roster it's handed into the `--agents`
JSON flag; `lib/drivers/opencode.nix` renders it into
`.config/opencode/agents/<name>.md` files instead, one per entry, each with
`description`/`mode`/`model` frontmatter.

An entry may also set `effort` (issue #2242), a pass-through, driver-specific
effort/reasoning-level string — spindrift does not normalize it, so the value
must already be one the target driver accepts (e.g. `"high"`). The claude
Driver emits it as an `"effort"` key beside `model` in the agent's `--agents`
JSON object; the opencode Driver emits it as a `reasoningEffort:` key beside
`model:` in the agent's YAML frontmatter. Omitting `effort` omits the
key/line entirely on both Drivers — the same inherit-session-effort behavior
as today. `byName.<name>.effort = ""` is accepted, not rejected, but is not
an opt-out the way `models.<name> = ""`/`byName.<name>.model = ""` is: it
still overrides `defaultRoster`'s own per-agent default effort with an
empty string, which both Drivers then treat as "omit the key/line" the same
as never setting `effort` at all — net effect, an explicit empty string
silently drops that agent's differentiated default effort down to the
target driver's inherited session effort.

`defaultRoster` itself now ships a fixed default `effort` per agent (issue
#2386), from `lib/roster-schema-defaults.nix`'s `rosterDefaults` table:

<!-- BEGIN GENERATED ROSTER EFFORTS -- nix run .#regen -- DO NOT EDIT -->
`scout=medium/reviewer=high/filer=medium/worker=high/review-axis=high`
<!-- END GENERATED ROSTER EFFORTS -->

This is a per-name table lookup on each of `defaultRoster`'s five built-in
entries, not a `normalizeRoster`-level default — a freshly-baked image
that omits `roster` entirely (and so falls back to `defaultRoster`) runs
each subagent at a differentiated effort out of the box, with no Consumer
hand-authoring one. A hand-authored custom `roster` entry still gets no
`effort` injected; it must set `effort` itself to get anything beyond the
target driver's inherited session effort.

The legacy knobs map onto the default roster's entry names:

| Legacy knob    | Roster entry `name` |
| -------------- | -------------------- |
| `SCOUT_MODEL`  | `scout`               |
| `REVIEW_MODEL` | `reviewer`            |
| `FILER_MODEL`  | `filer`               |
| `WORKER_MODEL` | `worker`              |

`scoutModel`/`reviewModel`/`filerModel`/`workerModel` are **deprecated**: they
still work — the default roster derives from them unchanged, as a fallback
under `models` — but a `nix eval` warning fires whenever any of them is set,
whether via the `mkHarness` `defaults` argument or a Consumer's
`agents.models.*`, pointing at this section. Unlike those knobs, which
retier or add/drop a subagent with no image rebuild (`SCOUT_MODEL=...` at
dispatch time, no `spindrift build`), adding an arbitrary Nth custom agent
via `roster` is a `mkHarness`/image-time decision and requires a rebuild.

spindrift's own dogfood Consumer config (`nix/dogfood-defaults.nix`) is a
concrete `roster` user, naming only the Filer:

<!-- BEGIN GENERATED DOGFOOD FILER PIN -- nix run .#regen -- DO NOT EDIT -->
`roster = rosterLib.defaultRoster { models = { filer = "claude-haiku-4-5-20251001"; }; };`
<!-- END GENERATED DOGFOOD FILER PIN -->

Filer stays a local pin because it's opt-in by design (`filerModel`'s schema
default is empty) and the dogfood genuinely depends on it for #393's
`agent-review-finding` filing. Scout, reviewer, and worker are all
unmentioned and so inherit their `lib/env-schema.nix` defaults (issue
#2434) instead:

<!-- BEGIN GENERATED DOGFOOD MODELS -- nix run .#regen -- DO NOT EDIT -->
`claude-haiku-4-5-20251001`, `claude-opus-5` (issue #2433), and `claude-sonnet-5` respectively.
<!-- END GENERATED DOGFOOD MODELS -->

The dogfood still inherits `defaultRoster`'s built-in per-agent effort
defaults unchanged (issue #2386), and sets no separate `reviewEffort` knob
(issue #2512): the roster's `reviewer` entry's own effort is what the
orchestrator's code-owned review pass (issue #2387) runs at directly, the
same way it already does for the model (issue #2427) — one mechanism instead
of two.

The **prompt is baked into the image**: changing `prompts/issue-prompt.md`
requires an image rebuild (`spindrift build`). Point `SPINDRIFT_PROMPT_DIR` at
any directory to override it at runtime for zero-rebuild iteration. The
template an override directory replaces is internal — see
[`VERSIONING.md`](../VERSIONING.md)'s prompt-template carve-out for what that
buys you and what it does not.

Opt-in prompt steps (the skill preamble, the caveman-default narration
directive, `FILE ISSUES`, `AUTO-FORMAT`, `AUTO-LINT`, `CI FAILURE`, and the
`OPEN A PULL REQUEST` ticket-reference line) are each one row in a nix-owned
**Conditional fragment registry**
(`lib/fragments.nix`) — a gate variable, a fragment file under
`prompts/fragments/`, and a substitution variable — rendered into the
entrypoint's single fragment loop and its substitution allowlist together,
so a fragment can never reference a variable the substitution step doesn't
know about. Adding an opt-in prompt step that renders through the driver-exec
`assemble-prompt` verb (every prompt phase_prompt_assembly assembles) is a
nix-only change: one registry row plus one fragment file, no entrypoint edit.
`conflict-resolve-prompt.md` is the one exception — it renders earlier, via
`phase_conflict_resolve`'s own bash-only `_subst` call, before
`phase_prompt_assembly` ever runs, so its `CAVEMAN_STEP`/`SKILL_PREAMBLE`
gates are precomputed by a small hand-written block in `entrypoint.sh` rather
than the shared fragment loop. All instruction prose —
conditional or not — lives with the rest of the prompt surface rather than
as heredocs in the entrypoint script. `SPINDRIFT_PROMPT_DIR` therefore
overrides fragments the same way it overrides `prompts/issue-prompt.md`
itself: a directory that enables a knob (`AUTO_FORMAT`, `AUTO_LINT`, a filer
model, etc.) must ship the matching `fragments/*.md` file, exactly as it
already must ship `filer-prompt.md` when the filer is configured — the
entrypoint reads the fragment unconditionally once its gate is on, with no
baked-in fallback.

The comment-discipline rule is no longer a fragment anchor: `worker-prompt.md`
(issue #3419), and now `issue-prompt.md`, `fix-prompt.md`, and
`conflict-resolve-prompt.md` too (issue #3505), each carry the
`/code-comments` policy body inlined verbatim instead of pointing at a
`${CODE_COMMENTS_STEP}` fragment. A build-time check
(`prompt-code-comments-inlined`, `nix/checks/prompts.nix`)
pins all four against `templates/default/skills/code-comments/SKILL.md` and
fails if any of them drifts from it or still names `/code-comments` or
`CODE_COMMENTS_STEP`. The `/code-comments` skill itself still bakes into
every image and stays invocable — only the prompt-side delivery changed, from
a pointer a coordinator could skip invoking to prose it cannot skip reading.
`fragments/code-comments-default.md` and the `CODE_COMMENTS_STEP` registry
row are gone; there is nothing left in the Conditional fragment registry for
comment discipline to opt into.

The `OPEN A PULL REQUEST` ticket-reference line is the one row with three
mutually exclusive fragments (`pr-body-closes.md` / `pr-body-local-ref.md` /
`pr-body-local-noref.md`) instead of one on/off pair: `ISSUE_TRACKER` and
`LOCAL_ISSUE_REFERENCE` together pick exactly one gate, so issue-prompt.md
concatenates all three substitution variables and only the active one ever
renders — see [Local issue tracker](#local-issue-tracker-issue_trackerlocal)
for the three cases.

The caveman-default step is keyed on the baked skill itself rather than a
separate knob: whenever `DRIVER_SKILLS_DIR/caveman/SKILL.md` is present at
runtime, the issue pass and the fix pass (via the shared COMMS block, see
below), the scout prompt, both conflict-resolve prompts, the orchestrator's
review pass, and the research prompts (both the repo-backed and
self-contained variants) direct the agent to use `/caveman` for narration
and prose, exempting code, commands, error messages, and commit messages,
plus the machine-parsed marker grammar (the `SPINDRIFT_OUTCOME` line and its
`note=` field, the `VERDICT:` line, and host-relay signal lines like
`SPINDRIFT_PR_INTENT`) — see `fragments/caveman-default.md`. The worker
prompt carries only the opening `/caveman` directive plus a narrower
code/commands/error-messages exemption, dropping both the commit-message
exemption and the marker-grammar paragraph
(`fragments/caveman-default-worker.md`): a worker never writes a commit
message (the coordinator owns COMMIT, issue #3419), and the worker role is
structurally forbidden from ever emitting the marker grammar (issue
#2059/#2491 quarantine), so naming those markers in its own rendered prompt
would trip that contract.
The review prompt carries the marker-grammar paragraph minus
`SPINDRIFT_ISSUE_INTENT`, which the reviewer agent itself never emits (issue
#2707) — only the Filer subagent it spawns does — plus two additions of its
own (`fragments/caveman-default-review.md`). First, every
`## Blocking`/`## Non-blocking` finding stays full prose, since the
orchestrator hands finding text to a Filer subagent that turns it into a
GitHub issue body verbatim. Second, the `## Probed (APPROVE only)` lines
stay full prose too (issue #3265), on the same tier for the adjacent reason:
no Filer relays those, so the tier is text a human reads cold rather than
text the Filer forwards, and a compressed receipt stops being evidence that
the approving pass ran the hunt it names. The research prompts carry their
own variant too (`fragments/caveman-default-research.md`, issue #2708), for
the opposite reason from the worker prompt: research's posted verdict
comment is the entire human-facing product of the run — a human reads it to
decide whether to promote the issue or close it, and a later worker picks up
the context-for-a-worker section cold — so the exemption both widens and
narrows relative to `fragments/caveman-default.md`: it widens from just the
marker-grammar lines to the whole posted comment (the verdict line and its
rationale, the context-for-a-worker section, the open-questions section, and
the `<!-- spindrift-research -->` machine marker), on top of the
still-exempt `SPINDRIFT_OUTCOME` line and its required shape, but it narrows
too — it never names the `VERDICT:`/`SPINDRIFT_PR_INTENT` lines, since
research never emits either, and it drops the base fragment's commit-message
exemption (`fragments/caveman-default.md` exempts commit messages, always
full human-quality prose — irrelevant here since a research dispatch never
commits). It does name `SPINDRIFT_COMMENT` explicitly, where the base
fragment only reaches it generically, as one instance of "any host-relay
signal line such as `SPINDRIFT_PR_INTENT`": on a read-only dispatch
(`BoxWriteEnabled=false`) the box can't post the verdict comment directly,
and independently, whenever the issue tracker is a local tracker there is no
tracker client to post through at all — either way it relays that comment
through a single `SPINDRIFT_COMMENT` host-relay stdout line instead, making
it the sole carrier of the verdict on that path
(`fragments/research-verdict-local.md`). A Consumer that never bakes the
skill gets a prompt with zero mention of it.

A fix box (dispatched when CI comes back red — see [Runtime flow](#how-a-run-works))
receives `FIX_PASS` and runs `prompts/fix-prompt.md` instead: the branch is
already checked out with the prior run's work, so it skips SCOUT and
implement-from-scratch and goes straight to re-running checks, making a
targeted fix, committing, pushing, and waiting for CI — emitting the same
`SPINDRIFT_OUTCOME` grammar. `FIX_PASS` unset (the initial run) is
byte-identical to before this prompt existed.

On genuine-red, `selfHeal` also fetches the failed check names plus a bounded
log excerpt for the PR's head commit (`forge.PRForge.FailureDetail`, the same
fine-grained-PAT-safe GraphQL `statusCheckRollup` query `CheckState` uses) and
forwards it into the fix box as `CI_FAILURE_SUMMARY`. `fix-prompt.md` renders
it as a `# CI FAILURE` section ahead of `# CONTEXT`, so the fix agent goes
straight to the failing check instead of rediscovering it via a blind local
re-run — which misses CI-only failures (flaky, environment-specific, or
checks the repo's local CHECK step doesn't run). The fetch is best-effort:
a failure to fetch it never blocks the fix pass, and `CI_FAILURE_SUMMARY`
unset or empty leaves `fix-prompt.md` byte-identical to the no-detail case,
falling back to the local re-run with no error.

A fix box also resumes the *same Driver session* the initial run used,
instead of cold-starting a fresh one: the launcher creates an ephemeral,
process-lifetime, per-issue host directory and mounts it writable over the
Driver's declared session-cache dir — `sessionCacheDirRelative` in its
`lib/drivers/` entry (`/home/agent/.claude/projects` for claude, where its
session transcripts live), narrow enough that it can never shadow the baked
skills dir above (the only writable host mount the runner seam has — the
prompt/skills mounts above are read-only). A Driver that omits
`sessionCacheDirRelative` has no resumable session state: the launcher
creates no per-issue cache directory and the runner adapters add no mount on
either backend, the same "no cache, cold-start, never an error" degradation
described below. The harness image pre-creates the declared dir owned
`1000:1000` so the OCI runtime reuses the existing directory instead of
fabricating root-owned parent dirs when the volume is mounted; the bwrap
adapter additionally emits `--dir` on the declared dir's parent before the
bind so it is agent-owned in the tmpfs. The launcher only creates, mounts,
and evicts that directory — it never reads, copies, parses, or chmods its
contents, and the persisted session never leaves the host (it is not pushed
to the remote or attached to the PR). The cache is keyed strictly
`<cache>/<issue>`, so a session can only ever be resumed within its own
issue's trust domain; it is evicted as soon as that issue reaches a terminal
state (`agent-complete` or `agent-failed`), and the whole cache is removed
when the launcher process exits. A fresh `spindrift dispatch` — or a crash —
therefore always starts with an empty cache; `spindrift recover <n>` (see
[Runtime flow](#how-a-run-works)) still adopts a stranded PR, just without a
session to resume, so it takes the same cold-context fix flow described
above.

The actual pin/resume verb lives behind the Driver seam (ADR 0009): on the
initial run the claude Driver pins a deterministic session id (derived from
`REPO_SLUG` + `ISSUE_NUMBER`, so no state beyond those two env vars is needed
to recompute it) via `--session-id`; the fix box recomputes the same id and
passes `--resume` only when that session's transcript is actually present
under the mounted directory. When it is not — the cache was evicted, this is
the first fix pass after a crash, or the branch was rebased out from under
the session — the fix box falls back cleanly to the cold-context fix flow
above, with no error.

##### Role capability profiles

The [Default models](#default-models) table below says *which* model each
role gets by default.
[ADR 0049](adr/0049-role-capability-profiles-are-provider-neutral.md) says
*what each role's work demands*, in provider-neutral capability terms —
what a Consumer on a non-Anthropic provider maps their own model lineup
onto, since the roster's `model`/`effort` fields stay untyped strings with
no spindrift-owned tier vocabulary. It carries one profile per role —
coordinator, scout, worker, reviewer, filer — each naming whether the tier
is driven by capability, by cost/volume, or by both, and stating the role's
reasoning-effort intent. The profiles live only there, not restated here,
so a fifth roster entry has one place to go stale and a check that catches
it (`nix/checks/roster.nix`).

The reviewer's top tier is a capability floor, not a cost artifact: its
failure mode — a confident wrong APPROVE, or a fabricated finding — is
silent, unlike the worker's, whose mistakes the check gate catches before
they ever reach review. A Consumer substituting models across providers
should retier the reviewer last, not first.

Each profile states an effort *intent*, not a level, because the two
Drivers don't share an effort vocabulary and spindrift normalizes neither
value (claude's `--effort` ladder of `low`/`medium`/`high`/`xhigh`/`max`,
opencode's provider-specific `--variant` set) — see the per-entry `effort`
pass-through described earlier in this section.

##### Default models

<!-- BEGIN GENERATED DEFAULT MODELS -- nix run .#regen -- DO NOT EDIT -->
| Agent | Default model |
| --- | --- |
| `MODEL` (coordinator) | `claude-sonnet-5` |
| `scout` | `claude-haiku-4-5-20251001` |
| `reviewer` | `claude-opus-5` |
| `filer` | *(empty; dogfood pins `claude-haiku-4-5-20251001`)* |
| `worker` | `claude-sonnet-5` |
<!-- END GENERATED DEFAULT MODELS -->

#### Authoring a new Driver

`lib/drivers/default.nix` is the registry: the deep module that both
validates and renders every `lib/drivers/` entry (see `claude.nix`), so a
per-Driver file (`claude.nix` itself, and any future sibling) stays pure
data with no validation or rendering logic of its own (issue #624). A new
Driver entry declares:

- `name`, `package`, `bin`, `flagsCommon`, `outcomeExtractFnBody`,
  `sessionFlagsFnBody`, `agentsJsonTemplate` — the fields ADR 0009 already
  documents.
- `skillsDirRelative` — where the agent CLI scans for skill files, relative
  to `$HOME`. Required; the harness bakes skill files to the fixed,
  Driver-independent `/agent/skills` path instead (see the `skills` row
  above), and `agent/entrypoint.sh`'s `phase_prompt_assembly` copies them
  (`HARNESS_SKILLS_DIR`, then `OPERATOR_SKILLS_DIR` on top) into
  `$DRIVER_SKILLS_DIR` — rendered from this field — at box startup
  (issue #2489).
- `agentFilesTemplate` — a `{ roster }` function (see [Subagent
  roster](#subagent-roster)) returning an attrset of HOME-relative path →
  file content, for a Driver whose subagents land as on-disk files rather
  than a `--agents` flag (opencode renders
  `.config/opencode/agents/<name>.md`, one per roster entry). Required;
  `claude` returns `{ }` and composes subagents via
  `agentsJsonTemplate` instead, which also takes `{ roster }`.
- `sessionCacheDirRelative` — where the agent CLI's session transcripts
  live, relative to `$HOME`. Optional; a Driver that omits it has no
  resumable session state (see above).
- `argvShape` (ADR 0009, issue #2534) — the Driver's CLI argv-assembly
  shape, an attrset the Go/bash side walks to build the CLI invocation:
  `promptStyle` (`"flag"` or `"positional"`); `promptFlag` (required only
  when `promptStyle` is `"flag"`); `modelFlag`; `modelOmitEmpty` (bool —
  omit the model slot entirely when the model value is empty);
  `agentsFlag` (optional — omit the attribute entirely for a Driver with
  no `--agents` equivalent); `effortFlag`; and `order`, a permutation of
  the 6 argv slots `prompt`/`model`/`agents`/`session`/`driverFlags`/
  `effort`, restricted to whichever slots actually apply — `agents` is
  excluded from the required permutation when `agentsFlag` itself is
  omitted. See `claude.nix`'s (flag-style, with `--agents`) and
  `opencode.nix`'s (positional-style, without `--agents`) own `argvShape`
  blocks for the two worked examples.

The registry validates every entry against this required-attribute list at
eval time (`assertShape` in `lib/drivers/default.nix`): an entry missing one
of the required fields above fails the build with a message naming the
Driver and the missing attribute, before an image is ever produced.
`sessionCacheDirRelative` and, within `argvShape`, `promptFlag`/`agentsFlag`
are the fields this check treats as optional (`argvShape` itself is still a
required top-level attribute). Cross-half parity with the Go registry
(`cmd/launcher/internal/driver`) stays name-only by design (ADR 0009) —
each half enforces its own entries' completeness independently.

A separate validator, `assertArgvShape`, checks `argvShape`'s internal
structure — `assertShape` above only confirms the attribute is present, not
that its contents are well-formed. `assertArgvShape` requires: `promptStyle`
is `"flag"` or `"positional"`; `promptFlag` is a non-empty string when
required; `modelFlag`/`effortFlag` are non-empty strings; `modelOmitEmpty`
is a bool; `agentsFlag`, when present, is a non-empty string; and `order` is
a list containing each applicable slot exactly once, with no missing, extra,
or duplicate slots. Like `assertShape`, it collects every violation it finds
into one `throw` message naming the Driver, rather than failing on just the
first.

The registry also owns rendering: `renderPreamble` turns a validated entry
into the `DRIVER_*` variable block (`DRIVER_NAME` — the launcher selects its
host-side strategy by it — plus `DRIVER_BIN`, `DRIVER_FLAGS_COMMON`,
`DRIVER_SKILLS_DIR`, the last baked as an absolute path under
`/home/agent`, the image's fixed `HOME`) and the
`_driver_extract_outcome`/`_driver_session_flags` function definitions
`mkHarness` bakes into `agent/entrypoint.sh` ahead of its own body, instead
of `mkHarness` string-building them inline. The bats harness sources the
exact same rendered bytes (issue #433) before exec-ing the entrypoint, so a
test run and a built image can never drift apart — a bats fixture has no
real `/home/agent` to write skill files into, so `tests/helper.bash`
appends one test-only line *after* the registry-rendered preamble,
redirecting `DRIVER_SKILLS_DIR` at the test's own `$HOME`; the baked
preamble itself renders identically for both. `agent/entrypoint.sh` itself
carries no Driver value literals — if the nix-rendered preamble never ran (a
malformed image build), the entrypoint's `configure_env` fails fast with a
message naming the missing variable rather than silently impersonating the
claude Driver.

`mkHarness` also derives from the two directory declarations above for the
*host*-side half, though the two now diverge (issue #2489). The session
cache half is unchanged: the image bake pre-creates
`sessionCacheDirRelative` agent-owned (so podman/bwrap never fabricate a
root-owned parent when the launcher mounts over it), exported as an
absolute path (`DRIVER_SESSION_CACHE_DIR`, rendered by
`lib/preambles.nix`'s `renderDriverMountPreamble` — a separate renderer from
the registry's own `renderPreamble` above, consumed by the launcher wrapper
process rather than the in-box entrypoint) that the Go launcher's OCI and
bwrap adapters mount over directly — no Driver-specific path literal lives
in those runner adapters for session cache. Skills no longer follow this
pattern: `DRIVER_SKILLS_DIR` is rendered the same way but is read only by
`agent/entrypoint.sh` itself (see the `skillsDirRelative` and `skills`
entries above), not by the Go launcher. The runner adapters instead mount
`SPINDRIFT_SKILLS_DIR` onto a fixed, Driver-independent literal,
`/operator-skills` (`operatorSkillsDir` in
`cmd/launcher/internal/runner/mount.go`), which the entrypoint then copies
into `DRIVER_SKILLS_DIR` at box startup.

The SPINDRIFT_OUTCOME contract — the sections that instruct the agent to
print the `SPINDRIFT_OUTCOME issue=… landing=… status=… note=…` line the
launcher parses to learn the landing reference (a PR URL under
`CODE_FORGE=github`, a branch ref under `CODE_FORGE=git`) — is harness-owned,
not Consumer-tunable. At
`spindrift build` time, a `prompt` that omits the contract gets it appended
automatically (idempotent: a prompt that already has it is left untouched).
A runtime `SPINDRIFT_PROMPT_DIR` override is covered too: the entrypoint
appends the same canonical contract to a rendered issue prompt that omits it,
idempotently, so a runtime-mounted custom prompt can't ship an agent that
never emits the outcome line either.

#### Loop/background affordances are stripped, not merely discouraged

A headless Box run has no harness watching for a promised re-invocation:
`ScheduleWakeup`/`CronCreate`/`CronDelete`/`CronList`/`RemoteTrigger`/`Monitor`
each end the Driver's turn trusting a later wakeup the runner never delivers,
and a backgrounded Bash call (`run_in_background: true`) does the same for a
gate the Driver never blocks on (issue #1542 lost a run this way: the Driver
backgrounded its test gate, called `ScheduleWakeup`, and the run ended with
zero work pushed). Issue #1609 makes both structurally impossible rather than
relying on prompt wording alone (issue #1608 hardens that wording, but as
explanation, not enforcement):

- `claude.nix`'s `flagsCommon` carries a `--disallowedTools` entry naming the
  six scheduling tools above, so the claude Driver never sees them in any of
  the three passes that share `flagsCommon` (main run, conflict-resolve, fix)
  — a tool the model can't see, it can't call.
- `run_in_background` is a parameter of the Bash tool call, not a tool name,
  so it can't be stripped the same way. `lib/image.nix` bakes a Claude Code
  `PreToolUse` hook (`agent/reject-background-bash.sh`, registered by a
  `~/.claude/settings.json` the image now ships) that denies any Bash call
  carrying `run_in_background: true`, with a message telling the Driver to
  rerun the command in the foreground and block on it. `run_in_background`
  only sees the structured tool-call parameter, so a foreground Bash call
  can still self-background at the shell level — a trailing or mid-command
  `&`, or `nohup` — without ever setting that parameter. Issue #1620 widens
  the same hook to also parse `tool_input.command` for that: it masks quoted
  and backslash-escaped characters, then denies any standalone `&` control
  operator (as opposed to `&&`, a `>&`/`<&`/`&>` redirection token, or the
  `|&` pipe operator) or a `nohup` invocation. Issue #1635 further widens the
  same hook to also deny `setsid` and bash's `coproc` keyword, two more
  concrete shell-level detachment mechanisms; other detachment tools
  (`disown`, `at`, `systemd-run`, `screen -d`, `tmux new-session -d`, etc.)
  remain a deliberately out-of-scope judgment call for future work. Two edge
  cases are accepted false positives rather than chased: a literal `&` in
  arithmetic context (`$((3 & 4))`) and a literal `&` inside a heredoc body
  both deny a call that was actually safe, since the parser isn't
  `$((...))`- or line-aware.

Issue #2011 found a second, unclosed vector for the same failure mode:
the async `Agent`/`Task` subagent-launch tool backgrounds *by default*
(unlike Bash, which backgrounds only when explicitly told to) and is covered
by neither of the two controls above — `--disallowedTools` can't strip
`run_in_background` (a parameter, not a tool name, same as Bash's own), and
`reject-background-bash.sh`'s `PreToolUse` matcher is Bash-only. A Driver
launching its own scout/reviewer/filer subagents (the harness's own
prompt-directed workflow) could park its turn awaiting a notification a
one-shot `claude -p` session never receives, exactly like the `#1542`
`ScheduleWakeup` shape. Investigation ruled out the issue's other working
hypothesis — that `reject-background-bash.sh` was somehow inert specifically
under `ORCHESTRATOR_ENABLED` (`cmd/launcher/orchestrator`): the orchestrator
loops `driver-exec` for additional passes, but every pass — direct or
orchestrator-invoked — spawns the same `claude` binary with the same
`$HOME`, the same `--driver-flags` (`DRIVER_FLAGS_COMMON`, byte-identical
across both paths), and no `Env` override in either Go binary's
`exec.Command` call, so `~/.claude/settings.json`'s hooks were never
reachable by one path and not the other. The `#1998` dogfood run's park was
gap (b) alone.

The fix closes gap (b) at the tool-schema level rather than adding a third
`PreToolUse` hook: `CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=1`, a `claude`-native
env var with no public doc naming it as of this writing — confirmed instead
by reading the pinned `claude-code` 2.1.204 binary directly (`strings
bin/.claude-wrapped | grep CLAUDE_CODE_DISABLE_BACKGROUND_TASKS` finds it
gating `.omit({run_in_background: true})` on the Bash, Agent/Task, and
PowerShell tool schemas each). That reading is a snapshot, not the durable
guard: `nix/checks/drivers.nix`'s
`drivers-claude-cli-knows-disable-background-tasks-env` re-greps whichever
`claude-code` build is actually pinned, on every run, so a future version
that renames or drops the var fails CI loudly there instead of leaving the
Driver's export silently inert.

Setting it makes the CLI omit `run_in_background` from the `Bash`, `Agent`/
`Task`, and PowerShell tools'
own input schema entirely, so the Driver can never *request* async
execution from any of them — stronger than a deny hook, which still
requires the model to notice and correctly act on a denial after already
committing to the async framing mid-turn. `lib/drivers/
default.nix`'s `renderPreamble` grew a generic `envCommon` seam (an optional
per-Driver attrset of env vars, alongside `flagsCommon`'s CLI args) so
`claude.nix` can declare this one var without leaking a claude-specific
literal into the shared `lib/image.nix`; it renders as an `export` line in
the same `driverPreamble` text `flagsCommon` already flows through, so it
reaches `claude` under both the direct and orchestrator invocation paths
identically, and reaches both the OCI and bwrap runners identically (the
export lands in `entrypoint.sh`'s own shell process, inherited by every
child it execs, rather than a runner-specific `Config.Env`/`--setenv` list).
Disallowing `Agent`/`Task` outright via `--disallowedTools` was rejected —
scout/reviewer/filer subagent delegation is core to the harness's own
prompt-directed workflow, not a re-invocation promise like `ScheduleWakeup`.
`reject-background-bash.sh` is unchanged and still needed: it's the only
control over a Bash call that self-backgrounds at the shell level (a
trailing `&`, `nohup`, `setsid`, `coproc`), a vector
`CLAUDE_CODE_DISABLE_BACKGROUND_TASKS` doesn't reach since it only ever
governs the structured `run_in_background` parameter.

#### Self-inflicted secret reads are structurally blocked

Spec #1907's problem statement: an operator's direct experience is that the
Driver has read secret-bearing files into its own context despite being told
not to, and once a secret is in the transcript there is no output redaction
— it's effectively leaked into per-issue logs, PR bodies, and issue comments.
Issue #1909 closes this with two Harness-enforced, always-on defaults inside
the Box — no operator configuration, no opt-out. Issue #1927 replaces the
first of the two after a production regression:

- **Env-credential scrub hook (issue #1927; supersedes the
  `CLAUDE_CODE_SUBPROCESS_ENV_SCRUB=1` approach below).** Issue #1909
  originally baked `export CLAUDE_CODE_SUBPROCESS_ENV_SCRUB=1` into the
  entrypoint so Claude Code's own subprocess isolation would strip
  `ANTHROPIC_API_KEY` / `CLAUDE_CODE_OAUTH_TOKEN` from every subprocess it
  spawns. Issue #1926 found that feature bundles two effects that make it
  unusable inside the Box: it forces the Driver's permission mode to
  `default` (every Bash call then "requires approval", and a headless Box
  has no interactive approver to give it), and it wraps every Bash
  subprocess in Claude Code's own nested bwrap sandbox — which cannot mount
  `/proc` inside the Box's own outer bwrap sandbox. That second failure was
  reproduced directly (not just inferred from the production postmortem):
  invoking bubblewrap for a nested `--proc /proc` mount inside this
  harness's own bwrap-sandboxed build environment fails with the identical
  `bwrap: Can't mount proc on /newroot/proc: Operation not permitted`,
  across every `--unshare-user`/`--disable-userns` combination tried — the
  constraint is a kernel/capability boundary on nested mount-namespace
  `/proc` mounts, not a bwrap flag this harness can tune away. #1926
  reverted the baked export; #1927 replaces it with a third `PreToolUse`
  hook, `agent/env-credential-scrub.sh`, registered for the `Bash` matcher
  only (the threat is a spawned subprocess's environment, and `Read` never
  spawns one). It rewrites every Bash call, via
  `hookSpecificOutput.updatedInput` — a documented PreToolUse capability,
  independent of `permissionDecision` in Claude Code's own hook-output
  schema, that lets a hook replace a tool call's input before it runs — to
  `unset ANTHROPIC_API_KEY CLAUDE_CODE_OAUTH_TOKEN` ahead of the Driver's
  original command, and asserts no `permissionDecision` of its own on that
  path (the same silent, no-explicit-opinion posture `reject-background-bash.sh`
  and `credential-deny.sh` use for a call they don't deny — three hooks
  share the `Bash` matcher, and an explicit `allow` here could read as this
  hook's opinion overriding a sibling's `deny` on the same call). The
  rewrite is an actual removal from the subprocess's environment, not a
  denylist of dump commands: `env`/`printenv`/`set`/`export -p`/a direct
  `$VAR` expansion all come up empty as a structural consequence, with no
  per-command list to maintain or route around. Two vectors the rewrite
  alone can't close are denied outright instead: reading a *different*
  process's `/proc/<pid>/environ` (most plausibly the Driver's own, which
  legitimately still holds the credential for its own API auth — no
  rewrite of the current call can scrub another process's memory), and
  reading the *current* shell's own `/proc/<pid>/environ` via a pid form
  other than `self`/`thread-self` (`$$`, `$BASHPID`, `$PPID`, a glob) —
  confirmed empirically that `unset` clears what a subprocess forked
  *after* it sees at `/proc/self/environ`, but not the still-alive
  original shell's own environ region for its own lifetime, and the hook
  can't resolve which pid a piece of static command text refers to, so
  every `/proc/.../environ` reference is denied rather than parsed. Unlike
  the reverted mechanism, this hook never touches the Driver's permission
  mode or bash sandbox, so it structurally cannot reproduce either #1926
  failure mode.
- **Credential-read deny hook.** A second `PreToolUse` hook,
  `agent/credential-deny.sh`, is baked in and registered in the image's
  `~/.claude/settings.json` alongside `reject-background-bash.sh` (issue
  #1609) — one `Read`-matched entry and one `Bash`-matched entry, both
  pointing at the same script, since a Bash call can `cat` a credential path
  the same way a Read call can open it directly. Matching is boundary-aware,
  not a full-string suffix check, so it catches the path anywhere in a Bash
  command — piped, redirected, or passed to `cp` — not just when the
  command ends with it. It denies a `Read`/`Bash` call naming
  `.claude/.credentials.json`, `.config/gh/hosts.yml`, or a `.env`/
  `.env.<variant>` dotenv file, home-wide (not gated to any one Driver
  invocation, since every pass sharing the Box's `$HOME` — main run,
  conflict-resolve, fix — should be equally unable to read these paths).
  Implemented as a hook, not a
  `permissions.deny` rule, for the same reason `reject-background-bash.sh`
  is: the Box invokes the Driver with `--dangerously-skip-permissions`,
  which bypasses the permission-rule system entirely, but hooks are their
  own enforcement layer and still fire under that flag.

None of the three controls break legitimate agent work: `gh`, `git`, and the
Driver authenticate via environment variables already forwarded into the
Box, so they never need to *read* these files or *dump* their own
environment.

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
| `prefetch` | shell snippet that runs in the work tree after each clone; use it to download and cache dependencies so the agent doesn't fetch them cold on every tool invocation; under bwrap it's part of the agent-closure freshness dimension (ADR 0043) |
| `packages` | bakes a toolchain into the image itself; pre-warmed across runs (no per-run network fetch needed) |

Detection covers the following files (first match wins) — a lockfile for most
ecosystems, but Gradle projects don't always commit one, so any of its
build/settings files is enough on its own:

| file(s)                                                                                         | reported ecosystem |
| ----------------------------------------------------------------------------------------------- | ------------------ |
| `Cargo.lock`                                                                                    | cargo              |
| `package-lock.json`, `pnpm-lock.yaml`, `yarn.lock`                                              | npm/pnpm/yarn      |
| `go.sum`                                                                                        | go mod             |
| `build.gradle`, `build.gradle.kts`, `settings.gradle`, `settings.gradle.kts`, `gradle.lockfile` | gradle             |

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
# => { image, spindrift, packages, apps, internals }
# packages.spindrift is the CLI; add it to a devShell so `spindrift` is on PATH.
```

`internals` bundles every check-only output (build/run fixtures, contract
files, `driverExecBin`, `roster`, …); only `image`, `spindrift`, `packages`,
and `apps` are the versioned Consumer contract (ADR 0010).

`mkHarness` takes the locked *nixpkgs input* (not a pre-built `pkgs`) so it can
map a darwin `system` to its Linux twin and re-instantiate for the OCI image —
keeping the agent's toolchain and your dev shell from one pin (ADR 0002).

A harness built from `perSystem.spindrift.*` and one built by calling
`mkHarness` directly with the same arguments are byte-identical.
`flakemodule-equivalence` and `flakemodule-fixture` in
`nix/checks/equivalence.nix` enforce that: the first compares this repo's own
module config against the equivalent direct call, the second a minimal
flake-parts consumer that imports the shim — evaluated in-repo through a
nested `mkFlake`, so an out-of-tree consumer's own locked import of the module
is not itself covered. Both compare the two `packages.spindrift` wrapper
store paths, and only because those already match does the wrapper's
`--input` run document — the direct call's own — stand in for both when the
baked image store path is grepped out of it; the guarantee is therefore
about what the CLI is rather than about every app a harness exposes, and the
daemon wrapper path still has no equivalence seam (issue #3701).

### Calling the roster helpers directly

`spindrift.lib.rosterLib` (issue #2560) is part of that same versioned
Consumer contract. It's a curried function: call it with your own `lib` to
get `{ normalizeRoster; dropOptedOut; defaultRoster; }`. `defaultRoster` is what
`mkHarness`'s no-explicit-roster fallback path uses internally to build the
five-agent scout/reviewer/filer/worker/review-axis roster — a Consumer can
call it directly to build a custom roster without hand-authoring all five
entries.

```nix
perSystem = { inputs, lib, ... }: {
  spindrift.agents.models.roster =
    (spindrift.lib.rosterLib { inherit lib; }).defaultRoster {
      byName.reviewer.model = "claude-opus-5";
    };
};
```

The other two functions in the returned attrset are `dropOptedOut` and
`normalizeRoster`. `normalizeRoster` is the same validation/`promptFile`-default
pass `mkHarness` runs on every roster before handing it to a Driver (issue
#2152 slice A), useful to a Consumer building an entirely hand-authored
`roster` list who wants it caught at eval time rather than downstream.
`dropOptedOut` is the #392 opt-out step: it drops any entry whose `model` is
the explicit `""` sentinel, the step `mkHarness` runs after `normalizeRoster`
and right before handing the roster to a Driver.

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
      systems = [ "aarch64-darwin" "aarch64-linux" "x86_64-linux" ];
      imports = [ spindrift.flakeModules.default ];
      perSystem = _: {
        spindrift = {
          # Minimal toolchain — shared utilities only.
          # The Target repo's own devShell drives the real build/test commands.
          infra.image.packages = p: [ p.git p.gh p.gnused ];

          agents.prompt = builtins.readFile ./prompts/issue-prompt.md;

          # infra.nix.inBox is true by default — nix is already in the box,
          # no override needed for the devShell probe to work.
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
   because `infra.nix.inBox = true` is the default per ADR 0008), the
   entrypoint runs `nix develop ".#<DEV_SHELL_NAME>" --command true` under a
   `DEV_SHELL_PROBE_TIMEOUT`-second timeout (default 300 s, baked from
   `lib/env-schema.nix`).
2. If the probe succeeds, the prefetch hook and the Driver (claude invocation)
   run inside `nix develop ".#<DEV_SHELL_NAME>" --command bash <wrapper>` so
   the agent operates in the Target's exact pinned environment — tools, env
   vars, and shellHook included. If `nix develop` fails to exec the Driver
   (nix rc ≠ 0 and empty stream), the entrypoint relaunches once in the baked
   env rather than dying. That relaunch logs one line to the box's own
   stderr; it's an in-box observability detail, not itemized here alongside
   the operator-facing behavior.
3. If `flake.nix` is absent, `nix` is unavailable, the probe fails, or the
   timeout fires, the box degrades gracefully to the baked toolchain.

**`infra.nix.inBox` interplay.** The probe requires a working `nix` CLI
inside the box, which `infra.nix.inBox = true` (the default, [ADR
0008](adr/0008-nix-is-a-first-class-default-in-the-box.md)) provides.
Setting `infra.nix.inBox = false` (see [option surface](#option-surface))
produces a lean, nix-free image — there is no in-box `nix`, so the devShell
probe is skipped and only the baked `packages` toolchain is available.
**The devShell pattern requires `infra.nix.inBox = true`.**

**Practical upshot.** Keeping `packages` to a minimal set (`git`, `gh`, and the
like) means the Consumer image stays small and works across Target repos with
different language stacks. The `DEV_SHELL_PROBE_TIMEOUT` guard (default 300 s,
overridable at runtime via env var) ensures a broken or unusually heavy devShell
eval cannot stall the box. See the `nixInBox` [option](#option-surface) and
[ADR 0008](adr/0008-nix-is-a-first-class-default-in-the-box.md) for the full
rationale and the lean-image escape hatch.

---

## Runtime configuration

The target, secrets, and commit identity are set at **dispatch time** — never
baked as Nix options — so one image drives any Target repo without a rebuild
(ADR 0001). Secrets stay **runtime env** (`harness.env` or your shell, never
the flake); every other knob below is a flake `settings.*` value or a
`--flag` (ADR 0020) — the env var column still works this release (deprecated,
warns) but is no longer the primary channel:

Every secret knob (`GH_TOKEN`, `BOX_GH_TOKEN`, `CLAUDE_CODE_OAUTH_TOKEN`,
`ANTHROPIC_API_KEY`, `JIRA_TOKEN`) can also be sourced from an external
command instead of a plaintext value — **the preferred, highly encouraged
way to supply secrets**, since it keeps live credentials out of any file at
rest on the host. Set `<SECRET>_CMD` (e.g. `GH_TOKEN_CMD="rbw get
spindrift-gh-token"`) in `harness.env`, your shell, or direnv, or pass a
one-off `--<secret>-cmd` flag; the mechanism is tool-agnostic and works with
`rbw`, `op`, `pass`, `vault`, or any command that prints the secret on
stdout. The launcher execs the command and captures its stdout into memory,
trimming a trailing newline the same way `--<secret>-file` does — the secret
never touches disk. Resolution precedence, first non-empty wins:
`--<secret>-cmd` flag > `<SECRET>_CMD` env > `--<secret>-file` flag >
`<SECRET>` direct env > `--secret-cmd`/`SECRET_CMD` template — so a `_CMD`
variant overrides a direct value, and migrating to a vault is a matter of
adding the command, not first removing the old value. Supplying both
`--<secret>-cmd` and `--<secret>-file` for the same secret is a configuration
error. A failing or empty command aborts the launch with a named, value-free
error: the message names the knob and, on a non-zero exit, the exit code,
plus a generic, tool-agnostic remediation hint ("your vault may be locked;
unlock it (e.g. `rbw unlock`) and re-run") — never the command string, its
captured stdout, or its captured stderr. The fetched value is never logged,
baked into the nix store, or written to the launcher input document — only
the command string itself (which reveals a vault item name, not the
secret) may appear in host-side logs. The plaintext direct-value and
`--<secret>-file` forms remain fully supported and are not deprecated;
with this in place, `harness.env` is expected to hold fetch recipes
rather than live credentials. See
`spindrift --help --all` for the full `--<secret>-cmd`/`--<secret>-file`
flag list. When the launcher's own stdin and stderr are both TTYs, an
interactive vault unlock prompt passes through instead of being discarded —
see [Secret exposure model](#secret-exposure-model) for the TTY-gated
exception.

One vault under a uniform item-naming scheme (e.g. every item named
`spindrift-<kebab-case-env>`, the convention `harness.env.example` already
documents per secret above) can skip the per-secret repetition: `--secret-cmd`
/ `SECRET_CMD` sets a single templated fetch command, tried as the lowest-
precedence fallback for any secret above that has none of the four forms set.
`{name}` substitutes that secret's env name in kebab case (`GH_TOKEN` ->
`gh-token`), so `SECRET_CMD="rbw get spindrift-{name}"` reproduces every
per-secret `_CMD` example above in one line. A per-secret `<SECRET>_CMD`
(env or flag), file flag, or direct env value still wins over the template.
The template is attempted only for a secret this run actually needs — e.g.
`JIRA_TOKEN` only when `ISSUE_TRACKER=jira`, and never for the opt-in
`BOX_GH_TOKEN` (ADR 0016) — so an operator who doesn't use those features
never gets an unwanted vault lookup for them. When a vault's naming isn't
uniform, fall back to the per-secret `<SECRET>_CMD` forms above for the
exceptions.

| var                       | default                | meaning                                  |
| ------------------------- | ---------------------- | ---------------------------------------- |
| `REPO_SLUG`               | — (required unless `CODE_FORGE` and `ISSUE_TRACKER` are both `local`; baked via `forge.repoSlug`) | target repo, `owner/repo` |
| `GH_TOKEN`                | — (required unless `CODE_FORGE` and `ISSUE_TRACKER` are both `local`) | GitHub token for `gh` inside containers (secret; env only) |
| `GH_TOKEN_REFRESH_FILE`   | — (baked via `forge.ghTokenRefreshFile`) | path the launcher polls to keep `GH_TOKEN` current past an installation token's ~1h lifetime — see [GitHub App installation token](#github-app-installation-token-recommended) |
| `CLAUDE_CODE_OAUTH_TOKEN` | — (one auth required)  | from `claude setup-token` (secret; env only) |
| `ANTHROPIC_API_KEY`       | —                      | alternative to the OAuth token (secret; env only) |
| `GIT_USER_NAME`           | host `git config`; baked via `git.user.name` | commit author name (applied repo-locally inside the Box — see [Hermetic git config](#hermetic-git-config)) |
| `GIT_USER_EMAIL`          | host `git config`; baked via `git.user.email` | commit author email (applied repo-locally inside the Box — see [Hermetic git config](#hermetic-git-config)) |
| `CODE_FORGE`              | `github` (baked)       | code-landing backend: `github` (open PR, watch CI, merge), `git` (push-only to `CODE_FORGE_REMOTE_URL`; no PR, CI-watch, or merge gate — see [ADR 0013](../docs/adr/0013-issue-tracker-and-code-forge-are-independent-seams.md)), `local` (host-mediated landing onto the Accumulation repo's Integration branch; no PR, CI-watch, or network — see [ADR 0033](../docs/adr/0033-host-mediated-local-code-forge.md)), or `forgejo` (open a PR via `fj pr create`, watch the CI rollup, and rebase-merge under `MERGE_MODE` on a Forgejo/Gitea instance authenticated by `FORGEJO_TOKEN` — the second full `PRForge` backend beside `github`; see [ADR 0038](../docs/adr/0038-the-forgejo-backend-decision-set.md)) |
| `CODE_FORGE_REMOTE_URL`   | — (required when `CODE_FORGE=git`) | plain git remote URL to clone from and push to (self-hosted git, gitea, GitLab-without-MRs, a bare server repo) |
| `CODE_FORGE_ACCUMULATION_REPO_DIR` | `.spindrift/accum.git` under the launcher's working directory when `CODE_FORGE=local` (auto-created and seeded); an explicit value overrides it | host path to the bare Accumulation repo, mounted read-only into the Box and landed into host-side |
| `BOX_FORGE_AND_ISSUE_ACCESS` | `read-write` (baked)   | a third axis, orthogonal to `CODE_FORGE`/`ISSUE_TRACKER` (issue #1914): `read-write` (the Box writes directly, unchanged) or `read-only` (the Launcher host-mediates every write instead — see [Read-only Box](#read-only-box-box_forge_and_issue_accessread-only)), coherence-checked against the selected forge/tracker's registry capability bits at `nix build` (Consumer eval) time (issue #2526) — `read-only` is permitted only when the selected forge implements bundle-relay and host-side draft-PR-create and the selected tracker implements host-posted comments; `local`, `github`, and `forgejo` all satisfy the check today; a Go startup gate remains only as a backstop for a runtime override past what nix already validated |
| `BOX_SIGNAL_CARRIER`     | `log` (per-run only; not bakeable) | transport for the three mid-run signal channels (comment, PR intent, issue intent) crossing the launcher/Box seam: `log` (unchanged — nonce-guarded marker lines in the Box log) or `socket` (routed over a launcher-owned Signal socket instead, see [ADR 0052](adr/0052-mid-run-signals-cross-the-seam-over-a-launcher-socket.md) and [Signal socket](#signal-socket-box_signal_carriersocket) below) |
| `LABEL`                   | `ready-for-agent` (baked) | issues to pick up                     |
| `ISSUE_NUMBER`            | — (empty = discover)   | dispatch only this one issue, bypassing the `LABEL` query (per-run only; not bakeable) |
| `ISSUE_TRACKER`           | `github` (baked)       | IssueTracker backend: `github`, `local` (private Markdown + YAML frontmatter files — see [Local issue tracker](#local-issue-tracker-issue_trackerlocal)), `jira`, or `forgejo` (see [Issue Tracker backends](#issue-tracker-backends)) |
| `LOCAL_ISSUES_DIR`        | `.spindrift/issues` (baked) | directory scanned for issue files when `ISSUE_TRACKER=local`; git-ignored by default |
| `BASE_BRANCH`             | `main` (baked)         | branch to cut from and PR into           |
| `MAX_PARALLEL`            | `3` (baked)            | concurrent containers                    |
| `BRANCH_PREFIX`           | `agent/issue-` (baked) | branch name = prefix + issue number      |
| `IN_PROGRESS_LABEL`       | `agent-in-progress` (baked) | label a dispatched issue is swapped to |
| `FAILED_LABEL`            | `agent-failed` (baked) | label an issue gets when its Box fails or its PR can't merge |
| `COMPLETE_LABEL`          | `agent-complete` (baked) | label the launcher swaps on when CI reaches green (agent is done; the merge is a separate step) |
| `MERGE_MODE`              | `manual` (baked)       | post-green merge policy: `manual` (leave the green PR for a human), `immediate` (rebase-merge on green), `auto` (enqueue the forge's native auto-merge — meaningful on any `PRForge` backend, `github` or `forgejo`; the forge repo must have auto-merge enabled). Under `CODE_FORGE=git`, `manual`/`immediate` map to remote pushes instead (leave the pushed branch / push straight to the target branch); `auto` has no meaning off a `PRForge` backend and fails fast at startup. Under `CODE_FORGE=local`, only `immediate` relays the seam bundle into the Accumulation repo — `manual`/`auto` have no meaning under `local` and fail fast at startup. |
| `MERGE_METHOD`            | `rebase` (baked)       | how the final integration commits land on green: `merge` (merge commit), `squash`, or `rebase`; maps to GitHub's native `merge_method` (`github` Code Forge merge path only) |
| `SYNC_METHOD`             | `rebase` (baked)       | how a behind branch is brought current before landing: `rebase` (linear history) or `merge` (merge the base in); governs both the preflight-stale-base proactive sync and the reactive on-conflict sync during an immediate merge (`github` Code Forge PR-landing path only) |
| `MERGE_GUARD_PATHS`       | `.github/**,.forgejo/**,**/CLAUDE.md,**/AGENTS.md,.claude/**,.opencode/**` (baked) | comma-separated globs; a green PR touching a matched path downgrades to manual regardless of `MERGE_MODE` (`github` and `forgejo` Code Forges only; empty disables — see [Merge guard](#merge-guard)) |
| `MODEL`                   | (baked; see [Default models](#default-models)) | main/coordinator Claude model the in-container agent runs (worker-tier defaults are unaffected) |
| `EFFORT`                  | — (empty, no baked default) | main/coordinator reasoning-effort level, passed straight through to the Driver with no normalization — the value must be valid for the active `DRIVER`: on `claude` it becomes `--effort <level>` (`low`/`medium`/`high`/`xhigh`/`max`), on `opencode` it becomes `--variant <level>` (opencode's cross-provider variant selector); unset emits no flag either way, so the Driver's own default effort applies |
| `SCOUT_MODEL`             | (baked; see [Default models](#default-models)) | scout subagent model tier (empty drops the scout entry from `--agents`). **Deprecated** — superseded by the [`roster`](#subagent-roster) option |
| `REVIEW_MODEL`            | (baked; see [Default models](#default-models)) | reviewer subagent model tier (empty drops the reviewer entry from `--agents`). **Deprecated for non-orchestrator use** — superseded by the [`roster`](#subagent-roster) option. Under `ORCHESTRATOR`, the roster reviewer entry is itself superseded by the code-owned review pass, which binds its model from this value instead (falling back to the coordinator model when unset); an explicit dispatch-time `REVIEW_MODEL=...`/`--review-model ...` overrides the baked roster value per run, no rebuild needed (issue #3171) — precedence is dispatch-time env > baked roster reviewer entry > coordinator-model fallback |
| `REVIEW_EFFORT`           | — (empty, no baked default) | value for the code-owned review pass's own effort argument to the Driver; pass-through only, no normalization, same accepted values as `EFFORT` for the active Driver. Reaches the review pass via the handoff document's `ReviewEffort` field (issue #2975), not a direct flag — `driver-exec assemble-prompt` extracts it from the roster's `reviewer` entry, and `driver-exec` applies it only on the reviewer-role pass (see the code-owned review pass discussion under [In-box orchestrator](#in-box-orchestrator)). Overrides the roster reviewer entry's own effort (`rosterDefaults.reviewer.effort` by default) the same way `REVIEW_MODEL` overrides the reviewer's model — empty means follow the roster, a non-empty value overrides it. Meaningful only under `ORCHESTRATOR`. Like `REVIEW_MODEL`, an explicit dispatch-time `REVIEW_EFFORT=...`/`--review-effort ...` overrides the baked value per run, no rebuild needed (issue #3171) — precedence is dispatch-time env > baked roster reviewer entry > coordinator fallback; unset or empty at dispatch leaves the baked chain unchanged |
| `FILER_MODEL`             | (baked; see [Default models](#default-models)) | filer subagent model tier; empty (default) means the filer is not provisioned — setting a model is the opt-in (recommended: the same model as the `scout` default, see [Default models](#default-models)); see [Filer](#filer). **Deprecated** — superseded by the [`roster`](#subagent-roster) option |
| `WORKER_MODEL`            | (baked; see [Default models](#default-models)) | implement-capable worker subagent model tier (empty drops the worker entry from `--agents`); when set, the implementor runs IMPLEMENT as a coordinator and delegates one slice at a time to it. **Deprecated** — superseded by the [`roster`](#subagent-roster) option |
| `IMAGE`                   | `spindrift:latest`     | image tag to run                         |
| `SPINDRIFT_PROMPT_DIR`    | baked prompt store path | host directory mounted over `/agent/prompts` for zero-rebuild prompt iteration; declaratively configurable via the `perSystem.spindrift.agents.promptDir` flake option or `settings`, or hot-overridden at dispatch time via `--prompt-dir` / the env var. The knob itself is a versioned surface; the template an override directory replaces is not — see [`VERSIONING.md`](../VERSIONING.md)'s prompt-template carve-out |
| `SPINDRIFT_SKILLS_DIR`    | baked skills store path | hot-override skills at dispatch time: mounted to `/operator-skills` and merged over the baked `/agent/skills` set at box startup (not bakeable) |

Every `settings`-baked knob above can be re-pointed at dispatch time with its
`--flag` (see `spindrift --help --all` / `man spindrift`); a knob env var
still wins over the flake setting this release, but is deprecated and warns
(ADR 0020) — see [`MIGRATING.md`](../MIGRATING.md) for the flag/settings
equivalents. `(baked)` marks knobs whose defaults are baked into the Launcher
input document via `settings`; `(not bakeable)` marks knobs deliberately kept
off the flake surface (secrets, per-run overrides, and dev-iteration host-path
mounts, though the latter are still flags — see the full reference). Commit
identity is **required**: an override wins, else the host's `git config
user.name`/`user.email` is inherited; if neither is set, `spindrift dispatch`
exits rather than committing under an arbitrary identity.

> **`--agents` needs a recent `claude-code`.** Whenever any subagent model
> above (`SCOUT_MODEL`/`REVIEW_MODEL`/`WORKER_MODEL`/… or the [`roster`](#subagent-roster))
> is non-empty — the default — the launcher passes the subagent roster to the
> claude Driver as `--agents <json>`. That flag needs a recent `claude-code`
> — it is present on **2.1.204** (older builds may predate it). A Consumer
> whose `nixpkgs` input pins an older `pkgs.claude-code`
> (the [`default` template](../templates/default/flake.nix) does not `follow`
> spindrift's `nixpkgs`, so its version floats with the Consumer's own pin)
> fails *every* issue with `error: unknown option '--agents'`. The launcher
> classifies that failure as `reason=unsupportedFlag` (not the generic
> `taskFailed`) so the dispatch log names the cause. The fix: bump the
> Consumer's `nixpkgs` input so `pkgs.claude-code` is current, or blank the
> subagent model knobs (set `SCOUT_MODEL=`/`REVIEW_MODEL=`, etc.) to drop the
> roster and stop emitting `--agents`.

### opencode Driver: github-copilot Provider credential

The opencode Driver's `github-copilot` Provider is OAuth-only (ADR 0009
amendment, issue #260, empirically validated on opencode 1.17.15): there is
no API-key form, and the credential is a single long-lived GitHub OAuth
token (`gho_…`) minted once by opencode's device flow, stored verbatim in
both the `refresh` and `access` fields of opencode's auth store with
`expires: 0`. That device flow is interactive and cannot run headless
inside the Box — the `github-copilot` analogue of `claude setup-token` — so
it is a one-time **host** step:

```sh
opencode auth login -p github-copilot
```

This writes `~/.local/share/opencode/auth.json` on the host, keyed by
Provider. opencode itself never reads that file inside the Box: it loads
its whole auth store from the `OPENCODE_AUTH_CONTENT` env var (parsed by
`Auth.all()` before opencode ever touches `auth.json`), so no file mount is
needed — only the `github-copilot` slice of the store, extracted with:

```sh
OPENCODE_AUTH_CONTENT=$(jq -c '{"github-copilot": .["github-copilot"]}' ~/.local/share/opencode/auth.json)
```

`OPENCODE_AUTH_CONTENT` is a secret knob like `CLAUDE_CODE_OAUTH_TOKEN` above
— set it in `harness.env`, source it from a vault via
`OPENCODE_AUTH_CONTENT_CMD`/`--opencode-auth-content-cmd` (the preferred
route — see [Runtime configuration](#runtime-configuration)), or pass
`--opencode-auth-content-file`; it never belongs on argv, and the launcher
threads it into the Box the same way it does every other secret
(`offArgvKeys` on the bwrap runner, `-e`/`--env` on the OCI runners).

Point `MODEL` (and any per-tier `*_MODEL`) at a Provider-namespaced model id
to select `github-copilot`, e.g. `MODEL=github-copilot/<model>`. Copilot
serves its own drifting catalog (`claude-sonnet-4`, `claude-opus-4`,
`gpt-4o`, …) that is disjoint from the Anthropic-API model ids and shifts
over time, so obtain a currently-served `<model>` from Copilot's live
catalog — `opencode models`, filtered to the `github-copilot` provider —
rather than copying a literal id that will rot. The launcher's
`validate()` requires `OPENCODE_AUTH_CONTENT` only when `DRIVER=opencode`
*and* the top-level `MODEL` carries the `github-copilot/` prefix; it does
**not** inspect the
per-tier `*_MODEL` knobs, so a config that points only a subagent tier (e.g.
`REVIEW_MODEL=github-copilot/…`) at the Provider while leaving `MODEL` on a
non-Copilot model passes validation yet still needs `OPENCODE_AUTH_CONTENT`
set to run un-broken — set the credential whenever *any* selected tier
resolves to `github-copilot`. Under the opencode Driver, the claude
credentials (`CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY`) are not
required at all — the claude Driver and the opencode Driver's
`github-copilot` Provider each own their own credential, and validation
never demands both at once.

A `github-copilot` model id that Copilot no longer serves — an unsupported or
deprecated id — is rejected without a distinct signal: opencode collapses it
into the **same** opaque `{"type":"error",…,"name":"UnknownError"}` /
"Unexpected server error. Check server logs for details." envelope a
missing or expired credential produces, with no "model not found" in the
NDJSON stream. So if a Copilot run fails with that bare envelope, check the
`<model>` against the live catalog above — it is a distinct failure mode from
a missing/expired `OPENCODE_AUTH_CONTENT`, not just an auth problem.

### Advanced tuning

These knobs are rarely changed. All except `SPINDRIFT_SKILLS_DIR` and
`ISSUE_NUMBER` can be baked via `settings` (see
[Option surface](#option-surface)); all except the daemon-only knobs below —
every row whose meaning says the launcher itself ignores it — can
additionally be overridden at dispatch time via `--flag` (env still works
this release too, deprecated — ADR 0020). The launcher does generate a
`--flag` for those daemon-only knobs as well, to hold the 1:1
flake-option/flag parity (issue #3567), but never reads the value — passing
one is accepted and silently inert. Set them through `settings` /
`perSystem.spindrift.dispatch.*` instead, which the daemon resolves for
itself. See `lib/env-schema.nix` for the authoritative list.

| var                    | default | `settings` section | meaning                                                |
| ---------------------- | ------- | ------------------ | ------------------------------------------------------ |
| `MAX_JOBS`             | `0`     | `concurrency`      | caps the wave size (`0` = uncapped) |
| `CONTINUOUS_DISPATCH`  | `` (off) | `concurrency`     | **deprecated**, superseded by [Daemon](#daemon) (issue #3547) — opt-in slot-refill dispatch mode: refills each freed slot from a live re-discovery, gated by the freshness probe before every launch; exits with a new documented code when the probe finds the loaded image or the loaded host launcher stale (see the [exit-code table](#dispatch-exit-codes)) |
| `DAEMON_APP`           | `.#`    | — (post-freeze; no legacy alias — set `dispatch.daemonApp`) | flake app attribute the daemon re-invokes for each child Dispatch, pinned to the fetched revision — the Consumer's own CLI app, e.g. `.#` or `.#dogfood-bwrap`; read by the daemon only, the launcher itself ignores it (its `--flag` is accepted but inert) — see [Daemon](#daemon) |
| `DAEMON_AWAKE_WINDOW`  | `` (always awake) | — (post-freeze; no legacy alias — set `dispatch.daemonAwakeWindow`) | daily local-time span the daemon may start a new Box in, `HH:MM-HH:MM IANA-zone` (e.g. `22:00-06:00 Europe/London`); gates only starting a Box, not one already running; the zone is explicit and never inherited from the host; read by the daemon only, the launcher itself ignores it (its `--flag` is accepted but inert) — see [Daemon](#daemon) |
| `DAEMON_SELF_APP`      | `.#daemon` | — (post-freeze; no legacy alias — set `dispatch.daemonSelfApp`) | flake app attribute of the daemon itself, evaluated at each fetched tip to notice its own build changed and halt — distinct from `DAEMON_APP`, the child Dispatch app; a Consumer that re-exports the daemon under another top-level attribute (e.g. spindrift's own `.#dogfood-bwrap-daemon`) must set this to match, or the check would evaluate a different harness's daemon and report a permanent, spurious change; read by the daemon only, the launcher itself ignores it (its `--flag` is accepted but inert) — see [Daemon](#daemon) |
| `RESEARCH_RESERVATION` | `1`     | — (post-freeze; no legacy alias — set `dispatch.researchReservation`) | how many of `MAX_PARALLEL`'s pool slots prefer research Dispatches over work — a floor, not a ceiling; read by the daemon only, the launcher itself ignores it (its `--flag` is accepted but inert) — see [Daemon](#daemon) |
| `DAEMON_IDLE_FLOOR`    | `5m`    | — (post-freeze; no legacy alias — set `dispatch.daemonIdleFloor`) | wait before the daemon's first no-work check against a kind, and the poll slice size while riding out a jammed kind's backoff; a Go time.ParseDuration string, validated by the daemon at startup, which refuses to start on a bad value; read by the daemon only, the launcher itself ignores it (its `--flag` is accepted but inert) — see [Daemon](#daemon) |
| `DAEMON_IDLE_CAP`      | `30m`   | — (post-freeze; no legacy alias — set `dispatch.daemonIdleCap`) | ceiling the daemon's per-kind idle backoff doubles up to, starting from `DAEMON_IDLE_FLOOR`; a Go time.ParseDuration string, validated by the daemon at startup, which refuses to start if the cap is below the floor; read by the daemon only, the launcher itself ignores it (its `--flag` is accepted but inert) — see [Daemon](#daemon) |
| `DAEMON_FAILURE_BACKOFF` | `1m`  | — (post-freeze; no legacy alias — set `dispatch.daemonFailureBackoff`) | wait a slot backs off for after an unclassified child failure before refilling itself; a Go time.ParseDuration string, validated by the daemon at startup, which refuses to start on a bad value; read by the daemon only, the launcher itself ignores it (its `--flag` is accepted but inert) — see [Daemon](#daemon) |
| `DAEMON_BREAKER_THRESHOLD` | `5` | — (post-freeze; no legacy alias — set `dispatch.daemonBreakerThreshold`) | pool-wide unclassified failures within `DAEMON_BREAKER_WINDOW` that trip the circuit breaker and halt the whole daemon; validated by the daemon at startup, which refuses to start on a non-positive value; read by the daemon only, the launcher itself ignores it (its `--flag` is accepted but inert) — see [Daemon](#daemon) |
| `DAEMON_BREAKER_WINDOW` | `15m`  | — (post-freeze; no legacy alias — set `dispatch.daemonBreakerWindow`) | trailing window the circuit breaker counts `DAEMON_BREAKER_THRESHOLD` unclassified failures within; a Go time.ParseDuration string, validated by the daemon at startup, which refuses to start on a bad value; read by the daemon only, the launcher itself ignores it (its `--flag` is accepted but inert) — see [Daemon](#daemon) |
| `MAX_FIX_ATTEMPTS`     | `3`     | `selfHealing`      | fix-box passes when CI is genuinely red before `agent-failed` (`0` disables self-healing) |
| `MAX_REBASE_ATTEMPTS`  | `3`     | `selfHealing`      | rebase-and-retry passes when a green PR conflicts with the base after a sibling merge (`0` disables rebase retries); also caps the opt-in [Stale-base preflight](#stale-base-preflight)'s rebase budget |
| `MAX_BUDGET_TOKENS`    | `0`     | `selfHealing`      | cumulative tokens (every pass and every retried attempt within it) before stopping self-heal short of `MAX_FIX_ATTEMPTS` (`0` disables the token budget cap); also forwarded into the Box, where the orchestrator's own review loop applies the same threshold to its own fresh, Box-local sum (implement/fix/review passes plus dispatched workers in *this* Box only, not the host's cross-Box figure) to commit to a terminal land pass instead of a further BLOCK-triggered review round |
| `MAX_BUDGET_USD`       | `0`     | `selfHealing`      | cumulative cost in USD (every pass and every retried attempt within it) before stopping self-heal short of `MAX_FIX_ATTEMPTS` (`0` disables the cost budget cap); quote fractional values in flake settings, e.g. `"4.44"`; also forwarded into the Box for the orchestrator's own review-loop budget cap, same as `MAX_BUDGET_TOKENS` (same threshold, same fresh-per-Box sum, not the host's own cross-Box one) |
| `PREFLIGHT_STALE_BASE` | `` (off) | `selfHealing`    | opt-in: proactively rebase a green-but-behind PR (no conflict) and re-green it before merging — see [Stale-base preflight](#stale-base-preflight); off by default merges a green-but-behind PR as-is |
| `MERGE_POLL_INTERVAL`  | `30`    | `branches`         | seconds between CI-status polls in the merge gate. Not a rate-limit lever: the gate issues one strictly-serialized, single-point GraphQL query at a time, only while a PR is actively mid-landing, bounded by `MERGE_POLL_TIMEOUT` — it never bursts. The cadence-sensitive pollers are the *continuous refill* ticker (runs for the launcher's whole lifetime and bursts outside its ticker) and the *console backlog* poll — slow those in a rate-limit sweep, not this knob. The interval is also reused as four fixed-call-count delays (the `SUCCESS` confirm re-poll, the `3 ×` check-registration window, the merge-blocked-by-checks retry, and the fix-pass no-op confirm), so raising it stretches every landing for zero rate-limit benefit (issue #3249) |
| `MERGE_POLL_TIMEOUT`   | `3600`  | `branches`         | seconds to wait for CI green before abandoning the merge — at the default, a merge that rides out the full deadline can outlive an installation token's ~1h lifetime when `GH_TOKEN_REFRESH_FILE` is unset (see [GitHub App installation token](#github-app-installation-token-recommended)) |
| `OVERLAP_GATE`         | `defer` | `concurrency`      | declared `## Touches` overlap policy: `defer` (hold a Dispatchable issue whose declared touch-set intersects an in-progress issue's, retrying once the collider completes) or `off` (disable the check — see [Declared touch-set overlap](#declared-touch-set-overlap)) |
| `TRANSIENT_RETRY_MAX`  | `3`     | `selfHealing`      | retries for transient box exits (529/network backoff; consecutive 429 holds) |
| `TRANSIENT_BACKOFF_SECS` | `30`  | `selfHealing`      | base linear backoff per transient retry                |
| `HOLD_JITTER_SECS`     | `5`     | `selfHealing`      | jitter added to a 429 hold-until-reset before re-dispatch |
| `DEV_SHELL_NAME`       | `default` | `sandbox`        | which devShell to enter; set `ci` to use a lean headless shell distinct from the interactive `default` |
| `DEV_SHELL_PROBE_TIMEOUT` | `300` | `sandbox`        | seconds before the in-box devShell probe is abandoned for the baked toolchain |
| `MEMORY_LIMIT`         | `5g`    | `sandbox`          | memory cap: hard `--memory` cap under OCI; under bwrap, a per-Box cgroup v2 `memory.max` when the host delegates a writable cgroup subtree, else best-effort — warns and proceeds uncapped rather than refusing to launch (ADR 0042); empty disables |
| `PIDS_LIMIT`           | `512`   | `sandbox`          | process-count cap: hard `--pids-limit` cap under OCI; under bwrap, a per-Box cgroup v2 `pids.max` when the host delegates a writable cgroup subtree, else best-effort — warns and proceeds uncapped rather than refusing to launch (ADR 0042); empty disables |
| `NETWORK_MODE`         | `open`  | — (post-freeze; no legacy alias — set `infra.network.mode`) | Box network posture: `open` (default; isolates bwrap into its own netns behind a hardened pasta helper — working egress, host loopback blocked, issue #2666 — OCI unchanged, no isolation there), `host` (bwrap-only opt-out restoring the pre-#2666 shared-host-netns behavior; no-op on OCI), `no-host-loopback` (keep egress, deny host-loopback on podman; inert-but-correct on docker/nerdctl, same as `open` there — unsupported on `runtime=bwrap`, eval error), `none` (fully offline — documented test-only, a Driver can't reach its Provider) — see [Network mode](#network-mode-network_mode) |
| `PODMAN_NETWORK`       | —       | `sandbox`          | raw `--network` escape hatch for podman run; mutually exclusive with `NETWORK_MODE` at eval time |
| `BWRAP_UNSHARE_NET`    | —       | `sandbox`          | raw `--unshare-net` escape hatch for bwrap, now pasta-backed (issue #2666); redundant with the isolate-by-default posture unless paired with `NETWORK_MODE=host`, which nix eval already rejects; mutually exclusive with `NETWORK_MODE` at eval time |

`MEMORY_LIMIT`/`PIDS_LIMIT` degrading without cgroup delegation (above) no
longer costs `is-running`, `list-running`, and `reap` their visibility:
under bwrap those resolve a Box through the same per-Box cgroup v2
subtree, but that subtree — and the `ErrAlreadyRunning` collision guard
and Console's orphan detection (issue #651) alongside it — survives
whenever a delegated cgroup directory can be created at all; an
unwritable `pids.max`/`memory.max` degrades only that one cap, not the
whole cgroup (ADR 0042). They report "nothing running" only when no
directory can be created at either place the launcher would put one: the
delegation anchor the walk resolves, or — where no ancestor qualifies —
the launcher's own cgroup. That is a host with no unified cgroup v2 mount
(a cgroup v1/hybrid host), a wholly read-only cgroup filesystem, or, the
commoner case, a filesystem writable to root but with no directory on the
launcher's own cgroup path writable by the user it runs as (a
non-delegated, non-systemd host). A writable ancestor whose
`cgroup.subtree_control` lacks a controller a configured limit needs is
not a valid anchor, so it does not rescue visibility on such a host.

The bats test suite has its own internal `WAIT_FOR_LOG_LINES_TIMEOUT` knob
(`tests/helper.bash`'s `wait_for_log_lines` poll helper) for widening its
default poll patience against a loaded host — a test-only bash env var, not
part of the Consumer settings surface above. The `bats` check
(`nix/checks/bats.nix`), excluded from `nix build .#checks-inbox` (it builds
the OCI image) but run by `nix flake check`, bakes a wider default (10s, vs.
the shell-level 2s default).

#### Network mode (`NETWORK_MODE`)

`NETWORK_MODE` (`perSystem.spindrift.infra.network.mode`) is the primary
network-posture knob — one of `open`, `host`, `no-host-loopback`, or `none`,
rendered into the right per-runtime flag by the launcher:

- **`open`** (default) — bwrap isolates into its own network namespace
  behind a hardened pasta helper (`pasta -t none -T none -u none -U none
  --no-map-gw`, ADR 0042): general egress works, host loopback is blocked —
  podman-rootless parity (issue #2666). OCI runtimes are unchanged: no
  `--network` flag, so the runtime's own default network (podman/docker/
  nerdctl bridge, general egress, no host-loopback isolation) applies.
- **`host`** — bwrap-only documented opt-out. Deliberately restores the
  pre-#2666 behavior of sharing the host's network namespace as-is (no
  pasta helper, host loopback reachable). Has no OCI rendering — falls
  through to the same no-`--network`-flag default `open` already renders
  there, a harmless no-op, not an eval error.
- **`no-host-loopback`** — keeps internet egress while denying
  host-loopback, **on podman**: renders `--network=pasta` (no `--map-gw`),
  which genuinely denies host-loopback by default. **On docker/nerdctl**
  this mode currently renders `--network=bridge` — but `bridge` is
  docker's/nerdctl's own *default* network, byte-identical to what `open`
  already renders there (no `--network` flag at all falls back to the same
  default bridge). Docker's/nerdctl's default bridge does **not** deny
  host-loopback: a host service bound to `0.0.0.0` stays reachable from
  inside the container via the bridge gateway IP (e.g. `172.17.0.1`, or
  `host.docker.internal` where wired), regardless of this mode. So on
  docker/nerdctl, `no-host-loopback` is currently an inert-but-correct
  render — right backend syntax, no stronger isolation than `open` — not
  yet a functional guarantee; podman is the runtime this mode's guarantee
  actually holds on today.
  **Unsupported on `runtime=bwrap`**: since issue #2666, bwrap's default
  `open` posture already isolates the network namespace via pasta with host
  loopback blocked, so `no-host-loopback` would render byte-identical to
  `open` there — a distinct choice with no distinct effect, which would
  mislead a Consumer into thinking they get something `open` doesn't
  already give them. It stays rejected: a Consumer flake combining
  `NETWORK_MODE=no-host-loopback` with `runtime=bwrap` fails at `nix eval`,
  not at dispatch time — use `open` instead.
- **`none`** — fully offline. Renders `--network=none` on OCI runtimes and
  bare `--unshare-net` on bwrap (no pasta helper — the one mode that skips
  it). This is **documented test-only**: a Driver cannot reach its own
  Provider (the Anthropic API, GitHub Copilot, etc.) under `none`, so it's
  only useful for tests that don't need a live model call or a
  network-dependent assertion — not a real dispatch configuration.

Setting both `NETWORK_MODE` and a raw knob (`PODMAN_NETWORK` /
`BWRAP_UNSHARE_NET`, i.e. `infra.network.podman` / `infra.network.bwrapUnshare`)
on the same Consumer flake is an eval error — there is no precedence rule
between them, pick one.

#### Reaching a local/self-hosted model server under egress restriction

A local/self-hosted opencode Provider — Ollama, LM Studio, or a llama.cpp
server (Tier 5; [ADR 0009](adr/0009-agent-cli-is-a-pluggable-driver.md)'s
issue-#269 amendment) — is an OpenAI-compatible endpoint on the host loopback
or LAN (`http://localhost:11434/v1`, `http://127.0.0.1:1234/v1`,
`http://127.0.0.1:8080/v1`). Whether the Box can reach it turns on
[`NETWORK_MODE`](#network-mode-network_mode) (or, on a Consumer that still
sets a raw knob instead, `PODMAN_NETWORK`/`BWRAP_UNSHARE_NET`), and the two
runners differ:

- **`open` (default), OCI — already reachable, unchanged.** The OCI
  runtime's own default bridge permits general egress, so a
  host-loopback/LAN model server is reachable with no extra wiring.
  Selecting a local Provider opens nothing new; the reachability is already
  latent. This did not change with issue #2666.
- **`open` (default), bwrap — no longer reachable (issue #2666).** As of
  issue #2666, `open` isolates bwrap into its own network namespace behind a
  hardened pasta helper, which blocks host loopback by design (the whole
  point of the issue). A host-loopback/LAN model server is **not** reachable
  under the default anymore. To deliberately reopen it, set
  `NETWORK_MODE=host` — the documented bwrap-only opt-out that restores the
  pre-#2666 shared-host-netns behavior — at the cost of losing the
  isolation this issue adds for the whole Box, not just the model-server
  path.
- **`no-host-loopback`, podman — reopen it with the raw escape hatch.**
  `NETWORK_MODE=no-host-loopback` renders plain `--network=pasta`, which
  denies host-loopback. For a backend that needs both restricted egress
  *and* a reopened host-loopback hole, drop down to the raw `PODMAN_NETWORK`
  knob instead — it's passed verbatim to `--network` and accepts a compound
  value `network.mode` can't express: `PODMAN_NETWORK=pasta:--map-gw` or
  `PODMAN_NETWORK=slirp4netns:allow_host_loopback=true`. (Remember
  `NETWORK_MODE` and `PODMAN_NETWORK` are mutually exclusive at eval time —
  using the raw knob for this means leaving `NETWORK_MODE` unset; an
  explicit `NETWORK_MODE=open` still throws when paired with the raw
  knob.)
  Plain-default host-loopback reach is rootless-podman-version/
  `containers.conf`-dependent — verify it against your podman rather than
  assume it.
- **`no-host-loopback`, bwrap — unsupported, rejected at eval.** This is the
  exact cell `NETWORK_MODE=no-host-loopback` formally rejects at `nix eval`
  when `runtime=bwrap` (see [Network mode](#network-mode-network_mode)):
  since issue #2666, `open` already isolates bwrap the same way
  `no-host-loopback` would (pasta-backed, egress works, host loopback
  blocked), so the distinct choice has no distinct rendering to reject into.
  A local Provider is reachable under neither `open` nor
  `no-host-loopback` on bwrap; reopen it with `NETWORK_MODE=host` instead
  (see the `open`, bwrap bullet above), which also reopens general
  host-loopback/LAN reachability, not just for a local Provider.

Reopening host loopback under a restricted-egress config is a deliberate
weakening of that posture — treat it as an explicit opt-in, never something a
Provider selection implies silently.

#### Syscall filter

Every bwrap Box runs under a compiled BPF seccomp filter attached via
bwrap's own `--seccomp FD` flag ([ADR 0054](adr/0054-bwrap-boxes-run-under-a-compiled-syscall-denylist.md),
issue #2670). It is unconditional — no `settings` toggle, no env knob — and
built at nix eval time from `lib/seccomp.nix`.

The filter is a curated **denylist**, not a podman-style full allowlist: it
denies exactly the following syscalls (`SCMP_ACT_ERRNO(EPERM)`) and allows
everything else —

`acct`, `add_key`, `bpf`, `clock_adjtime`, `clock_settime`, `create_module`,
`delete_module`, `finit_module`, `get_kernel_syms`, `init_module`, `ioperm`,
`iopl`, `kexec_file_load`, `kexec_load`, `keyctl`, `lookup_dcookie`, `mount`,
`nfsservctl`, `open_by_handle_at`, `perf_event_open`, `pivot_root`,
`process_vm_readv`, `process_vm_writev`, `ptrace`, `query_module`, `reboot`,
`request_key`, `settimeofday`, `stime`, `swapoff`, `swapon`, `syslog`,
`umount2`, `ustat`, `vm86`, `vm86old`.

A denylist is deliberately the opposite risk profile from podman's own
allowlist default: enumerating every syscall an arbitrary Driver's or
Consumer's toolchain might legitimately need is unverifiable, and a false
negative in an allowlist silently breaks a Dispatch outright. A denylist can
only ever be too narrow — a documented gap to close later, not an outage.

**Known limitations.** `clone`, `unshare`, `setns`, and `personality` are
**not** denied, even though podman's own default profile applies nuanced,
argument-based restrictions to some of them — bare `clone(2)` backs ordinary
thread creation everywhere, and scoping a deny to just the dangerous flag/
value combinations needs argument-aware BPF rules this cut does not attempt.
The filter also covers the **native architecture only** (libseccomp's
`seccomp_init` default) — no 32-bit/multiarch compat-syscall coverage.

**Failure posture.** If the compiled filter file is missing or unreadable,
the launcher warns and proceeds without it rather than refusing to launch
the Box — the same degrade-don't-lie precedent ADR 0042 already applies to
missing cgroup delegation: the filter is defense-in-depth on top of bwrap's
existing namespace/mount isolation, not the sandbox's sole isolation
mechanism.

### Claude Code output caps

Unlike every knob above, `BASH_MAX_OUTPUT_LENGTH` and `MAX_MCP_OUTPUT_TOKENS`
are fixed constants baked straight into the image's `config.Env` by
`lib/image.nix` — Claude Code's own output-cap knobs, not a spindrift
`settings.*` surface: there is no spindrift `--flag` for either, only the
container runtime's own `-e`/`--env` override of a baked `config.Env` entry,
same as any other OCI image env var (issue #1987).

Only `BASH_MAX_OUTPUT_LENGTH` is derived, from `lib/output-caps.nix`'s single
`bashMaxOutputLength` attribute: the research-verdict prompt budget reads it,
`nix/checks/image.nix` reads it to cross-check its own hand-typed pin, and the
docs check below greps this section's prose against it — and a second reader
is what earns a constant the hoist (issue #3669). `MAX_MCP_OUTPUT_TOKENS` has
no second reader, so despite the plural file name it stays hand-typed as a
literal beside the derived line in `lib/image.nix`, and that is the file to
edit to change it.

Cost of a dispatch run is ~99% cache-read, and cache-read scales with
context size times turn count: every token in the conversation is re-read on
every later turn. The bulk of that context is verbose tool output (`nix
build`, `nix flake check`, `go test`, `git log`) accumulating inline. Claude
Code's documented behavior past `BASH_MAX_OUTPUT_LENGTH` chars is to write a
Bash command's full output to a file in the session directory and hand the
model back only a path plus a short preview, with nothing lost on disk — but
the stock default (30,000 chars / 25,000 tokens) is high enough that it
rarely engages. The Box lowers both knobs so that early truncation kicks in
at all:

- `BASH_MAX_OUTPUT_LENGTH=8192` — bash tool output past 8 KB stops reaching
  the model whole: the documented spillover above if the client behaves as
  documented, a hard cut on a dispatch run (see below). High enough that a
  short `git log`/`git status` still returns inline, low enough to catch the
  `nix build`/`go test` firehoses this ticket's cost data (see #1987) flags
  as the dominant cache-read cost.
- `MAX_MCP_OUTPUT_TOKENS=2000` — the documented file-plus-preview spillover
  described above, applied to MCP tool output rather than bash, not an error,
  once a result exceeds the threshold (a server-declared
  `anthropic/maxResultSizeChars` tool still overrides this per-tool). Spindrift
  has no MCP server configured today, so there's no live traffic to size this
  against; picked deliberately far below the 25,000-token default since a
  future MCP addition should default to file-based inspection for a large
  result rather than growing the transcript, with headroom to raise it
  per-server via the annotation above if a legitimate large-result tool shows
  up.

**What a `--output-format stream-json` run actually records is a hard cut,
not a spillover.** On a Box log — verified on issue #3605's — the
`tool_use_result.stdout` of an overflowing Bash call is exactly 8192
characters long and ends mid-token, with no spill-file path and no
truncation notice of any kind. Whether the interactive client behaves as
documented above is untested here; what a dispatch run gets is the cut.
That matters beyond cost, because some Bash results are load-bearing rather
than merely informative: under the `log` Signal carrier, every host-relay
control signal — `SPINDRIFT_PR_INTENT`, `SPINDRIFT_ISSUE_INTENT`,
`SPINDRIFT_COMMENT` — rides back to the launcher as a base64 line the host
parses out of the echoed output, so the cap is each one's size limit too.
`BOX_SIGNAL_CARRIER=socket` moves all three channels off the log entirely
(see [Signal socket](#signal-socket-box_signal_carriersocket) below), so
the cap is not their size limit there at all. On the `log` carrier, the
intent lines stay well under it in practice; the research verdict is the
one that does not, since a read-only research Box hands a whole comment
body back on a single `SPINDRIFT_COMMENT` line, so it is the case this
budget text is written for — 8192 characters less the 51 the marker and
nonce take, leaving 8141 characters of base64 and 6105 bytes of Markdown
before encoding (issue #3669). Every `log`-carrier verdict fragment that
carries a `SPINDRIFT_COMMENT` line states that budget, and two checks in
`nix/checks/prompts.nix` derive the numbers — the fragments' via
`research-verdict-comment-line-fragments-state-carrier-and-payload-budget`
and this paragraph's via
`research-verdict-budget-numbers-in-reference-docs-match-the-baked-cap` —
from `lib/output-caps.nix`, so neither can drift from the baked cap.

Both are asserted directly on the built image's `config.Env` by
`nix/checks/image.nix`'s `output-cap-env-marker` check, the same
way `nix-store-writable-env-marker` verifies `NIX_STORE_WRITABLE` above.
That check pins both values by hand: it does not read
`BASH_MAX_OUTPUT_LENGTH` from `lib/output-caps.nix`, so it stays an
independent assertion about the *built* image rather than a restatement
of `lib/image.nix`'s own import. That pin is instead cross-checked
against `lib/output-caps.nix` at eval time, so drift throws instead of
the check passing on a stale number.

Under the `log` Signal carrier, the `-e` override above is a runtime knob
only: it changes what the Box's Bash tool cuts, not what the fragments
say, so overriding `BASH_MAX_OUTPUT_LENGTH` leaves them quoting the
build's numbers. A lowered cap can also move the cut onto a payload
length that *is* a multiple of four, where a truncated payload decodes
cleanly and half a verdict posts as though it were whole. Changing
the cap therefore means editing `lib/output-caps.nix` and rebuilding,
which re-derives the numbers above and re-asserts that invariant in
`nix/checks/prompts.nix` — and also updating `output-cap-env-marker`'s
hand-typed pin in `nix/checks/image.nix` to match, which the eval-time
throw above forces rather than leaving to be remembered.

### Bash command-output interceptor

The output caps above only engage once a command's output crosses the
threshold, and what they leave is the start of the output — the wrong end
for a build error. Issue #1988 adds a uniform, error-oriented interceptor
on top: a PreToolUse/PostToolUse hook pair, baked
into the image alongside `reject-background-bash.sh` and the other hooks
described in [Self-inflicted secret reads are structurally
blocked](#self-inflicted-secret-reads-are-structurally-blocked), that applies
to *every* Bash call, not just the ones that overflow, and hands back a **tail**
of the output rather than a head — build/test failures sit near the end.

- **`agent/bash-output-tee.sh`** (`PreToolUse`, `Bash` matcher) rewrites
  every Bash call, via the same `hookSpecificOutput.updatedInput` mechanism
  `env-credential-scrub.sh` uses, to wrap the original command in a group
  that tees its combined stdout+stderr to a per-command log file under
  `/tmp/spindrift-bash-output/`, then re-exits with the original command's
  own status (captured via `PIPESTATUS` before `tee`'s own exit status would
  otherwise shadow it) — both back to Claude Code and into a `<log>.exit`
  sidecar file for the PostToolUse hook to read. The log directory lives
  under `/tmp`, so it doesn't outlive the Box container itself; a single
  run's total log volume is bounded by however many Bash calls that run
  makes, not by anything longer-lived.
- **`agent/bash-output-summary.sh`** (`PostToolUse`, `Bash` matcher) recovers
  the log path by parsing the `} 2>&1 | tee -- "<path>"` line out of
  `tool_input.command` (the two hooks are separate processes with no shared
  state, so the rewritten command text is the only channel between them) —
  anchored on that full line, and on the last match, so a user command that
  happens to contain its own `tee -- "..."` text can't be mistaken for the
  wrapper's own marker. A log under the inline bound (default 4096 bytes) is
  left untouched — no needless round-trip to read a file back for output
  that already fits. Past the bound, the hook replaces the tool result via
  `hookSpecificOutput.updatedToolOutput` with the exit code, the log file
  path, and the last N bytes of the log — the full output stays on disk the
  whole time; the agent greps/reads the file directly for anything the tail
  doesn't cover.

One other PreToolUse hook already shares the `Bash` matcher and rewrites
`tool_input` independently (`env-credential-scrub.sh`; `credential-deny.sh`
only ever denies, it never rewrites). Claude Code does not document how
multiple hooks' `updatedInput` responses for the same call compose when more
than one rewrites it, so rather than risk `env-credential-scrub.sh`'s own
`unset ANTHROPIC_API_KEY CLAUDE_CODE_OAUTH_TOKEN` rewrite silently losing to
this one, `bash-output-tee.sh` duplicates that same `unset` as the first line
inside its own wrapped command — redundant if both hooks' rewrites do compose,
but correct either way.

### Declared touch-set overlap

An issue body may declare the paths it expects to change in a `## Touches`
section — a bullet list of path globs, parsed the same way regardless of
`ISSUE_TRACKER` (unlike dependency edges, where the `jira` adapter resolves
native issue links and the `github` adapter prefers its own native
issue-dependencies relationships, both falling back to `## Blocked by` prose
only where applicable — see [Issue Tracker backends](#issue-tracker-backends)):

```markdown
## Touches

- lib/env-schema.nix
- cmd/launcher/*.go
```

With `OVERLAP_GATE=defer` (the default), dispatch holds a Dispatchable issue
whose declared touch-set intersects the declared touch-set of any currently
`agent-in-progress` issue, retrying once the colliding issue
completes. An issue with no `## Touches` section, or one whose touches never
intersect an in-progress issue's, dispatches immediately — the gate only ever
delays dispatch, it never fails an issue. Set `OVERLAP_GATE=off` to disable
the check entirely.

This bounds wasted work from parallel issues that rewrite the same generated
surface (schema-derived artifacts, a shared config file) and collide
repeatedly at rebase time — the same drift-bounding spirit as the [Merge
guard](#merge-guard), making no adversary-proofing claims: a `## Touches`
section is declared by whoever files the issue, not verified against the
diff it eventually produces.

The gate only compares a candidate against issues already `agent-in-progress`
— two Dispatchable issues in the same batch with overlapping declared
touches, neither yet in progress when the check runs, still dispatch
together.

**Inferred touch-sets (v2, `CODE_FORGE=github` only).** A declared
`## Touches` section only bounds what its author thought to write down. On
the `github` Code Forge, the gate augments each in-progress issue's declared
touch-set with the actual changed files of its open PR (fetched once per
wave, alongside the declared touches), so a candidate still holds against
files the in-progress issue never declared. An in-progress issue with no open
PR yet contributes only its declared touch-set — no error, no over-blocking.
Off `github` — where there is no PR to inspect — and for any in-progress
issue whose PR-file fetch fails, the gate falls back to declared-only
behavior exactly as above. Conversely, a failed fetch of an in-progress
issue's own declared touches (e.g. a transient `gh issue view` error) falls
back to its open PR's changed files only, printing a diagnostic naming the
issue so the gap is visible rather than silent — the check itself never
errors.

---

## How a run works

### Runtime flow

```
spindrift dispatch   (the nix-built Go launcher, host-side)
  └─ gh issue list --label ready-for-agent        (find the work)
     └─ for each issue, up to MAX_PARALLEL at once:
        podman run  spindrift:latest               (disposable box)
          └─ /agent/entrypoint.sh
             ├─ git clone <REPO_SLUG>  +  git checkout -b agent/issue-N
             ├─ run PREFETCH (optional cache warm-up)
             └─ run_driver_in_env  (assembles the prompt/--agents JSON, mounts
                the session cache, then hands off to one of:)
                ├─ driver-exec                 (default: one Driver pass)
                │  └─ claude -p "<prompts/issue-prompt.md>" --dangerously-skip-permissions
                └─ orchestrator                (ORCHESTRATOR_ENABLED=1: N passes)
                   └─ driver-exec × N, one fresh Driver session per pass,
                      seeded from a run-state handoff file — see [In-box
                      orchestrator](#in-box-orchestrator)
                (either path, inside the Driver pass itself:)
                implement → check → commit → push → self-review (reviewer subagent)
                   → open PR as a draft
                   → print  SPINDRIFT_OUTCOME issue=N landing=<url> status=ready
        │
        └─ back on the host, the launcher runs the MERGE GATE for that issue:
           ├─ poll CI on the PR head until green (or red, or timeout)
           ├─ green → flip the PR out of draft, apply MERGE_MODE, then swap
           │          issue to agent-complete once the landing path settles:
           │           manual    → leave the green PR for a human (default)
           │           immediate → rebase-merge the PR now (rebase-retry and
           │                       an agent conflict-resolve box keep the
           │                       issue agent-in-progress until this settles)
           │           auto      → enqueue the forge's native auto-merge
           ├─ red   → capture the failed checks + a bounded log excerpt
           │          (best-effort), then dispatch fix boxes (up to
           │          MAX_FIX_ATTEMPTS, each driving prompts/fix-prompt.md
           │          instead of issue-prompt.md — the branch is already
           │          checked out and CI_FAILURE_SUMMARY carries the known
           │          failure, so the box skips SCOUT, re-implementation,
           │          and blind rediscovery and goes straight to check/fix/
           │          commit/push), then re-gate
           ├─ stale base, no conflict (immediate) → merge as-is by default;
           │                                        only when PREFLIGHT_STALE_BASE
           │                                        is on, preflight-rebase and
           │                                        re-wait for green first
           │                                        (up to MAX_REBASE_ATTEMPTS)
           ├─ merge conflict (immediate) → flip the PR back to draft (a
           │                               visible not-mergeable signal),
           │                               rebase the PR (up to
           │                               MAX_REBASE_ATTEMPTS), and once the
           │                               resolved head re-reaches green,
           │                               flip it back to ready before the
           │                               retried merge — both flips are
           │                               best-effort, same as the
           │                               draft-out-at-green flip above
           └─ post an aggregate usage/cost comment to the issue
```

Before assembling the prompt, the launcher reads the subject issue's body
plus its last-10-comment snapshot host-side and forwards it into the Box as
the `ISSUE_TEXT` env var (issue #3445) — the Box never fetches its own
subject issue from the tracker any more. `promptassembly` renders it into a
fenced `# ISSUE TEXT` section (a short preamble marking it authoritative and
untrusted, then the text itself inside a CommonMark-safe fence) and appends
that section to the assembled prompt and to the review prompt, after
everything else; it is also available as the `${ISSUE_TEXT}` substitution
var in any template, fragment, or agent prompt file that references it
directly (only `scout-prompt.md` does, since the scout is the one subagent
prompt assembled outside that automatic append). The host-side text is
capped at `forge.maxIssueTextBytes` (64KB); a truncated thread ends with an
explicit `[truncated: ...]` marker line rather than being silently cut.
`ISSUE_TEXT` travels through the runner process's own environment, never on
its argv — a bare `-e ISSUE_TEXT` under OCI, no `--setenv` under bwrap at all
— the same route credentials take (`offArgvKeys`), since a command line is
world-readable via `ps`/`/proc` for the Box's whole lifetime while a process
environment is owner-readable only (issue #3470).

The split is deliberate: the **Box** owns implementing the issue and opening the
PR, but the **launcher** (host-side, the Go binary) owns the CI-green decision,
the merge, and the terminal label swap — a Box cannot approve or merge its own
PR, and keeping merge authority outside the throwaway container is what makes
branch protection meaningful. `agent-complete` marks the landing path settled
— CI is green **and** `MERGE_MODE` has run its course (merged, auto-merge
enqueued, handed off, or merge-blocked-with-note) — so the label never claims
"nothing left to do" while a rebase-retry or conflict-resolve box might still
be running. **Which of those outcomes happened is the `MERGE_MODE` policy**,
decoupled so the same run can land PRs automatically or hand green PRs to a
human reviewer. The
Box's last line is a machine-readable `SPINDRIFT_OUTCOME` line (grammar in
`cmd/launcher/internal/outcome`) that tells the launcher which PR to gate.

The harness never touches the Target repo's working tree on your host — it all
happens through fresh clones inside containers — so it can drive **any** GitHub
repo you point `REPO_SLUG` at. `Closes #N` in the PR description closes the issue
when the PR merges — by the launcher (`immediate`), by GitHub (`auto`), or by a
human (`manual`).

**`CODE_FORGE=git`** (push-only, [ADR 0013](../docs/adr/0013-issue-tracker-and-code-forge-are-independent-seams.md))
replaces everything from *open PR* onward: the Box pushes its branch to
`CODE_FORGE_REMOTE_URL` and prints `SPINDRIFT_OUTCOME ... landing=agent/issue-N status=ready`
— no PR, no CI-watch. The launcher skips the CI-poll entirely (there is
nothing to poll) and swaps the issue straight to `agent-complete`, then
applies `MERGE_MODE` as a plain push: `manual` leaves the branch as pushed,
`immediate` merges it onto the target branch. `auto` has no meaning off
a `PRForge` backend and is rejected at startup.

`immediate`'s merge-and-push to `CODE_FORGE_REMOTE_URL` runs on the
**launcher host** (a throwaway local clone, not inside a Box), reusing
`GIT_USER_NAME`/`GIT_USER_EMAIL` as the merge commit's identity. The host
needs its own push credentials for that remote (e.g. an SSH key or
credential helper covering `CODE_FORGE_REMOTE_URL`) — separate from the
Box's `GH_TOKEN`, which only covers the Issue Tracker. A push auth failure
surfaces as a `merge-blocked` comment on the issue, not a crash.

### In-box orchestrator

`ORCHESTRATOR_ENABLED` (default off, `boxEnv`-only — see `lib/env-schema.nix`)
swaps which binary `run_driver_in_env` hands the assembled prompt/`--agents`/
session flags to. Off, `entrypoint.sh` calls `driver-exec` directly and the
box makes exactly one Driver pass, as it always has. On, the identical flag
set goes to `orchestrator` (`cmd/launcher/orchestrator`) instead — a second
in-box Go binary, built the same hermetic way as the launcher itself
([ADR 0007](adr/0007-runtime-logic-is-a-nix-built-go-binary.md)) — which loops
`driver-exec` for as many passes as the implementor's own review verdicts and
two numeric caps call for
([ADR 0035](adr/0035-the-in-box-orchestrator-loop-is-a-go-program-above-driver-exec-not-entrypoint-prose.md)).
`entrypoint.sh`'s own job — prompt/`--agents` assembly, session-cache
mounting — is unchanged either way.

Each pass after the first runs the Driver **sessionless** (no `--resume`,
ever) — only the very first pass carries the box's initial session pin
verbatim — so continuity across passes comes from a compact run-state
artifact, not a growing transcript:

- **Run-state handoff.** A JSON file (`/tmp/run-state.json` by default,
  outside the repo like `/tmp/brief.md`) records the last reviewer verdict,
  the reviewer's own findings text, the scout-brief path (`--scout-brief-path`,
  default `/tmp/brief.md`), the most recent pass's own pass-summary path
  (`--pass-summary-path`, default `/tmp/pass-summary.md`), the most recent
  fix pass's own dispositions path (`--dispositions-path`, default
  `/tmp/dispositions.md`, issue #2550), the append-only decisions log path
  (`--decisions-path`, default `/tmp/decisions.md`, issue #2695), and the
  reviewed-commit anchor (`ReviewedCommitAnchor`, issue #2551) — the repo
  workdir's own `HEAD` commit SHA, recorded via one `git rev-parse HEAD`
  invocation right after each review pass completes. That recording is
  best-effort: an `os.Getwd` or `git` failure, or `git` output that doesn't
  look like a real commit SHA once trimmed, just logs to stderr and leaves
  the anchor at whatever a prior review pass already recorded (or empty, on
  the first pass), never errors the run. It also carries dispatch-internal
  bookkeeping unrelated to any seeded prompt: the done/remaining slice lists
  (`DoneSlices`/`RemainingSlices`). Each pass reads it before running and writes it back after, through
  a temp-file-then-rename so a mid-write kill can never leave a half-written
  file behind. A missing or corrupt file degrades to a cold start, never an
  error.
- **Seeded prompts.** Before any pass whose run-state carries prior data (in
  practice every pass after the first, though a warm-started state file would
  seed pass 1 too), the orchestrator prepends a "Run-state handoff" section —
  last verdict, reviewer findings, scout-brief path, pass-summary path,
  decisions log — to the original prompt, so a fresh implementor pass knows
  where a prior pass left off without reading its transcript.
- **Round-N review-prompt seeding.** A round-N (N>1) review pass gets its own,
  narrower seeded section instead: its own prior verdict message plus the
  append-only dispositions log's content, both verbatim, framed as claims to
  verify against the diff — nothing else from the implementor (no pass
  summary, no scout brief, no worker findings) reaches this prompt. Round 1's
  review prompt is always unseeded. A missing dispositions log degrades to
  seeding the prior verdict alone, never an error.
- **Delta focus.** When the run state's reviewed-commit anchor looks like a
  real git commit SHA (7 to 64 lowercase hex characters, covering both a
  SHA-1 and a SHA-256 repo), the same round-N review prompt also gets a
  "### Delta focus" section naming the range since that anchor — `git diff
  <anchor>..HEAD` and `git log <anchor>..HEAD --oneline` — as where to
  concentrate the hunt (issue #2551). The full branch diff stays available
  throughout; this narrows where the reviewer spends its attention, never
  what it's allowed to see, and territory outside that range (assumed
  already covered by the prior review pass) is re-examined only where a new
  commit actually touches it. The section also requires the reviewer to
  re-skim the FULL diff's shape end to end before it may issue APPROVE,
  regardless of the delta focus above — delta review must never
  narrow final approval's own coverage. A missing or invalid-looking anchor
  omits the section entirely, degrading to the unchanged full-review prompt,
  never an error.
- **Dispositions log.** Each fix pass's own fresh `--dispositions-path` file
  is appended, one "## Round N" section at a time, to a per-run,
  append-only log (`DispositionsLogPath`) — an earlier round's won't-fix
  entry is never dropped or collapsed just because a later
  round wrote its own dispositions file, so a round-N reviewer sees every
  disposition decided so far. This stays safe only because each entry is a
  terse reference (a commit SHA, a `file:line`, an issue number) rather than
  restated diff/file/transcript content: a round whose mean estimated
  tokens-per-entry, or whose total estimated tokens across every entry,
  exceeds a fixed ceiling is flagged loudly (a `run_state_error` op with
  `phase: dispositions_budget`) as a tripwire for an entry restating
  content instead of referencing it — the total check is what actually
  catches a pasted diff hunk or file excerpt, many individually-short
  lines that would keep the mean check alone from ever tripping — never a
  budget the agent is meant to trim into.
- **Decisions log.** Each implement/fix pass's own fresh `--decisions-path`
  file (default `/tmp/decisions.md`) is appended, one "## Round N" section at
  a time, to a per-run, append-only log (`DecisionsLogPath`) — the same
  append-only shape, reference-only contract, and token-budget tripwire
  (flagged as a `run_state_error` op with `phase: decisions_budget`) as the
  dispositions log above. Unlike the dispositions log, which seeds only the
  round-N *review* prompt, the decisions log seeds every *implement/fix*
  pass's prompt (pass N>1). A missing or unreadable decisions log degrades to
  an unseeded prompt, never an error.
- **Code-owned caps.** `--max-review-rounds`/`--max-slices` are
  `driver-exec assemble-prompt`'s own flags (issue #2975): assemble-prompt
  folds them straight into the handoff document's `Caps.MaxReviewRounds`/
  `Caps.MaxSlices` fields, and the orchestrator reads them off the loaded
  handoff rather than taking either as its own flag — neither `orchestrator`
  nor plain `driver-exec` declares a `-max-review-rounds`/`-max-slices` flag
  of its own any more. `--max-review-rounds` (default 3) caps additional
  passes a `BLOCK` verdict may trigger; `--max-slices` (default 9) caps the
  implement/fix/review invocation count regardless of verdict; either set to
  `0` disables that cap. Every `driver-exec` invocation this loop makes —
  implement, fix, review, and the terminal land pass alike — increments the
  same pass counter, but `--max-slices` only bounds the passes that precede
  the cap firing: the case fires once that counter reaches `--max-slices`,
  and rather than stopping there it commits the run to one further pass
  beyond the cap — the terminal land pass (issue #2457) — so a run whose
  `--max-slices` cap fires makes `--max-slices + 1` total `driver-exec`
  invocations, not `--max-slices`. `--max-review-rounds` tracks a separate
  `reviewRounds` counter that only increments when a review pass's verdict is
  `BLOCK`. The loop stops the instant a pass's log carries a terminal
  `SPINDRIFT_OUTCOME` line — unconditionally, ahead of either cap.

  Because both counters advance over the same pass sequence,
  `--max-review-rounds` can only actually reach `N` rounds — rather than
  being silently shadowed by `--max-slices` firing first — if `--max-slices`
  is large enough, and the minimum depends on which driver loop is running.
  With the code-owned review pass enabled (the handoff's `ReviewPromptFile`
  field non-empty, the default — see below), implement and review are
  separate `driver-exec` invocations, so the minimum is `2N + 3`: 1 initial
  implement pass + (`N`+1) review passes + `N` fix passes + 1 terminal land
  pass. Without a review pass (the legacy single-loop path, reached only
  when `ReviewPromptFile` is empty), each pass folds its own review in
  inline instead of splitting implement/review into separate invocations,
  so the minimum is one less than half: `N + 2`. The shipped defaults sit
  exactly at the review-pass loop's minimum for `N=3`: `--max-slices=9` is
  `2*3+3`. When both caps are non-zero and `--max-slices` falls below
  whichever minimum applies, the orchestrator surfaces the incoherence as a
  warning printed to stderr naming the minimum `--max-slices` the given
  `--max-review-rounds` needs — the run still proceeds, with
  `--max-slices` shadowing `--max-review-rounds` exactly as it would have
  gone unwarned before.

  A third cap, `--max-budget-tokens`/`--max-budget-usd` — also
  `assemble-prompt`'s own flags now, folded into `Caps.MaxBudgetTokens`/
  `Caps.MaxBudgetUSD` the same way as the two caps above (issue #2694; both
  default `0`, disabled) — bounds the review-pass loop's own review-round
  decision by cumulative spend instead of a pass or round count: once
  cumulative token or USD usage across every pass *this Box has run so far*
  (implement, fix, and review passes alike, plus any dispatched worker's
  own spend) would meet or exceed the configured cap, a further
  `BLOCK`-triggered review round instead commits the run to one terminal
  land pass, the same terminal-land mechanism `--max-review-rounds`/
  `--max-slices` already use. This sum is fresh per Box invocation, not the
  host launcher's own cross-Box `selfHealGate` figure (`dispatch.
  CumulativeUsage`, which sums every attempt's log across the whole issue,
  including a prior fix-pass Box) — a run whose implementor loop spans
  several fix-pass Boxes gets a fresh budget in each one, not one shared
  total. Either dimension alone can trip it; a negative or malformed value
  degrades to `0` (disabled) rather than erroring, mirroring the host
  launcher's own tolerance for the identical `MAX_BUDGET_TOKENS`/
  `MAX_BUDGET_USD` env vars (`atoiNonneg`/`floatNonneg`). The two CLI
  wrappers that parse the raw string — `driver-exec assemble-prompt`'s and
  `driver-exec env-handoff`'s own `--max-budget-tokens`/`--max-budget-usd`
  flags — degrade a malformed value silently, the same as the host launcher:
  neither has an operator-facing diagnostics channel to log to. Only
  `orchestrator`'s own defense-in-depth clamp against an already-loaded
  handoff's typed (`int`/`float64`) `Caps.MaxBudgetTokens`/`Caps.MaxBudgetUSD`
  fields — reachable only via a hand-edited or otherwise corrupted handoff
  file, since both producers already reject a negative value before writing
  one — logs one stderr line naming the degraded value, since the Box has no
  other channel back to an operator. Unlike
  `--max-review-rounds`/`--max-slices` — both consulted by the
  legacy single-loop path too — `--max-budget-tokens`/`--max-budget-usd` is
  consulted only by the code-owned review pass's own decision: the legacy
  loop (`ReviewPromptFile` empty) still receives the same `Caps.MaxBudgetTokens`/
  `Caps.MaxBudgetUSD` values off the handoff but never reads them, so a run
  on that path spends past a configured budget cap with no warning at all.
  Forwarded from the same `MAX_BUDGET_TOKENS`/
  `MAX_BUDGET_USD` knobs the [Advanced tuning](#advanced-tuning) table's
  `selfHealing` group already documents — see that table for the
  operator-facing env var/`settings` surface.

**Code-owned review pass (issue #2037).** The review pass is enabled whenever
the handoff document's `ReviewPromptFile` field is non-empty (issue #2975):
`driver-exec assemble-prompt` sets it, via its own `--review-prompt-output`
flag, to the path it wrote the rendered `review-prompt.md` text to, and only
when that pass's `Assemble` call actually rendered one. `entrypoint.sh`
passes `--review-prompt-output` unconditionally — `phase_prompt_assembly`,
the one call site that builds the `assemble-prompt` invocation, runs
unconditionally from `main()` regardless of ORCHESTRATOR/research/`FIX_PASS`.
The real gate lives inside `Assemble` itself: `Result.ReviewPromptText` is
populated only when the orchestrator is on, this is the default fresh-work
dispatch, and `FixPass == 0`, so the flag is always passed but the file it
names is only ever non-empty under those conditions — this is a consumer of
ADR 0035's master switch, not a separate sub-knob: turning the orchestrator
on always drives the review pass. The orchestrator itself takes no
review-prompt flag of its own; it reads `ReviewPromptFile` straight off the
loaded handoff, the same way `driver-exec` does for the pass whose
`--top-level-role` is `reviewer`.
The review pass's own model/effort travel the same way, as the handoff's
`ReviewModel`/`ReviewEffort` fields — but unlike `ReviewPromptFile`, neither
is a passthrough of an `assemble-prompt` flag: `Assemble` itself extracts
them from the `reviewer` entry of the `AGENTS_JSON_TEMPLATE` environment
variable `entrypoint.sh` exports (the nix-baked roster reflecting
`REVIEW_MODEL`/`REVIEW_EFFORT` — see the table above)
before stripping that entry from what becomes `--agents`, so the review pass
never provisions its own `reviewer` subagent. An operator's explicit
dispatch-time `REVIEW_MODEL`/`REVIEW_EFFORT` — forwarded by the launcher as
`BOX_REVIEW_MODEL_OVERRIDE`/`BOX_REVIEW_EFFORT_OVERRIDE` only when actually
set at dispatch time, never a baked default — binds over the extracted
values last (issue #3171), so a per-run override needs no image rebuild. `driver-exec`'s role-aware
resolution applies `ReviewModel`/`ReviewEffort` only on the reviewer-role
pass, overriding the coordinator's own `Model`/`Effort` field by field: an
empty `ReviewModel` falls back to the coordinator model, matching the
pre-#2277 behavior, and an empty `ReviewEffort` means the review pass follows
the roster reviewer entry's own effort rather than the coordinator's, a
non-empty value overriding either (issue #2512).
An implement/fix pass's own REVIEW section is stripped to a deferral
(`review-loop-orchestrator.md`) that stops the turn right after COMMIT unless
the seeded run-state above it already shows an `APPROVE` verdict, and the
`reviewer` entry is dropped from `--agents` entirely (verdict authority moves
fully to the review pass). The loop this drives is implement → review →
(`BLOCK`) fix → review → … → (`APPROVE`) land, where "land" is its own
distinct terminal role reached via an `APPROVE` verdict (or a cap), not
another fix-role pass — `scanReviewLog` (not `scanPassLog`) reads the review
pass's own verdict and Blocking/Non-blocking findings text, both carried
into the next fix or land pass's seeded prompt via run-state. Each pass's
`pass_start` marker carries its role (`implement`, `review`, `fix`, `land`,
or `delta-review`) so telemetry can tell them apart. That pass number,
carried by both `pass_start` and `pass_usage`, is manifest-anchored and
keeps counting up across a nudge resume, unlike the process-local
counter `passmachine`'s caps and session-reuse logic measure, which
restarts at 1 each re-invocation (issue #3091). Cost is not assumed
lower than the inline loop — a review pass re-reads the diff cold instead
of sharing the implementor's warm cache — the win this pass is scoped for
is review quality and code-owned termination, measured via the A/B harness.

Right after each pass's own log is read — before the next pass
truncates it — the orchestrator emits a `pass_usage` marker (issue
#3156) carrying that pass's number and the same role vocabulary as
`pass_start` (empty on the legacy single-loop path, which
distinguishes no roles). Its payload carries the pass's distinct
API-call count and its uncached-input/output/cache-read/
cache-creation token totals, plus a per-agent breakdown: the main
loop (`main`) first, then each spawned subagent keyed by its
`subagent_type`, ordered costliest first so a single expensive
worker is identifiable at a glance. The uncached-input/cache-read/
cache-creation totals are summed over the per-agent rows,
deduplicated by `message.id` — a multi-content-block assistant
message is re-emitted once per block with byte-identical usage, so a
raw event sum double-counts. This deliberately does not reconcile
with the result-event header figure (`usage.Report`'s own `Totals`)
— the deduplicated per-message sum is the one that matches billed
usage. The output-token figure is the one exception: the stream's
per-message `output_tokens` is a placeholder, not ground truth
(issue #3213) — summing it under-counts by roughly two orders of
magnitude on a real run — so `output` is sourced from the pass's own
result event instead, is reported for the main loop only, and every
subagent row's output is always `0` since the stream carries no
ground-truth figure for a subagent's own output. The payload states
that itself via `output_is_main_loop_only`, and only then does the
heartbeat mark the figure `out (main loop)` — a driver whose own
stream does carry whole-pass output (opencode) leaves the flag unset
and its `out` column unqualified. A pass that crashed
or produced no usage events emits a zero-valued summary rather than
nothing, so the marker is present for every pass.

Right after the terminal land pass returns, the orchestrator emits a
`land_delta` marker (issue #3244) carrying what landing changed relative to
the tree the reviewer APPROVEd (`runstate.ReviewedCommitAnchor`, issue
#2551), computed rebase-invariantly: if the land pass rebased the branch
onto a base that moved during review, comparing the branch's own patch
content on each side of the rebase — rather than diffing straight across it
— keeps base movement out of the count. A missing or invalid anchor, or any
git failure along the way, degrades to an explicit "unknown" delta rather
than an error, the same fail-open contract as the anchor itself; a delta of
zero is also stated explicitly rather than omitted, so a reader never has to
wonder whether the marker is simply missing. The same delta is recorded on
the pass manifest's land entry (`land_delta`, alongside `pass`/`kind`/
`verdict`/`outcome_found`/`usage`) and, host-side, appended as a trailing
one-line section of the PR body settle opens or adopts — "known", zero, and
"unknown" all render there, and a manifest with no land entry (an older Box)
appends nothing.

The delta also records, per changed path, the changed-line ranges the land
pass touched (issue #3503): each Range is a pre-image hunk from the
anchor-to-HEAD diff, deliberately kept in the reviewed anchor's own
coordinates — the same review pass that set the anchor also recorded
the findings whose `path:line` citations already live in that space,
so a pre-image range is directly comparable to a finding's line while a
new-side range would not be. `count == 0` marks a pure insertion, landed
immediately after old line `start`, distinguishing an insertion from a
modification. A path whose pre-image hunks cannot be determined is simply
absent from the ranges — a binary file, say; a mode-only change, which
numstat counts as `0 0 <path>` while the `-U0` diff renders only the mode
lines and no hunk at all; on a rebased branch, a path the moved base also
changed, whose anchor-to-HEAD hunks mix the land pass's edits with the
base's and cannot be told apart in the anchor's coordinates; a renamed
path, which numstat reports as a composite key (`d/{old.txt => new.txt}`
when the two paths share a directory, `old.txt => new.txt` when they do
not) that no diff header's own path can match — and which, at 100%
similarity, yields no hunks either way; or a path git renders quoted
(`core.quotePath`, e.g. non-ASCII), a shape the header regexes don't
match. Such a path still counts in
`files`, `insertions`, `deletions` and `paths`, which the rebase-invariant
numstat comparison answers outright. The ranges are absent entirely from
an unknown delta, the same degradation the rest of the delta already
takes.

Once the terminal land pass reaches its own `status=ready` outcome — after
its own markers above are emitted, before the host applies the landing —
the orchestrator runs one more bounded gate (issue #3246): does anything
that actually landed need a human's eyes before this run is allowed to
settle? The gate fires at most once and is itself terminal, never a loop,
so a run that fires it spends exactly one extra pass, never a lap back
through fix/review. It fires on either of two inputs: the land pass's own
fresh `/tmp/decisions.md` (issue #3245) declaring gate-discovered work —
read from that pass's own fresh decisions file, not the accumulated
across-passes decisions log, so an earlier pass's own mention of the phrase
can never false-fire it — or a recorded land delta (issue #3244) whose
touched lines land outside what the approving round's own findings vouch
for (issue #3504): a finding's `path:line` citation vouches for that line,
widened by a small tolerance window, not the whole file, so the gate fires
when the delta touches lines beyond those windows; a finding that names a
path with no line still vouches for the whole path; and a path the delta
touched but whose pre-image ranges aren't determinable — the same dropout
cases enumerated above — fails open rather than firing, the same posture
an unknown delta takes. A delta whose touched lines fall within what the
findings already cover, the common case, fires nothing and costs the run
nothing. An unknown delta — the same "unknown" case the
`land_delta` marker above already renders explicitly — does not fire on
its own either: there is nothing to compare it against, so the gate
degrades rather than escalates, the same fail-open posture every other
place this anchor is consulted already takes.

Firing still costs nothing when the run is already at a cap:
`--max-slices` or a token/USD budget that would fire skips the extra pass
outright rather than deferring it, since the run is already committed to
landing and there is no later pass to defer to. A `delta_review_trigger`
marker carries the fire-or-skip decision and its reason on both the fire
and the skip path, so the gate's reasoning is visible even when it never
spends the extra pass. A gate that does fire emits its own
`pass_start`/`pass_usage` markers under role `delta-review` and appears in
the pass manifest with that same kind, its own verdict, and its own usage,
counted like any other pass. An `APPROVE` verdict settles the run exactly
as it would have without the gate; `BLOCK` prints a corrective
`status=blocked` outcome line carrying the findings as its note, picked up
by the launcher's own last-line-wins log scan — the same mechanism
`bundleout`'s own corrective outcome already relies on — and posted to the
tracker for a human to triage, never a further fix lap. A pass that
produces no verdict at all fails open and settles too, so a malfunctioning
gate can never strand a branch the review pass already approved.

Every implement/fix/land pass's own COMMIT section also carries one more
fragment on the same `REVIEW_LOOP_ORCHESTRATOR` gate
(`commit-rework-orchestrator.md`, issue #2698) — the review pass itself
has no COMMIT section to carry it. It renders on every implement/fix/land
pass alike, including the first, but branches in prose on the seeded
handoff: once that handoff shows a `Last reviewer verdict:` line, this
overrides the shared "several small focused commits" preference and
tells the pass to fold each finding fix into the commit it belongs to
instead; the implement pass authoring the first slice, seeded with no
verdict yet, keeps the unmodified preference.

The land pass — reached via an `APPROVE` verdict — carries its own fixed work
order on the same `REVIEW_LOOP_ORCHESTRATOR` gate
(`land-pass-order-orchestrator.md`, issue #3214), scoped in prose to a seeded
`Last reviewer verdict: APPROVE` since one prompt serves every pass: rebase
onto the base first, then fold the kept non-blocking fixes, then run the
repo's full check gate once, then finish the pass. That work order also
bounds what step 2 may fold (issue #3245): a fold traces back to a
reviewer's own listed non-blocking finding, or to the mechanical churn a
rebase conflict or a formatter/linter run leaves behind, and anything else
the gate turns up is gate-discovered work — whose default is file, don't
fix, with an inline fix owing a declaration in both the outcome note and
the pass's own `/tmp/decisions.md`. The fragment carries the rule in full;
issue #3221's land pass landing a gate-discovered test-setup commit the
reviewer never saw is the motivating case.

The Driver stays pluggable ([ADR 0009](adr/0009-agent-cli-is-a-pluggable-driver.md)):
the turn's Driver/model/effort/argv-shape/devshell facts all ride the shared
handoff document instead of a per-pass flag surface (issue #2975,
[ADR 0046](adr/0046-the-turn-configuration-crosses-the-box-seam-as-one-handoff-document.md)).
`--handoff-file` is the one flag both `orchestrator` and `driver-exec`
require; `--prompt-file`/`--session-file`/`--log-path` remain genuinely
per-pass flags on both (falling back to the handoff's own `PromptFile` when
`--prompt-file` is omitted), and `--agents-file` no longer exists at all —
the roster rides the handoff's own `AgentsFile` field instead.

### Scout brief

When a `scout` is provisioned, its brief is persisted to `/tmp/brief.md` —
outside the repo, deliberately never a file in the working tree, and a
deliberate departure from issue #3157's original "in the workspace" wording
(a workspace file would need a gitignore entry and still risk a stray
`git add -A`, defeating the requirement that the brief never reach the
branch, diff, or PR) — so it survives session context compaction. The
`scout` writes that file itself before returning (`scout-prompt.md`); the
session that dispatched it only reads the file back from disk before it
starts work, and never writes or appends to it
(`fragments/scout-delegate.md`). That fragment is deliberately
delegation-free: it renders on `SCOUT_PROVISIONED` alone, so its wording
has to stay true on a scout-present, worker-absent roster, where nothing
is ever delegated (issue #3163). When a `worker` is provisioned too,
the coordinator quotes the brief's Map entries, Invariants & gotchas, and
Suggested-approach step for that slice into the delegation verbatim — scoped
to the slice, never the whole brief (`fragments/coordinator-scout-brief.md`,
gated on `COORDINATOR_SCOUT_BRIEF`, worker && scout && the default dispatch
kind) — and the worker's own prompt directs it to work from that quoted
excerpt first, opening the full brief at `/tmp/brief.md` only when the
excerpt is missing, wrong, or silent (`fragments/worker-scout-brief.md`), so
a delegated worker starts from the scout's map instead of re-deriving it.
The verbatim excerpt is deliberate insurance: a few KB in a worker's small
starting context is far cheaper than exploration turns late in a long
worker run, each of which replays the worker's whole accumulated context.

Issue #3216 pushes that same excerpt format one step upstream, into the
brief itself: every load-bearing Map claim — a seam, signature, invariant,
or gotcha a coordinator decision rests on — now carries a cited verbatim
excerpt, the file's own lines quoted under a `path:line` anchor, trimmed to
the decision-rich lines. `## Invariants & gotchas` cites each entry the same
way, and `## Suggested approach` cites any step that rests on a specific
signature or line (`scout-prompt.md`). The evidence the coordinator quotes
into a delegation is then the same evidence it verified the claim from,
quoted once by the scout rather than re-quoted per audience. The coordinator
verifies the scout's claims from those citations already in the brief,
reserving a tree read for spot-checking a citation that looks wrong or is
missing, never as a standing sweep
(`fragments/coordinator-scout-brief.md`); `fragments/scout-delegate.md`
carries the matching instruction to the scout itself, and narrows the
"wrong/missing pointer" that triggers a coordinator re-search to a citation
that is itself wrong or missing. The fix targets a measured cost:
in the #3183 dogfood run, the pass-1 coordinator spent 11 of its 44 calls
re-grepping ground the scout had just mapped, loading ~50K chars of tool
results into the run's longest-lived context — re-paid as a cache read on
every one of the ~35 calls that followed.

Issue #3449 moves the brief off the scout's return message and onto disk. The
scout writes the brief directly, so its cited excerpts reach `/tmp/brief.md`
byte-exact by construction rather than transiting the coordinator's context
and being retyped — a transcription slip there would silently break the
evidence chain that the coordinator's verification and every worker
delegation rest on, and nothing would catch it. The scout appends each
excerpt straight from its source file
(`sed -n '<range>p' <file> | sed 's/^/> /' >>/tmp/brief.md`) and, before
returning, verifies each citation block by block: a `path:line` anchor names
exactly the lines quoted beneath it, so re-running that anchor's own range
and `diff`ing it against the block catches a wrong range as well as altered
text, which a per-line grep of the whole file would not (`scout-prompt.md`).
The scout's return message is now a short pointer — the path, its line
count, and a line or two on what the change touches — not the brief's text,
so the coordinator no longer spends output tokens on the run's most
expensive model reproducing content it already holds. The ~60-line brief
budget is deliberately unchanged: the coordinator still reads the whole
brief, so a longer one would inflate the run's most expensive context.
Uncapping only becomes safe once the coordinator can read the brief
selectively, which is separate work.

A roster with no `scout` entry degrades gracefully rather than dangling a
reference to a brief that was never written:

- `scoutProvisioned = lib.any (e: e.name == "scout") finalRoster`
  (`lib/mkHarness.nix`) is forwarded to the Box as `BOX_SCOUT_PROVISIONED`,
  read into `Env.ScoutProvisioned` (`cmd/launcher/internal/promptassembly`).
  Unlike `WORKER_PROVISIONED`, this reads `finalRoster` directly rather than
  `agentsJsonAttrs`'s parsed keys: opencode provisions scout via
  `agentFilesTemplate`, not `agentsJsonTemplate` (which stays `""` for
  opencode regardless of roster), so an `agentsJsonAttrs`-keyed check would
  wrongly read false for an opencode box that does carry scout.
- The `# SCOUT` section's `SCOUT_PROVISIONED` gate and its paired
  `SCOUT_ABSENT` arm (`fragments/scout-absent.md`) are mutually exclusive: off,
  the implementor is told to map the repo itself, and no prompt anywhere
  references `/tmp/brief.md`. The orchestrator's run-state handoff is guarded
  by existence, not by roster: it emits its own Scout brief bullet whenever
  the recorded `ScoutBriefPath` names a file that is actually there, and omits
  it otherwise. Run-state must keep recording the configured path for a run
  that does write the brief, so the bullet's absence is evidence about the
  file, not about the roster.
- The coordinator's own brief guidance is separately gated on
  `COORDINATOR_SCOUT_BRIEF` (`WorkerProvisioned && ScoutProvisioned &&
  DispatchKind == "work"`), so a worker-only or scout-only roster never sees
  a delegation naming a brief that doesn't exist. The worker's own directive
  is gated on `WORKER_SCOUT_BRIEF` (`ScoutProvisioned && DispatchKind ==
  "work"`) with the same work-only restriction: a research dispatch never
  delegates a scout or writes a brief
  (`cmd/launcher/internal/promptassembly/gates.go`).

A long-running worker replays its whole accumulated context on every turn, so
cache-read cost grows with the run's length. `fragments/coordinator.md` has
the coordinator size each slice for a bounded run and state the turn budget
per delegation, rather than a fixed number baked into the prompt. Per
`worker-prompt.md`, a worker nearing that budget stops cleanly and returns a
remaining-work checkpoint alongside its normal report — rather than a new
on-disk path that would need its own parity guard alongside
`TestScoutBriefPathMatchesPromptProse` — and the coordinator hands the
remainder to a fresh worker seeded from it. The same replay cost is why
a worker composes a group's already-determined edits into one patch file
and applies it with a single `git apply --recount -C1 --reject`, verifying
once for the group rather than checking after every line. That invocation
lets a worker write the patch without first reading the file to get line
numbers right: `--recount` derives the hunk counts from the patch body,
`-C1` needs only one line of context to place a hunk despite drift, and
`--reject` writes anything it cannot place to a `.rej` file to repair from
instead of blocking the whole apply. The directive names only git, which
every Box bakes (`patch` is not among `lib/image.nix`'s harnessPackages),
and stays in shell and file terms so it holds the same across Drivers. Two
guards keep the batch from costing more than it saves: never re-read a
file solely to construct a patch — a full-file read is permanent prefix
inflation, charged on every remaining turn — and never group a change
whose content depends on another change in the same group.

Whether the Driver bounds a check or build command's output was the open
question behind issue #3419's check-output criterion, and the answer is
that it needs no prompt-level fix: the `agent/bash-output-tee.sh` /
`agent/bash-output-summary.sh` hook pair described in [Bash command-output
interceptor](#bash-command-output-interceptor) already tees every Bash
call's combined output to a log file and hands the model back only a
bounded tail, uniformly for every command and every role. Telling a worker
to redirect output by hand would have asked it to rebuild a guarantee the
Box already gives it, so the criterion took its brief-note arm:
`worker-prompt.md` carries a short reminder that the full log is on disk
and should be grepped for whatever the tail cut off, never read whole —
the one half of the interceptor's contract a worker still has to act on.
(`transcript_render.go`'s `resultTextMaxLen` bounds the host-side rendered
transcript line rather than what the agent replays, so it is not what makes
this safe.)

### Prompt contract build-time/runtime parity

The Box's own contract with the launcher/host — e.g. a read-only research
run's verdict must reach the launcher via a `SPINDRIFT_COMMENT` marker, an
orchestrator-on run's review pass must emit a `VERDICT:` line — is declared
once, as data, in `lib/prompt-contract.nix`'s `validateMarkers` registry
(id/marker/carrier/severity/`when`-gate/`message` per row). `severity` is
"reject" for a row whose omission is unrecoverable (both current reject rows
are narrow and condition-gated, so a miss there is unambiguous), and "warn"
for a row whose omission still has a real, exercised fallback that gets the
work recorded anyway (`pr-intent`, `issue-intent`, `research-issue-intent`) —
a deliberate, standing choice rather than an unfinished promotion to
"reject" (issue #2996). Two independent arms resolve that same registry into
a verdict:

- **Build time** (Nix): `buildTimeRejectVerdicts` folds each `severity ==
  "reject"` row into `ok`/`reject`/`advise` from whatever gate/content
  knowledge is available while `mkHarness` renders the prompt — `reject`
  fails the build outright; `advise` defers to the runtime arm below when a
  gate or its content isn't yet knowable.
- **Runtime** (bash): `agent/entrypoint.sh`'s `_validate_prompt_contract`
  re-checks the same rows against the fully-rendered prompt just before the
  Driver runs, once every gate is a resolved boolean — there's no build
  time's "unresolved" state, so it only ever blocks (`exit 1`) or doesn't.

Because these are two hand-written implementations of the same predicate,
`lib/prompt-contract.nix`'s `parityFixtures` (one record per reject-severity
row × gate × marker-present combination, each pre-resolved via the real
`buildTimeRejectVerdicts`) and `parityFold` (`verdict != "reject"`) pin the
semantic mapping between them: a Nix `reject` verdict must correspond to the
runtime validator blocking; `ok`/`advise` must both correspond to it not
blocking. `nix/checks/prompt-contract-parity.nix` asserts the fold holds in
pure Nix; `tests/prompt-contract-parity.bats` (wired as the `checks-inbox`
member `bats-prompt-contract-parity`, `nix/checks/bats.nix`) drives the same
fixtures — rendered to JSON — through the real `agent/entrypoint.sh` and
asserts its exit code agrees. A future row added to `validateMarkers` is
picked up by both without further edits.

### Prompt composition report

`driver-exec assemble-prompt`'s `--composition-output <path>` (`-` for
stdout) reports which pass kind's prompt is made of what, and how much of it
two pass kinds share — without dispatching a Box, the same standalone seam
`--prompt-output` already runs through (issue #3444). Internally it's
`promptassembly.Compose`, sharing `assemblePromptBodies` with `Assemble`
itself so the report can never drift from what the Driver actually receives.

A cell reports one `PassComposition` per pass kind it renders: an
orchestrator-on cell reports five — `implement`/`fix`/`land` sharing the
base-prompt body, `review`/`delta-review` sharing the review-prompt body —
while a non-orchestrator or warm-fix (`FIX_PASS` > 0) cell reports a single
`legacy` pass, and a research dispatch reports a single `research` pass.
Passes the orchestrator drives without a template of their own — a `settle`
pass, say — have no pass kind here and so never appear. The issue body,
unlike in a pre-#3445 report, is no longer an unattributed run-time fetch:
the launcher reads it host-side at dispatch and every reported pass's
`sources` carries it as a `var` source named `ISSUE_TEXT`.

Each pass breaks its prompt down into `sources`, one entry per distinct
source — a source referenced twice contributes one row summing both runs,
not a row per run — tagged by where its bytes came from: `template` (the
base or review-prompt template file), `fragment` (a conditional fragment the
registry gated in), `contract` (a comms/check/outcome contract file
substituted verbatim), `var` (a `${NAME}` substitution the allowlist fills
in, e.g. `ISSUE_TITLE` or `CI_FAILURE_SUMMARY`), or `carried` (a
`--composition-carried` block, below). `var` and `carried` are deliberately
separate kinds: a carried block named after a substitution variable is a
different origin from the variable itself, and collapsing the two would
merge their byte counts into one row. Each pass also reports its own `bytes`
total and a `remainder` — the gap between `bytes` and the sum of
`sources[].bytes`. It is structurally zero, since both sides are computed
from the same segments; it is a self-check on that accounting, not a
reconciliation against the dispatched prompt. What pins the report against
the real thing is `TestComposeReconcilesAgainstAssemble`, which diffs
`Compose`'s totals against `Assemble`'s own output.

The report's `diffs` array holds one `PassDiff` per unordered pair of
reported passes, partitioning each pair's sources into `shared` (the
smaller of the two sides' byte counts per source, e.g. two passes rendering
the same fragment identically), `onlyA`, and `onlyB`. Each side's partition
holds exactly: `sharedBytes` plus the sum of `onlyA[].bytes` equals
`a.bytes`, and likewise for `b`. The shared portion is a per-source byte
minimum — position-insensitive and content-blind — so it answers "how much
of these two prompts comes from the same places", not "how long a common
prefix would a prompt cache hit on". Two passes sharing every source but
ordering them differently still report the same `sharedBytes`; measuring
cache-prefix overlap would need a prefix-level comparison this report does
not attempt.

`--composition-carried [<pass>:]<name>=<path>` (repeatable) feeds text a
later stage prepends to the pass prompt at pass time — the orchestrator's
own run-state handoff, e.g. reviewer findings or the decisions record —
into the report as a `carried` source, since `Assemble` itself never sees
those bytes and so can't count them. Omitting the `<pass>:` prefix carries
the block into every reported pass. A block is never silently dropped: an
unreadable file, a value missing `=`, an empty `<name>` or `<pass>`, or a
`<pass>` naming a kind this cell does not render all fail the whole
invocation (exit 1), the last of them listing the pass kinds the cell does
render. A `:` or `=` inside `<path>` is safe — the pass prefix is whatever
precedes the first `:` that itself precedes the first `=`.
`--composition-carried`'s value is validated at flag-parse time regardless;
only the file read and the `<pass>`-name check are deferred to a run that
sets `--composition-output`.

```sh
driver-exec assemble-prompt \
  --prompts-dir ./prompts \
  --registry registry.json --validate-markers-registry validate-markers.json \
  --prompt-output /dev/null --agents-json-output /dev/null --handoff-output /dev/null \
  --composition-output -
```

### Hermetic git config

The entrypoint sets `GIT_USER_NAME`/`GIT_USER_EMAIL` as **repo-local** git
config on the cloned workspace (`git config user.name`, no `--global`), not
global config. CI's hermetic `nix flake check` sandbox has no global git
config, so keeping the Box's global surface empty too means a test that
shells out to git behaves the same in both places — no environment-sensitive
test can pass in the Box and fail in CI (or vice versa) because of an ambient
global git setting the Box has and CI lacks.

### Label lifecycle

`spindrift dispatch` uses labels on the Target repo as the dispatch state of each
issue, which is what makes re-running it safe. It queries only `LABEL`
(`ready-for-agent`), so the labels below are what keep an issue from being picked
up twice:

```
ready-for-agent ──dispatch──▶ agent-in-progress ───landing settles───▶ agent-complete
   (launch button)              (a Box is running,                     (agent done; CI was
                                 CI is polling, or the                  green and MERGE_MODE
                                 landing path — rebase-retry,           has run its course:
                                 conflict-resolve, post-force-          merged, auto-merge
                                 push-wait — is still running)          enqueued, or handed off)
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
- **Green is labelled only once the landing path settles; merge is a separate
  policy.** When CI confirms green the launcher applies `MERGE_MODE` first:
  `immediate` rebase-merges the PR (retrying rebase conflicts, dispatching an
  agent conflict-resolve box, and re-waiting for CI on the new head as needed
  — the issue stays `agent-in-progress` throughout), then verifies it really is
  merged; `auto` enqueues GitHub's native auto-merge; `manual` (the default)
  leaves the green PR open for a human. Only once that settles does the merge
  gate swap `agent-in-progress` → `agent-complete` — the agent's work is done.
  `Closes #N` in the PR body closes the issue whenever the PR merges.
  (Dependency ordering keys off the PR actually being merged, not this label —
  see [`Readiness.Ready`](../cmd/launcher/internal/waves/blocker.go).)
- **Red CI self-heals before it fails.** If CI goes genuinely red, the launcher
  dispatches up to `MAX_FIX_ATTEMPTS` fix boxes on the same branch and re-gates
  after each. Only once those are exhausted (or a fix box exits non-zero
  after transient retries) does it swap to `agent-failed` and stop. There are
  **no automatic re-dispatches from `ready-for-agent`**: a human inspects
  `.spindrift/logs/issue-<n>.log` and re-labels to retry. A fix box's transient
  exits get the same in-session retry as the initial run — a 429 mid-fix-pass
  holds until the reset time and re-dispatches rather than burning one of the
  `MAX_FIX_ATTEMPTS` slots.
- **The fix box gets the concrete failure, not a guess.** At the moment
  genuine-red is declared, `selfHeal` fetches the failed check names plus a
  bounded log excerpt for the PR's head commit and forwards it into the fix
  box as `CI_FAILURE_SUMMARY`. The fetch is best-effort — a fetch failure
  never blocks the fix pass, it just falls back to the fix box rediscovering
  the failure itself (`gh run view --log-failed`, the pre-#426 behavior).
- **Stranded issues are recovered explicitly, never adopted automatically.** A
  bare `agent-in-progress` label carries no liveness signal — it cannot tell an
  issue a crashed launcher stranded apart from one a live runner (another Box,
  or an overlapping local run) is actively committing to right now. `spindrift
  dispatch` never adopts on the strength of the label alone. The unstick is the
  `agent-recover` label (`agent-recover.yml` → `spindrift recover <n>`): an
  operator's explicit assertion that the issue is no longer owned by a live
  runner, re-running the merge gate on its open PR (draft or not).
- **A terminal recover failure never downgrades an already-successful issue.**
  The claim above strips whatever terminal label the issue carried (including
  `agent-complete`) before `spindrift recover` ever runs, so a recover attempt
  that then fails to find anything to adopt — leaving no open PR and no
  recoverable self-report — reads the issue's timeline for the terminal label
  that claim just removed. When that was `agent-complete`, recover restores
  it and comments that a recover was attempted and declined to change
  anything, rather than letting the workflow's unconditional park step demote
  the issue to `agent-failed` (issue #2477). An issue with no prior terminal
  label, or whose prior label was `agent-failed`, still parks `agent-failed`
  exactly as before.

Rename any of these with the `inProgressLabel` / `failedLabel` / `completeLabel`
knobs under `issues.labels` (baked) or the
`IN_PROGRESS_LABEL` / `FAILED_LABEL` / `COMPLETE_LABEL` env vars (runtime).

#### Issue Tracker backends

Per [ADR 0013](adr/0013-issue-tracker-and-code-forge-are-independent-seams.md),
the Issue Tracker (where issues live) and the Code Forge (where code and CI
live) are independent axes. `ISSUE_TRACKER` selects the tracker; the Code
Forge stays `github` regardless (Jira issues, GitHub PRs).

The [Quickstart wizard](../README.md#quick-start) always provisions
`github`; it never prompts for a tracker. `local`, `jira`, and `forgejo` are
experimental and reachable only by hand-editing `ISSUE_TRACKER` in the
generated `flake.nix`.

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
  `Blocks` link type's "is blocked by" direction) rather than prose parsing.
  The `github` tracker resolves the same way in principle — native
  issue-dependencies relationships first — but falls back to its
  `## Blocked by` / `depends on #N` body conventions when the native lookup
  is empty or unavailable (older GHES, missing token scope, API error); Jira
  has no such fallback.

  Every operator-facing blocker rendering — `preview`'s blocker annotations,
  a selective dispatch's blocked-skip notices, and the blocked-claim marker
  (and the release comment posted from it) — tags each ref with the source
  it resolved from: `(native)` for a tracker's native relationship, `(body)`
  for a body-text ref. `jira` refs always render `(native)`; `local` refs
  (no native concept) always render `(body)`; `github` renders whichever the
  precedence above actually used for that issue, so drift between a stale
  body section and changed native links is visible instead of silent.

  By default the agent's prompt input is the issue's summary and description
  only; set `JIRA_INCLUDE_COMMENTS` (non-empty) to also append the comment
  thread — opt-in, to keep the prompt-injection surface tight.

  Config: `JIRA_BASE_URL` (site base URL), `JIRA_PROJECT_KEY`, and
  `JIRA_STATUS_MAPPING` / `JIRA_INCLUDE_COMMENTS` are non-secret, set via
  `issues.jira`
  (baked) or their env vars (runtime) — see the [flake options
  reference](flake-options.md). `JIRA_TOKEN` is a secret env var alongside
  `GH_TOKEN`: a Jira API token used alone as a Bearer PAT (Server/Data
  Center), or paired with the non-secret `JIRA_EMAIL` for Basic auth (Cloud).
  `spindrift doctor`'s `Probe()` check validates Jira auth and reachability
  independently of the GitHub Code Forge probe.

  A [research dispatch](#research-dispatch)'s verdict terminals
  (`recommend` / `reject` / `unclear`) always ride this same label-fallback
  mechanism — they swap the `agent-research-*` labels, never a Jira workflow
  status. `JIRA_STATUS_MAPPING` has no research-state keys, and none are
  planned: jira-native workflow-status mapping for research states is
  deferred until a Jira user exists (ADR 0022). The `local` tracker maps
  research states the same way it maps work states, through its frontmatter
  `state` field.

- **`forgejo`** — a Forgejo/Gitea REST API adapter; Codeberg is the default
  instance, set via `FORGEJO_BASE_URL` (default `https://codeberg.org`, so a
  self-hosted Forgejo/Gitea instance just re-points it). `FORGEJO_TOKEN` is a
  secret env var alongside `GH_TOKEN`, a Forgejo/Gitea API token used with
  the Bearer/`token` auth scheme.

  Dispatch state uses the same label lifecycle as `github` (`ready-for-agent`
  / `agent-in-progress` / `agent-complete` / `agent-failed`, same
  `LABEL`/`IN_PROGRESS_LABEL`/`FAILED_LABEL`/`COMPLETE_LABEL` knobs) — Forgejo
  has no native workflow-status concept to prefer over labels, unlike Jira's
  status mapping.

  Dependencies resolve from Forgejo's native issue-dependencies API first,
  falling back to the `## Blocked by` / `depends on #N` body-text convention
  when the native lookup is empty or unavailable — the same precedence and
  `(native)` / `(body)` source tagging as the `github` tracker.

  Config: `FORGEJO_BASE_URL` is non-secret, set via `issues.forgejo.baseURL`
  (baked) or its env var (runtime) — see the [flake options
  reference](flake-options.md). `spindrift doctor`'s `Probe()` check
  validates the token and instance reachability independently of the GitHub
  Code Forge probe.

#### In-Box forgejo tooling (`fj`)

When either backend knob selects forgejo — `ISSUE_TRACKER=forgejo` or
`CODE_FORGE=forgejo` — the image bakes [`fj`](https://codeberg.org/Cyborus/forgejo-cli)
(forgejo-cli) onto the Box's PATH; a github-backend image never carries it.
This is the forgejo analog of the `gh` load out: in the default read-write
mode an Agent reads its issue, comments, posts research verdicts, and opens
the PR through `fj` porcelain rather than hand-assembled REST calls.

The entrypoint configures `fj` non-interactively at clone time:
`configure_forgejo_cli` feeds `FORGEJO_TOKEN` on stdin (never argv) to
`fj auth add-key`, which writes `~/.local/share/forgejo-cli/keys.json` keyed
by the instance host — an offline write, no network call. It is inert on a
non-forgejo image (no `fj`) or a read-only Box (no token), so the token
plumbing follows exactly where `fj` is baked and permitted to write. The
same `FORGEJO_TOKEN` also authenticates git pushes, carried as the origin
remote URL's userinfo.

The `*-forgejo` prompt-fragment family names the commands the Agent runs,
pinned by the prompt eval-checks the same way the `gh` family is:

- **Read** (`fj issue view <other-number>`) — for a parent/linked issue or
  PRD the subject issue references; the subject issue's own body and its
  last-10-comment snapshot no longer need an in-box read at all, since the
  launcher reads them host-side at dispatch and injects them into the
  prompt's `# ISSUE TEXT` section (issue #3445), same as every other
  tracker.
- **Comment / verdict** (`fj issue comment ${ISSUE_NUMBER} "..."`) — the
  read-write blocked-note and research-verdict steps; a read-only Box relays
  both host-side (a nonce-guarded `SPINDRIFT_COMMENT` line and the
  `SPINDRIFT_OUTCOME` `note=` field) exactly as the local and read-only
  github trackers do.
- **Open PR** (`fj pr create --base … --head … --title "WIP: …"`) — the
  read-write `CODE_FORGE=forgejo` create step; the `WIP: ` prefix opens a
  draft the launcher flips ready on CI green before it merges (the forgejo
  PRForge surface, [ADR 0038](../docs/adr/0038-the-forgejo-backend-decision-set.md)).

For the REST surface `fj` lacks — attaching a label at issue-create time —
the filer fragments (`filer-file-direct-forgejo.md`,
`filer-label-direct-forgejo.md`) fall back to a `curl` call against the
Forgejo REST API directly rather than an `fj` porcelain command.

#### Forgejo integration harness

The httptest contract suite (`TestForgejoClient_TrackerContract`,
`TestForgejoCodeForge_PRForgeContract`) pins the adapter's behaviour against a
fake that mirrors the adapter's own expectations back at it — fast, hermetic,
and part of the default gate, but structurally unable to catch the adapter
drifting from Forgejo's *real* wire format, because the fake drifts along with
it. The **integration harness** closes that gap: it boots a throwaway Forgejo
from its published OCI image, seeds a repo, an issue, and both label families,
and drives one real dispatch loop — claim, work, PR, merge, complete — entirely
through the real IssueTracker and CodeForge/PRForge adapters, asserting the
canonical lifecycle at each step. A deliberate API-shape regression in the
adapter (a renamed JSON tag, a changed REST path) fails the harness where the
contract suite stays green.

It is **opt-in and excluded from the default check gate**: it lives behind the
`integration` build tag, so `nix build .#checks-inbox`, `nix flake check`, and a
plain `go test ./...` never compile it, and it self-skips wherever no container
daemon is reachable (including this repo's own dogfood Box, which has none).
Run it explicitly, **pre-release or on demand**, on a host with `podman` or
`docker` and network egress to pull the image:

```sh
go test -tags integration -run TestForgejoIntegration ./cmd/launcher/internal/forge/forgejo/
```

One command boots, seeds, runs the loop, asserts, and tears the container down
— no external service and no credential beyond the throwaway admin account the
test itself creates inside the disposable, localhost-only instance. The image
defaults to `codeberg.org/forgejo/forgejo:11`; set `SPINDRIFT_FORGEJO_IMAGE` to
pin a different tag or a local mirror.

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
.github/**,.forgejo/**,**/CLAUDE.md,**/AGENTS.md,.claude/**,.opencode/**
```

— CI config for both forges plus the instruction surface (`CLAUDE.md`, `AGENTS.md`,
`.claude/`, `.opencode/`). Those files are a cross-run persistence vector: a
poisoned instruction file merged once feeds every future Agent as trusted
input on its next fresh clone, so the default set is deliberately broad.
Setting `MERGE_GUARD_PATHS=""` disables the guard entirely — an explicit
opt-out; the operator owns the consequences.

The changed-path list is read **host-side**, the same way the merge gate
reads CI state — never from anything the Box produced — so an injected
Agent following its normal flow cannot make the guard see a clean diff. It
does not, however, defend against a fully adversarial Agent. Under the
`github` Code Forge, [the launcher — never the Agent — owns the merge
decision](#how-a-run-works): the Agent must not run `gh pr merge`. What
follows describes what breaks that contract, not what a cooperative Agent
does — the GitHub token that opens the PR is the same token that can merge
it, so an Agent that defies the contract and runs `gh pr merge` against its
own PR bypasses the launcher-side check entirely. See
[ADR 0016](adr/0016-merge-guard-bounds-drift-not-adversaries.md) for that
boundary and the two-actor-separation hard mode.

The guard exists on the `github` and `forgejo` Code Forge merge paths (both
implement `PRForge`). The push-only `git` forge has no launcher in the merge
path and therefore no guard at all.

Configure it via `git.merge.guardPaths` (baked) or the
`MERGE_GUARD_PATHS` env var (runtime) — see the [flake options
reference](flake-options.md) for the full knob surface.

#### Two-actor separation (opt-in hard mode)

Under the single-token default, the GitHub token that opens a PR is the same
token that can merge it — the Box holds it, so a fully adversarial Agent can
run `gh pr merge` on its own green PR from inside the Box before the launcher
ever evaluates the merge guard (see [ADR
0016](adr/0016-merge-guard-bounds-drift-not-adversaries.md)). Two-actor
separation is the opt-in hard mode that closes that gap: two machine GitHub
users instead of one, with a repository ruleset barring the Box's user from
updating the base branch. It is the only configuration in which the merge
guard is literally uninfluenceable by the Agent, at the cost of a second
account and a second secret to provision.

**Setup:**

1. Create a **second GitHub user** (or bot account) distinct from the one
   behind your existing `GH_TOKEN`. Mint it a fine-grained PAT scoped to
   **only the Target repository**, with the same permission set the
   single-token default already grants (see the permission table above).
2. Set that PAT as `BOX_GH_TOKEN` — a secret, like `GH_TOKEN` itself: `harness.env`
   or your shell, never a flake setting. Unset, the harness behaves exactly as
   the single-token default, byte-for-byte; set, the Box receives this value
   as its own `GH_TOKEN` while the launcher keeps using its own for every
   host-side call (merges, labels, the usage comment).
3. On the Target repo, create a **repository ruleset** (Settings → Rules →
   Rulesets) targeting the base branch, with a "Restrict updates" rule (add
   "Restrict deletions" too, to stop the Box's user from deleting the branch
   outright).
4. Set the ruleset's **bypass list to the launcher's own user only** — the
   one behind `GH_TOKEN`. Every other actor, including the Box's user, is
   then blocked from updating the base branch by the ruleset itself,
   whether by a direct push or by merging a PR — GitHub enforces "restrict
   updates" on every ref update regardless of the token's own permission
   scope, which is why this closes the gap the merge guard cannot: no PAT
   permission table controls it, the ruleset does.

**Per-token permissions** — both tokens carry the same fine-grained scopes;
what differs is which GitHub user each belongs to, and only one is on the
bypass list:

| token                | GitHub user            | can update the base branch? |
| -------------------- | ----------------------- | ---------------------------- |
| `BOX_GH_TOKEN` (Box)  | second, non-bypassed user | No — the ruleset blocks it, even via PR merge |
| `GH_TOKEN` (launcher) | primary, bypass-listed user | Yes — the ruleset's sole bypass actor |

See [SECURITY.md](../SECURITY.md) for how this changes the "Box cannot merge
its own PR" claim.

#### Two-actor separation on Forgejo

The Forgejo backend earns the same opt-in hard mode, mirroring the GitHub
recipe above with `BOX_FORGEJO_TOKEN` in place of `BOX_GH_TOKEN`. Under the
single-token default, the `FORGEJO_TOKEN` that opens a PR is the same token
that can merge it, so a fully adversarial Agent could merge its own green PR
from inside the Box before the launcher evaluates the merge guard. Two
machine Forgejo users instead of one, with a branch-protection rule barring
the Box's user from updating the base branch, closes that gap. Forgejo has no
App-token model, so this is the Forgejo analog of GitHub's ruleset: a plain
branch-protection rule whose push and merge whitelists name only the
launcher's user.

**Setup:**

1. Create a **second Forgejo user** (or bot account) distinct from the one
   behind your existing `FORGEJO_TOKEN`. Mint it a PAT scoped to **only the
   Target repository**, with the same permission set the single-token default
   already grants.
2. Set that PAT as `BOX_FORGEJO_TOKEN` — a secret, like `FORGEJO_TOKEN`
   itself: `harness.env` or your shell, never a flake setting. Unset, the
   harness behaves exactly as the single-token default, byte-for-byte; set,
   the Box receives this value as its own `FORGEJO_TOKEN` while the launcher
   keeps using its own for every host-side call (merges, labels, the usage
   comment).
3. On the Target repo, add a **branch-protection rule** (Settings → Branches →
   Branch protection rules → Add new rule) whose branch-name pattern matches
   the base branch (e.g. `main`). Enable it so direct pushes are blocked
   unless whitelisted, and turn on **Enable Merge Whitelist** so unwhitelisted
   users cannot merge PRs into the branch either.
4. Set both the **push whitelist and the merge whitelist to the launcher's own
   user only** — the one behind `FORGEJO_TOKEN`. Every other actor, including
   the Box's user, is then blocked from updating the base branch by the rule
   itself, whether by a direct push or by merging a PR. No PAT permission
   scope controls this — the branch-protection rule does, which is why it
   closes the gap the merge guard cannot.

**Per-token permissions** — both tokens carry the same scopes; what differs is
which Forgejo user each belongs to, and only one is whitelisted:

| token                      | Forgejo user               | can update the base branch? |
| -------------------------- | -------------------------- | ---------------------------- |
| `BOX_FORGEJO_TOKEN` (Box)  | second, non-whitelisted user | No — the rule blocks it, even via PR merge |
| `FORGEJO_TOKEN` (launcher) | primary, whitelisted user  | Yes — the rule's sole whitelisted actor |

Unlike GitHub, **Forgejo exposes no endpoint to introspect a token's granted
scopes**, so the launcher cannot verify `BOX_FORGEJO_TOKEN`'s write capability
at startup the way `BOX_GH_TOKEN` is checked under `read-only`. The
branch-protection rule is the enforcement boundary regardless; provision the
Box user's PAT with least privilege and verify its scope yourself before
relying on it.

#### Forgejo Actions dispatch templates

`.forgejo/workflows/agent-dispatch.yml`, `agent-recover.yml`,
`agent-research.yml`, and `agent-research-close.yml` are the Forgejo Actions
mirror of the
`.github/workflows/` control plane described in the [Label
lifecycle](#label-lifecycle) section and the [GitHub App installation
token](#github-app-installation-token-recommended) / [Research
token](#research-token-least-privilege-optional) sections above — same
`issues: labeled` trigger, same label vocabulary (`agent-trigger` fires
dispatch, `agent-recover` fires recover, `agent-research` fires research,
`agent-research-reject` closes the rejected issue; the same lifecycle and
research label families apply unchanged). What
differs is authentication: Forgejo has no GitHub App model, so there is no
worker-App mint and no `gh-token-refresher` on this backend ([ADR
0038](adr/0038-the-forgejo-backend-decision-set.md)) — each workflow
authenticates with a single, non-expiring `FORGEJO_TOKEN` **repository
secret**, read directly rather than minted per run. A job-level `env:`
block selects the forgejo backend on both axes (`ISSUE_TRACKER: forgejo`,
`CODE_FORGE: forgejo`), and sets `FORGEJO_BASE_URL` (falls back to
`https://codeberg.org` via `vars.FORGEJO_BASE_URL || 'https://codeberg.org'`
when the repo variable is unset) and `FORGEJO_TOKEN` from the secret.

**Setup steps:**

1. **Enable Actions on the repository.** Codeberg (and most Forgejo
   instances) ship Actions disabled by default — opt in under Settings →
   Actions before any label can fire a run.
2. **Register a self-hosted runner** labelled `self-hosted` (Settings →
   Actions → Runners). Codeberg's shared runners cannot build the agent
   image — it needs Nix and the build is heavy/long — so a self-hosted
   runner is required; provision it with Nix build capability plus `curl`
   and `jq` on `PATH` (the up-front claim, the blocked-release, and the
   park-on-failure steps all talk to the Forgejo REST API through the
   `forgejo-label-swap` composite, and the research-reject close through
   `forgejo-issue-close`, both of which shell `curl`/`jq`, because `fj` has
   no label or issue verb and is not guaranteed on the runner). If your runner
   carries a different label, edit each workflow's `runs-on: self-hosted`
   to match.
3. **Create the `FORGEJO_TOKEN` repository secret** (Actions secret) — the
   token the workflows read directly, scoped the same as the Forgejo
   backend token documented above (Contents/Pull requests/Issues/Metadata
   RW). Also create the `CLAUDE_CODE_OAUTH_TOKEN` secret, and the
   `AGENT_GIT_USER_NAME` / `AGENT_GIT_USER_EMAIL` repository variables
   (`vars.*`, optionally `FORGEJO_BASE_URL` too) — same secrets/variables
   the `agent-setup` composite action (`./.github/actions/agent-setup`,
   which Forgejo resolves from the checked-out tree the same as GitHub)
   consumes on the GitHub set. The forgejo workflows call it with
   `forge: forgejo`, which skips agent-setup's GitHub-only rate-limit smoke
   (`gh api rate_limit`) and `gh issue edit` claim — both target
   api.github.com, which a Forgejo token cannot authenticate against — and
   claim the issue through `forgejo-label-swap` before the build instead.
4. **Ensure the label families exist** on the repo: the four triage labels
   for dispatch/recover, and — if using research — the `agent-research*`
   family; see [Create the research
   labels](#create-the-research-labels-on-the-target-repo) and `spindrift
   doctor`.

Because Forgejo PATs don't expire, a long run never needs to re-mint: unlike
the GitHub set's App-token machinery, there is no mint step and no
background refresher here — the single `FORGEJO_TOKEN` secret is read once
at job start and used for the whole run.

`.forgejo/**` is already part of the default `MERGE_GUARD_PATHS` glob (see
[Merge guard](#merge-guard) above), so a PR that edits these templates
matches it the same way an edit to `.github/workflows/**` does.

#### Stale-base preflight

A green PR can still be **behind** its base: main may have advanced past a
just-merged sibling whose changes the PR's tested tree never saw, even
though the PR itself carries no textual conflict. GitHub's mergeability check
(`Mergeable`/`ErrMergeConflict`, driving the existing rebase-retry above)
only catches content conflicts, so two individually-green, non-conflicting
PRs can still combine into a broken tree once both land — exactly what
happened when #670 and #672 merged ~90 seconds apart: `f8d9e9b` deleted a
symbol `a463411`'s concurrently-merged tests still referenced, and no check
ever compiled the two together before `launcher-go-vet` failed on `main`
(issue #936).

This preflight is **opt-in via `PREFLIGHT_STALE_BASE`, off by default** (ADR
0028). By default a green-but-behind PR merges as-is — the launcher does not
rebase it, and does not even run the `behind_by` check — trading the rare
cross-PR semantic break above for the throughput of parallel landings that
never pay an extra rebase + CI cycle. The freshness burden instead sits with
the implementor Box, which rebases onto the latest base immediately before
every push. Turn the knob on for deployments that prefer to pay the tax.

> **⚠️ With the preflight on, expect thrashing under high parallelism.** Every
> landing advances `main`, which leaves the other in-flight PRs behind, so each
> one that reaches green gets rebased and re-runs CI — and may be behind *again*
> by the time that CI finishes, triggering yet another rebase. Under enough
> concurrent landings this degrades into near-constant rebase + re-CI churn
> that burns CI minutes and tokens and starves throughput. The durable fix is a
> **merge queue** (GitHub's, or an external one), which serializes and batches
> the "test against the current tip, then land" step so each combined tree is
> built once instead of repeatedly. This is the main reason the preflight is
> off by default: if you run many issues in parallel and cannot put a merge
> queue in front of the branch, leave it off and rely on the worker's pre-push
> rebase, accepting the residual cross-PR-break risk.

When `PREFLIGHT_STALE_BASE` is set, then under `MERGE_MODE=immediate`, before
the first `Merge` attempt, the launcher compares the PR's branch against its
base via GitHub's REST compare API (`behind_by`) — a plain git-ancestry count
between two refs, not GitHub's GraphQL `mergeStateStatus` field, which only
reports `BEHIND` when branch protection requires branches to be up to date
before merging (a setting this project's fine-grained PAT cannot even read, let
alone rely on being enabled). A `behind_by > 0` hit **proactively rebases the
branch and re-waits for CI to confirm green on the rebased tree** (its own
attempt budget off `MAX_REBASE_ATTEMPTS`, independent of the reactive
conflict-retry loop's counters) before merging — so a PR whose rebase no longer
compiles against a freshly-merged sibling is blocked at that point instead of
landing on the strength of its stale green result. A query error is logged and
swallowed rather than blocking the merge; the ordinary `Merge` call surfaces
any real problem through its already-tested error handling.

This is a launcher-side sanity check, not an adversary-proof gate, and it
exists on the `github` and `forgejo` Code Forge merge paths for the same reason
the merge guard does — see [ADR
0026](adr/0026-preflight-stale-base-before-merge.md) for the root-cause
writeup and the trade-off against gating GitHub branch protection or
downgrading every stale PR to manual, and [ADR
0028](adr/0028-stale-base-preflight-is-opt-in.md) for why it is now opt-in.

#### Filer

An opt-in subagent, alongside the scout and reviewer, that turns the
non-blocking findings a review surfaces into tracked issues — but only the
ones the work loop escalated for a human, not the whole Non-blocking section.
Off by default; the current, non-deprecated way to opt in sets
`perSystem.spindrift.agents.models.roster` (see [`roster`](#subagent-roster))
to `lib/roster.nix`'s `defaultRoster` with `models.filer` set, e.g. in a
Consumer flake:

```nix
perSystem = { inputs, lib, ... }: {
  spindrift.agents.models.roster =
    (import "${inputs.spindrift}/lib/roster.nix" { inherit lib; }).defaultRoster {
      models.filer = "your-model-id";
    };
};
```

Setting `models.filer` opts the filer in while leaving scout/reviewer/worker
at their defaults. Setting `roster` itself to a hand-authored list instead of
going through `defaultRoster` replaces the whole default roster, not just the
filer entry, so a literal `roster = [ { name = "filer"; ... } ]` silently
drops scout/reviewer/worker too; reach for that only when authoring a full
custom roster. Setting `FILER_MODEL` (empty by default; recommended: the same
model as the `scout` default, see [Default models](#default-models)) is the
older, **deprecated** opt-in — it still works but is superseded by `roster`.
Either way, when neither is set, that's zero behavior change and zero prompt
residue in the rendered issue prompt.

The work loop triages Non-blocking findings before the filer ever
runs. It fixes inline, in the same effort and regardless of round, every
finding whose fix is cheap and in scope for the issue's own acceptance
criteria and the slice as originally authored — nits, smells, dead
code, misleading names, and doc updates for a surface the *original*
slice touches. Lines this branch itself touched in an earlier round's
own absorbed fix count as that surface: they are this branch's own work,
so a finding about them is in scope at every round. What stays out of
scope is surface this branch never touched at all — that pin, not the
round number, is what bounds where the in-scope surface can grow: it
widens with this branch's own fixes, never onto code this branch never
wrote, so round 2 does not simultaneously file more and fix less. It
drops, without filing, a finding that is correct but trivial and out
of scope for the slice, where the floor is worth rather than certainty
(issue #3610), and escalates what genuinely needs a human — a design
trade-off, out-of-scope work, or a change too large to fold in. Scope
decides which outcomes are available at all: on a surface the branch
never touched there is no inline fix to weigh, so a finding it is
merely unsure about is filed rather than fixed, at every round
(issue #3816). Only the tiebreak for an *ambiguous* finding inside the
surface the branch already touched is round-aware (issue #2701): on the
first review round it still fixes inline; from the second review round on
it escalates only when the fix would widen the diff — new behaviour,
a new surface, or an edit large enough to need its own review — and
keeps fixing inline an ambiguous finding whose fix is small and stays
inside what the branch already touches (issue #3611). Deferring is the
safer failure mode where a silently widened diff is the alternative,
and only there; elsewhere filing costs more than the nit does. This
keeps the filer from turning every nit into churn: one issue closed
should not spawn five more. Missing or inadequate tests for new logic are
Blocking, not filer fodder — they are fixed in the current work, never
deferred to an issue. The one exemption is a pure relocation, refactor,
or comment/doc change whose behaviour is already covered under test:
that is Non-blocking (issue #2696), and it goes through the same triage
as any other Non-blocking finding above — it needs neither an inline
fix nor a human, so it is dropped rather than escalated to the filer.

The round-2 tiebreak's narrowing (issue #3611) was calibrated against
measurement, not intuition; the before/after record, and the re-check
that closes the loop once runs have dispatched under it, live in
[issue #3611's round-2 escalation flip
recalibration](measurements/3611-round-2-escalation-flip.md).

When enabled, after the final `APPROVE` verdict and before opening the PR,
the main agent delegates only those escalated findings to the filer. The
filer:

- ensures the `agent-review-finding` label exists on the Target repo
  (idempotent — it creates the label itself; this label is separate from the
  four triage labels `spindrift doctor` manages and is not required for
  dispatch to work);
- searches **all open issues, regardless of label** and skips findings that
  already match — an open issue means the problem is already tracked,
  whether human-filed, `ready-for-agent`, filed via `/to-tickets`, or from a
  prior Filer run;
- additionally treats **closed** issues carrying `agent-review-finding` or
  `agent-research-reject` as suppressing matches — a closed finding is a
  human triage decision (won't-fix, duplicate, already fixed), and a closed
  research rejection is the same class of deliberate dismissal
  (false-positive, not-worth-doing, duplicate) — and is closed for you, by
  [`agent-research-close.yml`](#closing-a-rejected-research-issue), the moment
  the verdict lands; neither is ever refiled. A
  plain closed issue carrying neither label does **not** suppress filing — a
  problem that was fixed and later regressed can still be refiled;
- files one issue per surviving finding (merging only findings that are the
  same change), each with a conventional title, the finding's file:line refs
  and reviewing rationale, a provenance line, and an acceptance-criteria
  checklist. The branch form, `Found by review during #<issue> (branch
  <name>)`, is the primary case — filing happens before the PR opens, and
  `CODE_FORGE=git` / `CODE_FORGE=local` runs never open one at all. The PR
  form, `Found by review during #<issue> (PR <url>)`, is used only when the
  delegation actually supplied a PR URL — the filer never synthesizes one.

Filed issues carry `agent-review-finding` and **never** the dispatch label
(`LABEL` / `ready-for-agent`) — a human promotes them, the same launch-button
rule that gates every other issue. The PR body then lists the filed issue
URLs instead of the raw findings.

Filing is strictly best-effort: a filer failure or timeout never blocks the
PR or changes the outcome line — the main agent falls back to pasting the
escalated findings into the PR body, exactly as when the filer is off.

Under [`BOX_FORGE_AND_ISSUE_ACCESS=read-only`](#read-only-box-box_forge_and_issue_accessread-only)
with `ORCHESTRATOR_ENABLED` set (issue #2019), the filer's write mechanism
swaps: instead of `gh label create`/`gh issue create` (both writes a
read-only token can't make), it prints one nonce-guarded, base64-encoded
`SPINDRIFT_ISSUE_INTENT` stdout line per issue to file, and reports `QUEUED`
rather than `FILED <url>` — the issue isn't filed until the Launcher relays
it, host-side, once the Box exits, so no URL is known yet. The Launcher
re-derives the destination repo (implicit in its own tracker instance) and
always applies the fixed `agent-review-finding` label itself, never the
payload's own `labels` field (the same do-not-trust-the-agent-target
invariant issue #1949 established for the PR-intent/comment-intent
channels). Everything upstream of the write mechanism — the Filer's own
site-key search, conventional-commit titling, merge-vs-split judgment — is
unchanged; only how the filed issue actually reaches GitHub differs, plus one
added host-side backstop described below (`dedupTerms`, issue #3609): before
filing, the Launcher re-checks each intent against the open backlog itself
and skips a repeat that survived the Filer's own search. Every other
combination (`read-write` regardless of `ORCHESTRATOR_ENABLED`, or
`read-only` with the orchestrator off) keeps the direct `gh issue create`
path above, unchanged.

The relayed payload's JSON may also carry an optional `type` key, one of the
closed set `bug` | `enhancement` | `chore` (issue #2594, ADR 0041) — the
Filer names a *type*, never a label; the Launcher owns the type→label
mapping and ensure-creates the mapped label best-effort before applying it
alongside whichever provenance label the caller supplies
(`agent-review-finding` on the work path, `agent-research-finding` on the
research path).
Omitting `type`, or naming anything outside the closed set, still files the
issue — just untyped, never rejected — and a label ensure-creation failure
is itself non-fatal, the same best-effort guarantee as the rest of this
channel. Extending the enum is a host-side-only change; the Box can never
smuggle an arbitrary label through this field.

The relayed payload may also carry an optional `dedupTerms` array — one or
more site keys (issue #3609). Unlike `labels`, which stays host-derived,
`dedupTerms` is Box-supplied, the same as `type`. A term is normalized
(trimmed, internal whitespace collapsed, lowercased) and dropped if it's
empty, contains `,` (the marker line's own field separator), or contains
`--` (which would close the `<!-- spindrift-dedup: ... -->` HTML comment
early) — an intent carrying no usable term after that filter has an empty
key set and never matches, and the run warns on stderr that the intent files
without dedup. Before filing, the Launcher builds a dedup index once per run
— and only once some intent actually carries a usable key, since a payload
with none can match nothing — from the open backlog: a tracker that
implements the optional `LabeledBacklogLister` capability (GitHub
does) is asked for open issues labeled `agent-review-finding` or
`agent-research-finding` directly, with bodies and newest-first, up to
a larger scan limit than plain `ListOpenIssues()` allows; a tracker
without the capability falls back to `ListOpenIssues()` itself. Each
backlog issue's keys are only the terms recorded in its hidden
`<!-- spindrift-dedup: ... -->` marker line — written by the Launcher itself
at filing time — never its title: two distinct findings can share a
formulaic conventional-commit title, and keying on prose would merge
them. The Launcher appends that marker line to every issue it files,
empty (`<!-- spindrift-dedup:  -->`, which reads back as no keys) when
the intent carried no usable term: the last marker line in a body wins,
so always writing its own is what stops a finding that quotes the marker
format in prose from speaking for the issue's key set. Each intent's keys
are likewise only its own normalized `dedupTerms`, never its title.

Dedup is per site, not per intent (issue #3808): a multi-site finding
carries one key per site, so a hit on one of them proves only that site
is tracked. The Launcher therefore skips an intent — it is never filed —
only when every one of its keys is already covered, and prints one line
on stdout naming every existing issue that covers them — more than one
when the finding's sites are spread across separate backlog issues. An
intent only some of whose keys are covered still files, since the
uncovered site is tracked nowhere else; that partial overlap prints its
own line instead, naming the already-tracked keys and every issue
covering them, and counts as `ok` in the tally below rather than
`skipped`, since it did reach the tracker. The issue it files carries
the already-covered keys in its own marker too, so that site is briefly
named by two open issues and the index's last write wins; both are
valid "already tracked" answers, and the next run sees full coverage
and skips, so this converges rather than refiling forever.
The index also grows as the run files its own intents, so two intents in
the same payload that collide dedup against each other too, not only
against the backlog, which is what stops the two review axes from filing
the same defect twice. The two direct filing paths (`gh`, Forgejo) never
reach the Launcher, so their dedup stays entirely the Filer's own: it
runs the open-issue search above itself, keyed on the finding's site
rather than its prose, and appends the same marker line to the body it
files, keeping the marker format identical across both write mechanisms.

##### Filing volume on the status output

Every settle that reaches the filing step prints one tally line on the run's
own status output, work path and research path alike:

```
    #1234  filed=ok:2,failed:1,skipped:1
```

`ok` counts the issues actually filed; `failed` counts the filing attempts
that errored, so a run that tried and failed does not read as a quiet run;
`skipped` counts an intent the Launcher's own dedup index caught before it
ever reached the tracker (issue #3609) — a duplicate that never became an
API call, so it belongs in neither of the other two. The line prints even
when nothing was filed (`filed=ok:0,failed:0,skipped:0`), which is what
makes "reached filing, filed nothing" distinguishable from a run that never
got that far and prints no tally line at all. That makes the
findings-per-run rate readable straight off a dogfood log or a dispatch
summary, without querying the tracker (issue #3608). The value is a
comma-joined set of `name:count` pairs, parsed by name, never by position —
which is exactly what let `skipped` slot in here as one more pair rather
than a breaking format change, and lets the next added count do the same.

##### Research filing

The Filer also backs the research Dispatch kind (see [Research
dispatch](#research-dispatch)): whenever a research run — ordinary or
`--self-contained`, read-only or read-write, orchestrator on or off — has
the Filer provisioned (same `models.filer` roster switch as above, no
separate research knob), both research prompts render a filing step letting
the researcher hand any finding worth tracking, beyond the issue's own
verdict, to the filer subagent. Unlike the work path's read-only/orchestrator
fork above, research filing is relay-only **unconditionally** — there is no
direct-file mode for research at all. The filer emits one
`SPINDRIFT_ISSUE_INTENT` line per finding; the Launcher files each host-side
after the Box exits, applying `agent-research-finding` (not
`agent-review-finding`) and a `Filed from research on #<N>` backlink to the
filed issue's body.

This changes observable read-write behavior too: once the Filer is
provisioned, the research verdict comment itself now always relays through
the Launcher (`SPINDRIFT_COMMENT`) as well, even under `read-write`, where an
unmodified research Box used to post its own `gh issue comment` directly.
The Launcher files each intent first, appends a "Filed issues" section
listing the real URLs — or, for a filing that failed, an inline
title-and-summary bullet — right after the comment body, then posts the
combined comment host-side in one call, so the researcher never fabricates
an issue URL itself. An intent the host-side dedup check (dedup.go, issue
#3609) matches against an already-open finding is never filed at all, and
gets its own "## Skipped (deduplicated)" section instead, naming what it
matched — the open issue's number, or the title of a peer this same run
filed moments earlier — so a run where every finding dedups still says so
in the posted comment, rather than appending nothing (issue #3811); the
work path posts that same section as a standalone comment of its own,
since gate.go has no filed-issues comment to append it to, so a dedup skip
is visible in the tracker artifact on both paths. Without the Filer
provisioned, the filing step and its "plus, when the Filer is provisioned"
closing reference both vanish from both research prompts, and the verdict
comment posts directly from the Box as always — the filing step and the
relay-comment change are both gated purely on Filer presence.

Override the filer's system prompt the same way as `scoutPrompt`/
`reviewPrompt`: the `filerPrompt` `mkHarness` argument (image rebuild), or
`SPINDRIFT_PROMPT_DIR` at runtime (zero-rebuild, works regardless of which
caller baked the image).

#### Create the labels on the Target repo

`gh issue edit` cannot invent a label, so all four must already exist on the
Target repo. `spindrift doctor` checks this and, in interactive mode, offers to
create any missing labels. To create them manually:

```sh
gh label create ready-for-agent   --repo owner/repo --color 0075ca --description "Fully specified; ready for an AFK agent"
gh label create agent-in-progress --repo owner/repo --color e4e669 --description "An AFK agent is actively working this issue"
gh label create agent-complete    --repo owner/repo --color 0e8a16 --description "Agent work merged and green"
gh label create agent-failed      --repo owner/repo --color d93f0b --description "Box exited non-zero; needs human triage"
```

#### Create the research labels on the Target repo

The standing/trigger and crash-triage labels (`agent-research`,
`agent-research-in-progress`, `agent-research-failed`) are a fixed,
non-configurable vocabulary — `agent-research.yml` keys off these names
directly. The three verdict-terminal labels below them are the compiled
*default* vocabulary; see [Configuring the research verdict vocabulary
(`RESEARCH_VERDICTS`)](#configuring-the-research-verdict-vocabulary-research_verdicts)
to change the verdicts and their labels. `spindrift doctor` checks and, in
interactive mode, offers to create these too, but treats them as advisory:
unlike the four triage labels above, a missing research label never fails
the check (so CI `doctor` runs stay green for deployments that don't use
research yet). To create the default set manually:

```sh
gh label create agent-research             --repo owner/repo --color fbca04 --description "Apply to fire a research dispatch"
gh label create agent-research-in-progress --repo owner/repo --color bfd4f2 --description "A Box is reviewing this issue"
gh label create agent-research-recommend   --repo owner/repo --color 2cbe4e --description "Relevant and enriched — promote it"
gh label create agent-research-reject      --repo owner/repo --color e11d21 --description "False positive, not worth it, or a duplicate — close it"
gh label create agent-research-unclear     --repo owner/repo --color d4c5f9 --description "Needs a human answer — answer, then re-apply agent-research"
gh label create agent-research-failed      --repo owner/repo --color b60205 --description "Box crashed or produced no verdict; needs human triage"
gh label create agent-research-finding     --repo owner/repo --color c5def5 --description "Filed from a research finding"
```

If a custom `RESEARCH_VERDICTS` set is configured, create its labels
instead of (or alongside, if some overlap) the three above.

`agent-research-finding` is a provenance label, not a dispatch label: when a
research dispatch's Filer is provisioned, the Launcher applies it, host-side,
to every issue it relays on the Filer's behalf (ADR 0041; see
[Filer](#filer) for the relay mechanics) — same contract as
`agent-review-finding` on the work path, a human promotes the filed issue to
`ready-for-agent` like any other. `spindrift doctor` already checks and, in
interactive mode, offers to create it alongside the rest of the research
family above.

#### Create the priority labels on the Target repo

The `agent-priority-{critical,high,low}` labels (ADR 0040) are a fixed,
non-configurable vocabulary that `ResolvePriority` matches by exact name.
`spindrift doctor` checks and, in interactive mode, offers to create these
too, but treats them as advisory: like the research labels above, a missing
priority label never fails the check. To create them manually:

```sh
gh label create agent-priority-critical --repo owner/repo --color d73a4a --description "Drop everything — highest dispatch priority"
gh label create agent-priority-high     --repo owner/repo --color ff8c00 --description "Dispatch ahead of normal-priority issues"
gh label create agent-priority-low      --repo owner/repo --color 8a9ba8 --description "Dispatch behind normal-priority issues"
```

#### Create the ambiguous-spec label on the Target repo

The `agent-ambiguous-spec` label (issue #2275) is a single fixed,
non-configurable label the pre-implement gate applies when an issue is
internally contradictory rather than merely hard — a distinct terminal from
`agent-failed`, since the Box halted deliberately rather than crashing.
`spindrift doctor` checks and, in interactive mode, offers to create it too,
but treats it as advisory: like the research and priority labels above, a
missing `agent-ambiguous-spec` label never fails the check. To create it
manually:

```sh
gh label create agent-ambiguous-spec --repo owner/repo --color e0cffc --description "An internally-contradictory issue; needs a human decision — not a crash"
```

#### Configuring the research verdict vocabulary (`RESEARCH_VERDICTS`)

By default the research kind's verdict terminals are the fixed three above:
`recommend` → `agent-research-recommend`, `reject` → `agent-research-reject`,
`unclear` → `agent-research-unclear`. `RESEARCH_VERDICTS` (flake option
`issues.research.verdicts`, schema key `researchVerdicts`) makes
that vocabulary — and each verdict's label — operator-configurable instead,
per [ADR 0022's amendment for issue
#2201](adr/0022-research-is-a-dispatch-kind.md#amendment-issue-2201-the-verdict-vocabulary-and-label-mapping-are-configurable-via-research_verdicts).
The default is the empty string, which is exactly the built-in three above
with no behavior change — this knob only matters once set.

The value is a JSON array of objects, **order preserved**, each with a
`verdict` token, the issue-tracker `label` its Complete transition swaps to,
and a one-line `description` of what it means (rendered into the research
prompt's verdict contract):

```json
[
  { "verdict": "approve", "label": "agent-research-approve", "description": "relevant; promote it." },
  { "verdict": "decline", "label": "agent-research-decline", "description": "not worth doing." }
]
```

The launcher (`forge.ParseResearchVerdicts`) validates the value at startup:
the array must be non-empty; every `verdict` and `label` must be non-empty;
verdict tokens must be unique and contain no whitespace; and no verdict
token may be the reserved `blocked` status, which stays the escape hatch for
"the Box crashed or produced no verdict" regardless of configuration. A
malformed value aborts startup rather than silently falling back to the
default. The same five rules are also enforced at nix eval time
(`lib/research-verdicts.nix`'s `parse`), so a malformed `RESEARCH_VERDICTS`
fails the flake build itself rather than only surfacing at launcher startup.

On Settle, the launcher parses the posted outcome line's verdict against
this configured set and applies the mapped label. An unrecognized verdict —
one not in the configured set, the reserved `blocked` token, or a missing
outcome line entirely — is never silently mapped to some label anyway; it
routes the issue to `agent-research-failed`, the same crash/no-verdict path
a genuinely absent verdict already takes.

The vocabulary is not launcher-only: the research prompt's machine-checkable
verdict contract — the VERDICT bullet list, the verdict enumeration, and the
`status=<...>` outcome-line alternation — is rendered from the same
configured set at build time (`lib/research-verdicts.nix`, wired through
`lib/mkHarness.nix`), so a custom set reaches both what the launcher accepts
and what the Box is told to emit. This is a `flakeOption` like the label
knobs (baked into the image), not a zero-rebuild runtime switch like
`SPINDRIFT_PROMPT_DIR` — changing it requires an image rebuild. The prompt's
surrounding *guidance* prose (for example, the "Open questions — mandatory
when the verdict is `unclear`" step, which names specific default verdicts
by name) is not rewritten from a custom set — only the machine-checkable
contract and each verdict's label render dynamically; rewriting the
per-verdict semantic guidance itself is out of scope here and applies
equally to the self-contained research mode's own baked prompt — see
[Self-contained research mode](#self-contained-research-mode).

**Caveat for a `SPINDRIFT_PROMPT_DIR` override of `research-prompt.md` /
`research-self-contained-prompt.md` specifically:** both checked-in
templates carry two injection markers (`<!-- RESEARCH_VERDICT_BULLETS -->`
and `` `<RESEARCH_VERDICT_ENUM>` ``) that only `lib/research-verdicts.nix`'s
`render` resolves, and that resolution happens at nix build/eval time (when
the harness image is baked), never at container runtime. An unmodified copy
of either template dropped into a `SPINDRIFT_PROMPT_DIR` override directory
therefore ships those markers to the agent verbatim, unrendered — the agent
would be told to "Render exactly one of these verdicts:" followed by
nothing. An operator overriding either of these two files via
`SPINDRIFT_PROMPT_DIR` must supply their own fully-written-out VERDICT
section (no markers) in their override copy, the same as they always had to
for any other prompt content.

#### Caveat: a killed launcher can strand an issue

The label swaps are best-effort. If the launcher is killed mid-run (Ctrl-C, a
crashed host, a laptop closing) an issue can be left in `agent-in-progress` with
no container running. `spindrift dispatch` never reconciles this on its own —
a bare `agent-in-progress` label is indistinguishable from an issue a live
runner is still working, so automatic adoption would risk force-pushing or
merging over that runner's in-flight commits (#600). Recovery is always an
explicit, opt-in operator action:

- **An open PR already exists (draft or not)** — label the issue
  `agent-recover`. `agent-recover.yml` claims it and runs `spindrift recover
  <n>`, driving that PR through the same adopt-and-gate path (flipping a
  draft to ready before re-running the merge gate).
- **No PR was opened yet** — there is nothing to adopt. Move it back to
  `ready-for-agent` to re-dispatch (or to `agent-failed` to park it).

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

A local issue has no in-box reachability — there's no server to reach, and
`gh issue view` inside the Box either fails or, for a numeric slug, silently
fetches an unrelated real issue on the Target repo. Neither the subject
issue's own body nor its linked issues need an in-box read any more: the
launcher resolves the subject issue's whole `## Blocked by`/`parent` link
chain host-side at dispatch, transitively, and renders it into the prompt's
`# ISSUE TEXT` section as a `## Linked issues` block after the subject body
(issue #3469), following on from the subject-issue-only injection (issue
#3445). Each linked issue in that block carries its status and body; an
unresolved reference and any entry omitted for size are listed too, all
within one 64KB injection budget, and a linked issue the launcher can't read
degrades to an unresolved entry rather than failing the dispatch — the
numeric-slug footgun above is exactly why this is a host-side resolve
instead of a live in-box lookup by number. The Box never gets a mount of
`LOCAL_ISSUES_DIR` at all: with both the subject issue and its linked
issues injected host-side, the read-only `/issues` bind ADR 0032 originally
required is retired, and `LOCAL_ISSUES_DIR` is read only host-side, by the
Launcher, to build `ISSUE_TEXT` — see [ADR
0050](adr/0050-local-issue-reads-cross-the-seam-as-host-injected-text.md).
This retires only the issue-plane exception to zero-shared-host-filesystem;
under `CODE_FORGE=local` the read-only Accumulation repo mount and the
writable outbox ([ADR 0033](adr/0033-host-mediated-local-code-forge.md))
remain documented exceptions. `github` (and `jira`) Dispatches are
unchanged — they keep reading and writing in-box via `gh issue view`/`gh
issue comment` for anything beyond the subject issue.

Each issue is one file, named `<slug>.md`, where `<slug>` is the issue's ID
(used anywhere the GitHub backend would use an issue number — dependency
refs, branch names, log file names). The ID must be a bare filename directly
inside `LOCAL_ISSUES_DIR`; one that resolves into a subdirectory or outside
the directory (e.g. `../../x`) is rejected with an error rather than
sanitized:

```markdown
---
title: Fix the thing
state: ready-for-agent
labels: [bug, priority-high]
created: "2026-07-09T12:00:00Z"
parent: some-upstream-slug
landing: "https://github.com/owner/repo/pull/123"
---
## What to build

...

## Blocked by

- some-other-issue-slug
```

(`closed:` is omitted above since this issue is open — the default — see below.)

- `title`, `labels`, `created` (RFC 3339) mirror the GitHub adapter's fields.
- `state` is the dispatch-state marker the launcher swaps in place —
  `ready-for-agent` / `agent-in-progress` / `agent-complete` / `agent-failed`
  by default (same names as `LABEL`/`IN_PROGRESS_LABEL`/`COMPLETE_LABEL`/
  `FAILED_LABEL`, which still apply — the local adapter uses them as the
  frontmatter value instead of a GitHub label).
- `parent` is optional and opaque — the local tracker is standalone; any
  linkage to an upstream tracker (a GitHub URL, a Jira key, another local
  issue's slug) is out of scope (ADR 0013) and never resolved by spindrift
  itself. Under `CODE_FORGE=local` it doubles as the seam's broad-ticket
  key (ADR 0033): the issue lands onto `integration/<sanitized-parent>` in
  the Accumulation repo, sanitized to a git-ref-safe token (lowercased,
  each run of non-`[a-z0-9]` characters collapsed to a single dash,
  leading/trailing dashes trimmed). A parentless issue lands on
  `integration/<own-sanitized-slug>` instead — a parentless seam is its own
  broad ticket.
- `closed` is a boolean, local-only open/closed axis (ADR 0029), independent
  of `state`: absent or `false` means open; `true` excludes the issue from
  both `ListOpenIssues` and `ListIssues`, so it is never re-dispatched or
  shown as outstanding. `reconcile` (below) is the sole authority that sets
  `closed: true`.
- `landing` is the immutable landing reference (a PR URL, or a push-only
  branch ref under `CODE_FORGE=git`) the launcher writes after a work
  outcome line is parsed. It's a plain pointer, not cached merge-state — a
  later `reconcile` re-checks the forge live rather than trusting this field
  for anything beyond "where did this land." `reconcile` (below) also writes
  it itself when it discovers a PR by agent branch for an issue with no
  recorded `landing`.
- `abandoned` is a boolean, local-only flag (ADR 0029) `reconcile` (below)
  sets when the issue's `landing` PR was closed without merging — a human
  rejected it. The issue stays open (`abandoned` never implies `closed`), so
  it keeps showing up in `ListOpenIssues` as something needing attention,
  rather than waiting forever on a merge that will never come.
- **Canonical order is ascending `created`** — the local analogue of GitHub's
  ascending issue-number order.
- **Dependencies** come from a `## Blocked by` section: one issue slug per
  bullet, no `#N` refs (local issues aren't numbered).
- `spindrift doctor`'s label-presence check always passes for the local
  adapter — there is no separate label registry to check; the four dispatch
  markers above always exist as values the `state` field can take.
- **Selective dispatch by slug**: `spindrift dispatch <slug>` (and
  `preview`/`research`) takes the same selective path as a numeric ID list
  like `spindrift dispatch 42 57` — every positional is an opaque issue ID
  regardless of tracker, so a single list can mix slugs and numbers in any
  order — bypassing the label/barrier gates and dispatching exactly the
  issues named.

#### `reconcile`: closing a local issue

`spindrift reconcile` is the local-tracker bookkeeping sweep (ADR 0029): the
sole authority that closes a local issue. It is observational — it never
lands code. On `github`/`jira` it prints a plain "nothing to do" line instead
of acting; `dispatch` also auto-invokes it as a final step whenever
`ISSUE_TRACKER=local`, so the common loop (dispatch → immediate-merge → issue
closes) needs no extra command.

Per open local issue carrying a recorded `landing`, reconcile asks the Code
Forge whether that PR merged and, if so, sets `closed: true`; an issue whose
`landing` PR is still open (green-and-mergeable, or in approval limbo) is
left untouched. For an open issue with **no** recorded `landing` — the Box
died after opening a PR but before its outcome line was parsed — reconcile
discovers the PR by the issue's agent branch (the same branch→PR resolution
`PRForge.PRForBranch` gives every other call site), records it as `landing`,
then closes the issue if that discovered PR already merged; if it's still
open, reconcile records the landing and leaves the issue for a later sweep.
When a `landing` PR (recorded or freshly discovered) was closed **without**
merging, reconcile sets `abandoned: true` instead of closing — a human
rejected it, so there's no merge left to wait on. Running it twice never
double-acts: a closed issue drops out of `ListOpenIssues`, and an
already-`abandoned` issue is skipped on later sweeps, so neither is ever
revisited.

The gated `InProgress → Dispatchable` orphan-reset is later work — see
[ADR 0029](adr/0029-local-issue-lifecycle-and-reconcile.md).

#### PR-body ticket reference (`LOCAL_ISSUE_REFERENCE`)

`ISSUE_NUMBER` is the ticket's file slug when `ISSUE_TRACKER=local`, and a
slug can be numeric (`.../42.md` → slug `42`). An unconditional
`Closes #${ISSUE_NUMBER}` in the PR body — the `github`-tracker default — would
therefore risk silently closing real GitHub issue #42 on the Target repo, on
top of leaking a private ticket slug into a PR body on the shared remote. To
avoid both, the PR-body reference is a three-way conditional fragment (see
the Conditional fragment registry above) keyed on `ISSUE_TRACKER` and the
`LOCAL_ISSUE_REFERENCE` global setting (grouped settings surface, ADR 0015;
`issues.localReference`):

- `ISSUE_TRACKER=github`: unchanged — the PR body still MUST contain
  `Closes #${ISSUE_NUMBER}`.
- `ISSUE_TRACKER=local`, `LOCAL_ISSUE_REFERENCE` off (**default**): the PR
  body has no reference to the ticket at all — no slug, no `Closes`/`Fixes`.
- `ISSUE_TRACKER=local`, `LOCAL_ISSUE_REFERENCE` on: the PR body carries
  `Local-issue: <slug>` — a plain, non-auto-closing breadcrumb for
  correlating a PR back to its private ticket — never a `Closes`/`Fixes`
  keyword, so a numeric slug can never trigger GitHub's issue auto-close.

Default off, because the local tracker's whole premise is a private ticket
folder (see above); opt in only if you want that traceability and have
weighed the slug leaking into the (shared, `github` Code Forge) PR body.

### Local code forge (`CODE_FORGE=local`)

`CODE_FORGE=local` is the code-plane mirror of [Local issue tracker
(`ISSUE_TRACKER=local`)](#local-issue-tracker-issue_trackerlocal) above — the
two host-mediated `local` planes (ADR 0033, ADR 0032): a local backend is
host-mediated because it isn't reachable from inside the Box, on the issue
plane and now the code plane alike. Pair the two for the fully private,
fully offline loop, or mix `CODE_FORGE=local` with `ISSUE_TRACKER=github` (or
vice versa) to keep only one plane private. See [ADR
0033](adr/0033-host-mediated-local-code-forge.md) for the full design and
[ADR 0032](adr/0032-host-mediated-local-issue-content.md) for the issue-plane
precedent it mirrors.

**Accumulation repo.** Code accumulates in a bare repo the launcher owns —
`.spindrift/accum.git` under the launcher's working directory by default,
overridden by `CODE_FORGE_ACCUMULATION_REPO_DIR`. The launcher auto-creates
it and seeds its base ref from `BASE_BRANCH` in the operator's own checkout,
offline, before any Box runs — idempotently on every run thereafter, so
there is no operator setup step.

**Seeding is cross-process locked (issue #2441).** Seeding force-pushes the
base branch into the Accumulation repo — a full mirror, including a rewind
if the operator's checkout moved backwards — which is safe within one
launcher process (seeding runs once, before any Box), but not across two:
a second, independent `spindrift` process (e.g. a `research` run and a
`dispatch` run against the same `CODE_FORGE_ACCUMULATION_REPO_DIR`) seeding
while the first process still has a Box mounted against it could rewind the
ref out from under that Box mid-flight. The launcher guards this with a
non-blocking exclusive file lock (`<accum-repo-path>.lock`), held from
seeding through the end of the whole process's run — every Box it
dispatches, not just the initial seed. A second process that tries to seed
the same Accumulation repo while the first still holds the lock fails fast
with a clear error naming the repo path, rather than blocking or silently
racing the first process's mount; the operator re-runs once the first
process exits. `console` is the one case where "the whole run" is a full
interactive session, not a single seed-and-dispatch: it holds the lock from
startup until the operator quits, so a `dispatch`/`research` process against
the same Accumulation repo fails fast for as long as a `console` session
stays open. The lock is advisory and per-`CODE_FORGE_ACCUMULATION_REPO_DIR`
path, but that default resolves to one shared `.spindrift/accum.git` under
the launcher's working directory (not one per ticket) — so, by default, two
unrelated tickets dispatched from the same checkout now contend for the same
lock too, serializing what used to run concurrently; give each its own
`CODE_FORGE_ACCUMULATION_REPO_DIR` to keep them independent.

Edge cases:

- **The lock file is never deleted.** `AcquireAccumulationLock` creates
  `<accum-repo-path>.lock` on first use and only ever unlocks/closes it on
  release — a leftover `accum.git.lock` sitting next to `accum.git` between
  runs is normal, not a sign of a stale or crashed process. `rm -rf
  .spindrift/accum.git` removes the repo but leaves its sibling `.lock` file
  in place; that's harmless, since the next run reopens and re-locks the
  same file.
- **A killed process can't strand the lock.** `SIGKILL` or a crash never
  runs `Release`, but the kernel drops the underlying `flock` when the
  holding process's file descriptors close on exit either way, so the next
  acquire succeeds immediately — there is no stale-lock recovery step for an
  operator to run.
- **Self-contained research and the remote Code Forges take no lock at
  all.** `--self-contained` research and `CODE_FORGE=github`/`git` never
  call `seedAccumulationRepoIfHostMediated`'s seeding path, so nothing acquires the
  lock for them; only non-self-contained runs under `CODE_FORGE=local` do.
- **`flock` is advisory-only, and unreliable over a network filesystem.**
  A process that doesn't check the lock can still write through it, and on
  NFS or similar, `flock` semantics aren't guaranteed at all — this
  serialization assumes `CODE_FORGE_ACCUMULATION_REPO_DIR` lives on a local
  filesystem, the same assumption ADR 0033's host-mediated design already
  makes for the Accumulation repo itself.

**Code-in / code-out.** The launcher RO bind-mounts the Accumulation repo
into the Box at `/repo`; the agent clones it read-only and works in the
tmpfs work dir, exactly as it would clone a real remote. The Box can't push
back through a read-only mount, so it emits its branch instead as a `git
bundle` (`seam.bundle`) written to a small, writable `/outbox` mount — the
code-plane analog of ADR 0032's stdout comment block. The launcher relays
that bundle host-side, fetching it into the Accumulation repo.

**Landing.** The launcher rebases the relayed branch onto
`integration/<parent>`'s current tip and fast-forwards the Integration branch
there — one Integration branch per broad ticket, always linear, never a merge
commit (issue #1889; this is unconditional, not a knob — the remote `git`/
`github` Code Forges keep their own merge-commit landing unchanged).
`<parent>` comes from *that seam's own* local issue's
`parent:` frontmatter, sanitized to a git-ref-safe token (lowercased, each
run of non-`[a-z0-9]` characters collapsed to a single dash, leading/trailing
dashes trimmed); an issue with no `parent:` set falls back to its own
sanitized slug instead, so a parentless seam is its own broad ticket rather
than sharing one collapsed branch. A clean rebase-and-fast-forward **is** the
landing — no PR, no CI, no network — and closes the seam through the same
`reconcile` path (above); a seam that cannot rebase cleanly onto the current
Integration tip leaves the seam unlanded and blocked instead, the Integration
branch untouched — there is no PR or dispatcher on this path to auto-resolve
it, so a rebase conflict simply blocks the seam.
`landing:` records `<integration-branch>@<sha>`, the immutable ref + commit
the land produced.

**`MERGE_MODE=immediate` only.** `CODE_FORGE=local` requires
`MERGE_MODE=immediate` — the rebase-and-close described above *is* the
"immediate" behavior. `manual` and `auto` have no meaning under `local`
(there is no PR to leave open, no GitHub auto-merge to enqueue); either one
would strand the relayed bundle unlanded in the outbox, so the launcher
fails fast at startup instead.

**Chaining is the `## Blocked by` graph, not a knob.** How seams compose
across a broad ticket is not a separate mode: it falls out of the `##
Blocked by` graph the operator already authors, driven by the existing
`waves` scheduler — independent seams fan out in parallel, dependent seams
wait on their blockers, and all converge on the one Integration branch. For
`local`, "blocker met" reduces to a local frontmatter fact — the blocker
seam's issue is closed on disk — so the whole DAG schedules fully offline,
with no remote PR query in the loop.

**Auto-surface (#1730).** Once a broad ticket's seams are all landed and
closed, the launcher fast-forwards `integration/<parent>`'s current tip into
the operator's own checkout as a local branch named after the ticket — the
parent key itself for a parented ticket, or the issue's own sanitized title
for a parentless one — a host-side fetch that creates or fast-forwards only
that branch ref, never
switches the operator's currently checked-out branch, and never pushes to
`origin`. Nothing is surfaced for an incomplete ticket, and an
already-surfaced unchanged branch is a no-op. Surfacing the assembled branch
locally is as far as spindrift goes — there is no `finalize` verb yet; the
operator still publishes the team PR manually with the `git push origin
<branch>` / `gh pr create` gestures they already know.

A broad ticket may itself be a tracked issue, but it is never one of its own
seams: an issue is the broad ticket rather than a seam of itself exactly when
its resolved key equals its own sanitized slug *and* at least one other issue
resolves to that same key — unless excluding every such colliding issue
would leave the group with none left, in which case none is excluded (issue
#3439). An excluded issue neither gates the surface nor counts toward the
surfaced seam count, and it is left open and untouched — `reconcile` remains
the sole authority for the `closed:` field, so the operator never has to
hand-set `closed:` on a broad ticket to get a surface. The surfaced branch
still takes the sanitized parent key regardless of where the broad-ticket
issue falls in `created:` order relative to its seams.

The exclusion holds even when the excluded broad-ticket issue was itself
dispatched and its own landing is stuck: that landing never becomes a
`held — stuck landing` verdict, so once every real seam has landed and
closed Surface prints the ordinary `surfaced → branch <name>` verdict.
(A stuck landing on one of the group's *real* seams still holds the
surface, as does a real seam still open.) That is deliberate, not an
oversight (issue #3440) — Surface's verdict reports on a broad ticket's
seams, and the excluded issue is not one of them. The stuck landing is
not lost: `reconcile`'s own per-issue sweep prints `status=stuck` for it,
as it does for any open issue whose recorded landing never merged into
its Integration branch, so the operator sees it there.

### Read-only Box (`BOX_FORGE_AND_ISSUE_ACCESS=read-only`)

`BOX_FORGE_AND_ISSUE_ACCESS=read-only` brings the `github` Code Forge and
Issue Tracker to parity with the host-mediated model `local` has always used
(ADR 0032, ADR 0033, and [ADR
0034](adr/0034-host-mediated-github-forge-and-issue-access.md) for the
`github` extension). The Box's `GH_TOKEN` collapses to
**read-only** — Contents R, Issues R, Metadata R (see [Read-only Box
token](#read-only-box-token) below) — and every write the Box would
otherwise make in-box instead travels out as a stdout block or a bundle for
the Launcher to apply with its own, separately-scoped write token:

| write            | `read-write` (default)  | `read-only`                          |
| ---------------- | ------------------------ | ------------------------------------- |
| land the branch   | `git push`               | `seam.bundle` written to the outbox; the Launcher relays it (ADR 0033's mechanism, reused) |
| open the PR       | `gh pr create --draft`   | a nonce-guarded, base64-encoded `SPINDRIFT_PR_INTENT` stdout line; the Launcher opens the draft PR host-side |
| post a comment     | `gh issue comment`       | a single nonce-guarded `SPINDRIFT_COMMENT` line; the Launcher verifies the nonce, decodes it, and posts it host-side (ADR 0032's mechanism, reused) |
| file an issue (Filer, opt-in) | `gh label create` / `gh issue create` | one nonce-guarded, base64-encoded `SPINDRIFT_ISSUE_INTENT` stdout line per issue; the Launcher files each one host-side (issue #2018) — see [Filer](#filer) |

A stray `git push` in the "land the branch" row above — the agent guessing at
the read-write workflow — fails locally instead of reaching the forge and
403ing there (issue #2463): the Box repoints `origin`'s push URL at a
throwaway local repo and installs a `pre-push` hook that always refuses, so
the failure is instant and names the actual hand-off instead of a bare 403.
This guard installs for `github`, `local`, and `forgejo` — every CODE_FORGE
choice permitted under read-only today has a genuinely relay-based hand-off:
`github` and `forgejo` carry `outboxRelayCapable` (issue #2927 gave
`forgejo` the bit `github` already had), and `local` carries
`hostMediatedRemote` instead. No backend registered today leaves a real
`git push` as its read-only hand-off; that shape exists only as latent guard
infrastructure for a hypothetical future backend.

The other three rows above get the same local-guard treatment, but via a `gh`
shim rather than a push hook (issue #2465): for a read-only `github` Box (the
same `_is_readonly_github` gate the push guard above uses), the Box installs a
`gh` shim ahead of the real `gh` binary on `PATH` that rejects `gh pr create`,
`gh pr ready`, `gh pr merge`, `gh issue comment`, `gh issue create`, and `gh
api` calls with a mutating method (`POST`/`PATCH`/`PUT`/`DELETE`), each
rejection naming the relay that replaces it — the PR-intent line, the
Launcher's own ready/merge once CI is green, the outcome `note=` field, or the
issue-intent line, as appropriate. Reads (`gh issue view`, `gh pr view`, `gh
run view`, `gh run list`, a plain `gh api` `GET`, and so on) pass through to
the real `gh` untouched. Like the push guard above, this is a cheap interim
guard, not adversary-proof.

The issue-filing row is additionally gated on `ORCHESTRATOR_ENABLED` (issue
#2019), unlike the three rows above it: `read-only` with the orchestrator off
keeps the Filer's pre-existing degraded behavior (it still attempts `gh issue
create`, fails, and the main agent falls back to pasting the findings into
the PR body) rather than switching to the relay — the relay only activates
once `read-only` and `ORCHESTRATOR_ENABLED` are both true.

This is checked by capability, not by `CODE_FORGE`/`ISSUE_TRACKER` value
alone: the selected forge must implement bundle-relay and host-side
draft-PR-create, and the selected tracker must implement host-posted
comments. mkHarness's `readOnlyCapabilityOk` eval assert (issue #2526)
enforces this at `nix build` (Consumer eval) time, before an image can ever
exist, reading the same backend-registry capability bits the launcher's own
`checkReadOnlyCapabilityGate` checks; a mismatched combination throws at
build time naming the missing seam. The launcher's Go-level gate survives
only as a backstop against a *runtime* override of
`BOX_FORGE_AND_ISSUE_ACCESS`/`CODE_FORGE`/`ISSUE_TRACKER` past what nix
already validated — it exits with a startup error naming the missing seam in
that case. `local` satisfies the check by construction (there is no other
way for it to work); `github` satisfies it as of issue #1919; `forgejo`
satisfies it via the `relayCapable`/`hostPostingCapable` registry bits this
issue (#2526) itself added to its backend row. `read-write` is unaffected
either way — it never inspects these capabilities.

Inside the Box, the write-enabled-vs-not decision is resolved once,
host-side, and forwarded as a single explicit positive signal
(`BOX_WRITE_ENABLED`, present only under `read-write`); the Box's prompt
fragments render the no-write path whenever it is absent, for any reason
(issue #1951) — an unset, typo'd, or forwarding-glitched value can never
fall open into the write-capable path.

A second startup gate (issue #1950) checks the *token*, not just the forge/
tracker shape: `read-only` also aborts unless `BOX_GH_TOKEN` is set and
differs from the Launcher's own `GH_TOKEN`. Where the Box token can be
introspected — a classic/OAuth PAT (`X-OAuth-Scopes`) or a GitHub App
installation token (the repo endpoint's `permissions.push` field) — it's
rejected if it carries write access; a fine-grained PAT can't be introspected
reliably, so it's accepted with a loud warning instead. `spindrift doctor`
reports this gate's outcome the same way it reports labels and forge
connectivity. `read-write` never inspects `BOX_GH_TOKEN` either way.

`read-only` is an **alternative route to the same guarantee** [two-actor
separation](#two-actor-separation-opt-in-hard-mode) provides: a Box that
cannot unilaterally land on the base branch. Two-actor separation gets there
by capability plus an out-of-band repository ruleset (the Box's token could
still open a PR and merge it; the ruleset is what stops it). Read-only gets
there by capability alone — the Box's token cannot open a PR or merge one in
the first place, because it never receives a token that can. Pick two-actor
separation when the Box still needs to author the PR/comment content itself
(the common case today); pick read-only for the strongest available posture,
at the cost of the Launcher doing more host-side work per issue. The two are
not mutually exclusive but are also not additive: `read-only` alone already
removes the capability two-actor separation was closing off with a ruleset.

### Signal socket (`BOX_SIGNAL_CARRIER=socket`)

The default `log` carrier sends the three mid-run signal channels
(`SPINDRIFT_COMMENT`, `SPINDRIFT_PR_INTENT`, `SPINDRIFT_ISSUE_INTENT`) as
nonce-guarded marker lines in the Box's stdout log — unchanged for every
existing Consumer. `BOX_SIGNAL_CARRIER=socket` moves those same three
channels off the log and onto a per-Dispatch Signal socket instead ([ADR
0052](adr/0052-mid-run-signals-cross-the-seam-over-a-launcher-socket.md)),
closing the defect the log carrier can't: the `BASH_MAX_OUTPUT_LENGTH`
Bash output cap (see [Claude Code output caps](#claude-code-output-caps))
truncates a large payload before the marker line ever reaches the log,
silently losing the signal.

The Launcher starts the listener before the container comes up and mounts
it read-write at the in-Box path `/signal-socket.sock`, beside the registry
proxy's own socket. It hands the Box `SIGNAL_SOCKET_ENDPOINT`
(`unix:///signal-socket.sock`, or `http://host:port` on the TCP fallback)
and, on the TCP fallback only, `SIGNAL_SOCKET_SECRET` — a secret minted
independently of `RUN_NONCE` and kept off argv. The listener closes at Box
exit; its accepted buffer stays readable after that, until settle has
consumed it.

The transport is decided by the same per-Dispatch
`RegistryProxyTransport` probe the registry proxy itself consumes
(`cmd/launcher/internal/dispatch/box.go`) — one probe serves
both seams — and returns one of three verdicts: a unix socket,
its TCP fallback, or an indeterminate/unavailable answer.
`spindrift doctor` surfaces this ahead of time: under
`BOX_SIGNAL_CARRIER=socket` it adds an Advisory `signal-socket-transport`
row (the row renders only under `socket`; nothing prints under `log`)
reporting `unix socket` or `tcp`. Three cases degrade that row, each
with a message naming the knob and the mode: `NETWORK_MODE=none`, which
degrades regardless of the transport and without running the probe at
all (loopback is torn down outright, so there is nothing to check); a
TCP verdict under `NETWORK_MODE=no-host-loopback`; and an indeterminate
probe answer. Advisory means the row never fails `spindrift doctor`
itself, it just tells the operator before dispatch what the
Dispatch-level gate below would otherwise only surface mid-run.

Requesting `socket` where the transport can't work is a startup error,
never a silent fallback to `log`: `NETWORK_MODE=none` fails at launcher
startup (the socket transport needs the loopback this mode tears down), and
`NETWORK_MODE=no-host-loopback` fails when the Dispatch starts — before any
container — once the per-Dispatch transport probe returns the TCP verdict.
Both errors name the knob and the mode.

The three log scanners still run under `socket` mode, but only to warn if a
marker line is still present in the log; such a line contributes no data —
no fallback, no double delivery. Put together, the rule is: a marker line
under `socket` mode is ignored with a warning and never contributes data,
and a `socket` request the transport cannot serve is always a startup or
Dispatch error, never a quiet fall back to `log`.

`BOX_SIGNAL_CARRIER` takes exactly two values, `log` or `socket`; the
default is `log`, unchanged for every existing Consumer — see the knob's
own row in [Runtime configuration](#runtime-configuration). This is a
runtime-only knob — no `spindrift.*` setting and no flake option — so the
daemon passes it through its child environment like any other env-only
knob. There is no live toggle: `BOX_SIGNAL_CARRIER=socket nix run
.#daemon` flips the carrier for every child the daemon starts from that
point forward, and a restart is the only way to flip it back — see
[Daemon](#daemon). The in-Box front for `socket` mode is the `driver-exec
signal comment|pr-intent|issue-intent|status` verb (issue #3724), which
reads `SIGNAL_SOCKET_ENDPOINT` and errors when it is unset — so it serves
the socket carrier only; under `log` the front stays what it has always
been, a nonce-guarded marker line the Box prints.

Each subcommand takes its body from stdin or `-body-file`, never argv —
argv is world-readable through `/proc`, and a signal body can run to tens
of KB. `-title`, `-type` and `-dedup` stay flags: all are short enough
that neither concern bites. `pr-intent` requires `-title`; `issue-intent`
requires `-title` and `-type` (`bug`, `enhancement` or `chore`), and
accepts a repeatable, optional `-dedup <site key>` per dedup term (issue
#3609); `status` takes no flags. The command's exit code is the
acceptance: exit 0 prints a receipt — `signal <kind> accepted: <n>
bytes, <hash>, sequence <n>` — and the signal is taken; a non-zero exit
prints `signal <kind> rejected (<status>): <reason>` and the signal was
NOT taken, so the agent can never mistake a rejection for success. The
socket carries a body whole — no marker line, no base64 — up to a hard
ceiling of 65536 bytes per field (`title`, `body`, `type` and each
`dedupTerms` entry alike); a body over that ceiling is refused outright,
never truncated, so an oversize send costs a retry rather than posting a
truncated verdict.

The prompts instruct the verb under `socket` (issue #3726): prompt assembly
picks each of the three signal fragments — the research verdict comment, the
PR intent, and the Filer's issue intent — from the carrier at run time, so a
socket-mode agent is told to run `driver-exec signal <kind>` with explicit
fields and to read the command's exit code as the acceptance, with no nonce,
no base64 and no "print exactly one line" wording anywhere in its prompt.
Both variants of every fragment bake into the image, so flipping the knob
takes no rebuild. The read-only `gh` shim's refusal messages for `gh pr
create`, `gh issue comment` and `gh issue create` name both routes, and the
in-Box marker gate — the post-driver resume nudge for a `ready` outcome with
no PR intent — asks the socket's status route under `socket` instead of
re-scanning the Box log.

---

## Security

### Secret exposure model

The pieces below — two structural controls, the residual they don't close,
and the strongest posture available today — compose into the
operator-facing secret posture. Together they target a specific threat:
**self-inflicted context contamination** — the
Driver reading a secret into its own transcript, which then leaks out through
a legitimate sink (a per-issue log, a PR body, an issue comment) because
there is no output redaction. None of them claim to defend a compromised Box
against an attacker exfiltrating data over the network — that is a different
threat; see [Threat model](#threat-model) below for what the isolation
boundary does and does not promise.

1. **External-vault sourcing is the preferred, highly encouraged way to
   supply secrets.** `<SECRET>_CMD` / `--<secret>-cmd` (see [Runtime
   configuration](#runtime-configuration)) fetches a secret from an
   operator-controlled command — `rbw`, `op`, `pass`, `vault`, or anything
   that prints a value to stdout — at launch time, and holds it only in
   Launcher memory; the fetched value itself never lands on the host disk.
   The plaintext direct-value and `--<secret>-file` forms remain fully
   supported for operators without a vault and are not deprecated. Once a
   secret has a `_CMD` variant set, `harness.env` is expected to hold a
   fetch *recipe* (a vault item reference) rather than a live credential.
   A locked vault or a pinentry prompt needs somewhere to put its prompt and
   read its unlock input, so this is TTY-gated: when the launcher's own
   stdin and stderr are both real terminals, the secret command inherits
   them as raw file descriptors — no PTY allocation, no stream proxying —
   so a master password flows terminal → vault tool without transiting
   Launcher memory. Stdout is unaffected either way: it is always captured
   as the secret and never printed. Non-interactively (CI, a pipe, a
   redirect), behaviour is unchanged from before this gate existed — stderr
   discarded, stdin not attached, so a vault tool can't block the run
   waiting on input that will never come. A command that still fails —
   non-zero exit, or empty stdout — aborts the launch with a message that
   names the knob, the exit code (non-zero exit only), and a generic,
   tool-agnostic remediation hint ("your vault may be locked; unlock it
   (e.g. `rbw unlock`) and re-run"); the command string, its captured
   stdout, and its captured stderr never appear in that message or in a
   log.
2. **The Box can't read its own credentials.** A `PreToolUse` hook
   (`env-credential-scrub.sh`, issue #1927) rewrites every Bash call to
   `unset` `ANTHROPIC_API_KEY` / `CLAUDE_CODE_OAUTH_TOKEN` before it runs,
   so a spawned subprocess never inherits either credential, and a second
   `PreToolUse` hook (`credential-deny.sh`) denies any `Read`/`Bash` call
   targeting a known credential path. Both are always-on Harness defaults,
   no operator configuration — see [Self-inflicted secret reads are
   structurally
   blocked](#self-inflicted-secret-reads-are-structurally-blocked).
3. **`GH_TOKEN` is the accepted residual.** `GH_TOKEN` is vault-sourceable
   like any other secret (point 1 above), but sourcing only controls how it
   *reaches* the Box — once there, it stays a live environment variable for
   the run's duration, because the Box is a first-class GitHub actor: it runs
   `git push`, `gh pr create`, `gh issue view`/`comment`/`create`/`list`,
   `gh label create`, and CI-log inspection during the fix pass, all
   authenticated by that variable. None of the controls above remove it —
   env-based auth is exactly why `git`/`gh` never need to *read* a credential
   file, but the token itself is still visible to a `Bash(env)` call. **#380**
   (two-actor separation) is the recommended companion: it doesn't remove the
   token from the Box, but it caps the blast radius of a leaked one by
   barring the Box's user from ever updating the base branch — see
   [Two-actor separation](#two-actor-separation-opt-in-hard-mode).
4. **The strongest available posture is zero GitHub token in the Box.**
   `CODE_FORGE=local` + `ISSUE_TRACKER=local` (ADR 0033, ADR 0032) mean the
   Box never receives a GitHub token at all: the repo and issue content
   arrive as read-only mounts, commits leave as a bundle the host relays,
   and every GitHub call happens Launcher-side. Available today for the
   private, fully offline loop — see [Local issue
   tracker](#local-issue-tracker-issue_trackerlocal) and [Local code
   forge](#local-code-forge-code_forgelocal).

### GitHub token permissions

The `agent-dispatch.yml` and `agent-recover.yml` workflows now authenticate via
a short-lived **GitHub App installation token** (see [GitHub App installation
token](#github-app-installation-token-recommended) below) rather than this PAT.
The permission table here is still the canonical list of scopes the agent needs
— the App is granted the same set — and the fine-grained PAT remains in use for
the host-side `spindrift dispatch` CLI and as the research fallback.

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

### Read-only Box token

Under [`BOX_FORGE_AND_ISSUE_ACCESS=read-only`](#read-only-box-box_forge_and_issue_accessread-only)
the Box's `GH_TOKEN` drops to the smallest scope that still lets it clone the
repo and read the issue — it never opens a PR, pushes a branch, or posts a
comment, so it needs none of the write permissions the table above grants:

| permission | level | why                                       |
| ---------- | ----- | ------------------------------------------ |
| Contents   | Read  | clone the repo — never push                |
| Issues     | Read  | read the issue and its comments — never write |
| Metadata   | Read  | mandatory baseline, auto-selected          |

Provision this token (a fine-grained PAT or a GitHub App installation, same
as above) scoped to **only the Target repository**, and hand it to the Box
the same way `BOX_GH_TOKEN` reaches it under [two-actor
separation](#two-actor-separation-opt-in-hard-mode) — the Launcher keeps
using its own, separately-scoped `GH_TOKEN` (the table above) for every
host-side write the Box no longer makes. A fully injection-steered Box
holding only this token cannot open a PR, push a branch, or comment,
regardless of what the prompt tells it to do — the same enforcement-boundary
property the [research token](#research-token-least-privilege-optional)
gets from a Contents-Read-only grant.

### GitHub App installation token (recommended)

`agent-dispatch.yml` and `agent-recover.yml` do not read `SPINDRIFT_GH_TOKEN`.
Each run instead mints a short-lived **GitHub App installation token** with
`actions/create-github-app-token`, feeding the same `gh-token` input the
composite `agent-setup` action already consumed. Nothing downstream changed —
the whole token seam is `gh` reading `GH_TOKEN` from the environment, so this
is a source-only swap at the mint step.

Provision a **worker App** installed on **only the Target repository**, granting
the same scopes the PAT table above lists (Contents RW, Pull requests RW, Issues
RW, Workflows RW — off by default, Checks R, Commit statuses R, Metadata R), and
store its credentials as two repository secrets:

| secret                                     | value                              |
| ------------------------------------------ | ---------------------------------- |
| `SPINDRIFT_AGENT_WORKER_APP_ID`            | the App's numeric App ID           |
| `SPINDRIFT_AGENT_WORKER_APP_PRIVATE_KEY`   | a generated private key (PEM body) |

Why an App instead of the PAT: every App installation gets its **own rate-limit
bucket**, isolated from every personal PAT on the account. The
dispatch / CI-polling / merge burst that was tripping GitHub's 403 / secondary
rate limits now draws on that dedicated bucket. Confirm from a run with `gh api
rate_limit` — the calls should draw down the installation's quota, not the
user's. The single-repo installation is still what bounds
`--dangerously-skip-permissions`: like the PAT, the App can touch nothing but
the one repo it is installed on.

**Token lifetime on long runs.** An installation token expires **~1h after
minting**, which used to mean a run exceeding that window would fail at `gh pr
merge` on a stale token (issue #1027). `agent-dispatch.yml` and
`agent-recover.yml` now pair the mint step with the `gh-token-refresher`
composite action (`.github/actions/gh-token-refresher`): it re-mints a fresh
installation token every 45 minutes for the rest of the job — directly against
the App's installation-token endpoint, signing its own short-lived JWT with the
App private key — and writes it to a file in `$RUNNER_TEMP`, exported as
`GH_TOKEN_REFRESH_FILE`. The private key stays in that backgrounded loop's
memory and a runner-temp file removed when the job ends; only the resulting
~1h tokens ever touch disk, and neither the key nor the launcher's own
`GH_TOKEN` reaches the Box.

The launcher's `--gh-token-refresh-file` flag (`GH_TOKEN_REFRESH_FILE` knob)
points at that file: once set, `bootstrap` starts a background poll
(`cmd/launcher/internal/tokenrefresh`) that re-reads it every 60s for the rest
of the process and swaps any new value into `GH_TOKEN`, so the terminal gh
calls (merge, label edits, the final comment) draw on whatever the refresher
most recently minted rather than the token captured at job start. The knob is
optional and off by default — an unset `GH_TOKEN_REFRESH_FILE` leaves `GH_TOKEN`
static for the whole run, as before (the fine-grained-PAT path, and any
deployment that doesn't wire up a refresher, are unaffected).

**Interval registry.** Two intervals drive the refresher loop, and both are
picked against the ~1h installation-token lifetime it exists to stay ahead of.
The **45m refresh cadence** re-mints roughly a quarter-hour before the live
token would expire — long enough not to hammer the mint endpoint, short enough
to leave slack if one attempt runs slow. The **5m failure backoff** retries a
transient mint failure (a network blip, a transient API error) without waiting
out the full 45m cycle, so a single miss still leaves several more attempts
inside that same hour. `lib/gh-token-intervals.nix` is the one root for both
values, and `nix/checks/gh-token-intervals.nix` pins the `gh-token-refresher`
action's `sleep_secs` literals against it — plus, once the launcher grows its
own App-refresh constants (issue #2867), those too. A bump starts at the
registry; the check fails until the hand-written sites follow.

### Research token (least-privilege, optional)

The research dispatch kind (ADR 0022) authenticates with a second, separately
scoped **GitHub App**, kept disjoint from the work App (a distinct App, or a
distinct installation) so the advise-only scope can never widen to the work
scope. Provision the App with the three permissions below, install it on the
Target repo, and store its App ID and private key as the
`SPINDRIFT_AGENT_RESEARCH_APP_ID` / `SPINDRIFT_AGENT_RESEARCH_APP_PRIVATE_KEY`
repository secrets; `agent-research.yml` mints a short-lived installation token
from them per run (via `actions/create-github-app-token`) and hands it to the
shared `agent-setup` seam:

| permission        | level     | why                                       |
| ----------------- | --------- | ------------------------------------------ |
| Contents          | Read      | clone the repo to review it — never push   |
| Issues            | Read and write | read the issue; write the verdict comment and swap research labels |
| Metadata          | Read      | mandatory baseline, auto-selected          |

This is the enforcement boundary that makes advise-only real: with Contents
read-only, a fully injection-steered researcher cannot push a branch, open a
PR, or merge, regardless of what the prompt tells it to do — the blast radius
collapses to a bad comment a human reads anyway. The installation token is
short-lived (expires ~1h after minting) and draws on the research
installation's own rate-limit bucket, isolated from the work App and any
personal PAT.

The research App is optional. Leave `SPINDRIFT_AGENT_RESEARCH_APP_ID` unset and
`agent-research.yml` skips the mint step and falls back to the
`SPINDRIFT_GH_TOKEN` PAT (Contents/Pull requests/Issues RW + Metadata R, above)
— research still works, but gives up the read-only guarantee: a compromised
researcher could push to a branch or open a PR with the broader token, even
though nothing in the research flow asks it to. Configure the dedicated App
when the blast radius matters more than one extra pair of repo secrets to
manage.

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

The Nix container image the fallback uses is pinned by digest (default — a
pinned copy of the value rooted in `lib/build-constants.nix`:
`docker.io/nixos/nix@sha256:bf1d938835ab96312f098fa6c2e9cab367728e0aad0646ee3e02a787c80d8fb8`).
Digest pinning is a supply-chain safety measure: the container runs with the
consumer's working tree bind-mounted read-write, so an unpinned `:latest` tag
would be a silent code-execution vector. Override with the `nixBuilderImage`
parameter in your `mkHarness` call.

**Bumping the pin:** pull the image you want, inspect its digest, and update
`lib/build-constants.nix`, then both places the digest is copied into this
file — the `nixBuilderImage` row in the [option surface](#option-surface)
table and the pinned reference just above this section:

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
- **`prompts/fix-prompt.md`** — the warm-fix counterpart run on a
  CI-red fix box (`FIX_PASS` set); tune it in step with `issue-prompt.md`'s
  test commands and commit conventions.

---

## Design notes & ADRs

The harness reproduces the part that matters for isolation — *containerize the
runner, launch one box per issue* — and leans on nix for the toolchain instead
of a Dockerfile. The trade-offs:

- **Simpler & fewer deps**: nix + a container runtime + Claude Code. The
  orchestration is a small, nix-built Go binary (`cmd/launcher`, ADR 0007); the
  only bash left is the in-box entrypoint. No orchestration library, no Node
  runtime to import.
- **Cross-issue dependency ordering within a run.** For the `github` tracker,
  the launcher resolves each issue's blockers from GitHub's native
  issue-dependencies relationships first, falling back to parsing
  `depends on #N` / `blocked by #N` (inline or a `## Blocked by` list) from the
  issue body only when the native lookup yields nothing or errors — native
  wins whenever it returns any relationships, body text is never merged in.
  The launcher dispatches in dependency waves from the resolved edges, holding
  a dependent until its blockers reach `agent-complete`; a cycle aborts the
  run. Independent issues
  still run concurrently up to `MAX_PARALLEL`. A declared `## Touches`
  section gets the same wave-and-retry treatment when it overlaps an
  in-progress issue's (`OVERLAP_GATE`, default `defer`) — see [Declared
  touch-set overlap](#declared-touch-set-overlap).
- **Reproducible toolchain by construction** via the pinned flake, rather than a
  floating language-runtime base image.

See [`docs/adr/`](adr/) for the full architectural decision records (0001–0055),
including the Go launcher ([ADR 0007](adr/0007-runtime-logic-is-a-nix-built-go-binary.md)),
the pluggable OCI/bwrap runner ([ADR 0006](adr/0006-box-isolation-is-a-pluggable-runner.md)),
and nix-in-the-box ([ADR 0008](adr/0008-nix-is-a-first-class-default-in-the-box.md)).

An ADR's four-digit number is its identity: no two files under
`docs/adr/` may share one, so "ADR 0045" resolves to exactly one document
and a bare citation in code or prose needs no grep to follow.
`nix/checks/adr-numbers.nix` enforces it at Nix evaluation time —
`adr-numbers-unique` fails on a shared prefix and
`adr-numbers-well-formed` fails on an entry that is not a regular file
named `NNNN-<slug>.md`, without which a badly named ADR would be skipped
by the uniqueness pin's grouping rather than caught by it. Both reach the
gate through `sourceChecks`, so `nix build .#checks-inbox` and `nix flake
check` both carry them. Renumbering an ADR leaves a header note naming its
old number: `CHANGELOG.md` is release-please-generated and is left as
shipped, so its release notes keep citing whatever number the ADR was
published under.

---

## Background realize process isolation

Why does the image-freshness background realize run in a process group of
its own, and why does that make Ctrl-C orphan a `nix build` rather than kill
it? The answer sits in `NixRealizer`
(`cmd/launcher/internal/runner/nixrealize.go`), the launcher's only nix
invocation behind the runner seam. It shells out to `nix build` against a
hermetic `git+file` flake reference pinned to a specific rev — no checkout,
no pull, no working-tree mutation — and the wait function it returns wraps
any non-nil error with that flake reference and the child's bounded stderr.

**The fork/wait split.** `NixRealizer.Start` blocks only long enough to
fork/exec the `nix build` child (`cmd.Start`, never `cmd.Run`); it does not
wait for the build itself to finish. The returned wait function does that
waiting, separately, whenever the caller chooses to call it. The split is
the seam's requirement rather than this implementation's choice — see
`freshness.Realizer`'s own doc comment for why it is required to avoid
racing `os.Exit`. A realize surviving the launcher's own exit falls straight
out of this split: the child keeps running in the background regardless of
what happens to the launcher process afterward. That survival is the premise
[ADR 0019](adr/0019-dispatch-exits-at-the-wave-boundary.md) relies on —
dispatch can exit at a wave boundary while a background realize keeps the
single-user nix store's lock held for the build's full duration, on a
lifetime independent of the launcher process's own.

**Setpgid.** `Start` places the `nix build` child in its own process group
(`syscall.SysProcAttr{Setpgid: true}`) rather than letting it inherit the
launcher's. This is a narrower, separate concern from the fork/wait split
above: it isolates the child from signals delivered to the launcher's
process group for the child's *whole* lifetime, including while the
launcher is still running, not only after the launcher has already exited.
Without it, any signal delivered to the launcher's process group — after
the launcher has already exited (container/session teardown, a shell
delivering job-control signals to its foreground process group, ...), or at
the very moment such a signal is what's terminating the launcher — would
reach the still-running `nix build` child too and kill it mid-build, even
though the whole point of starting it in the background is for it to
outlive the launcher's own exit (again, the case ADR 0019 relies on).

**Why it can't be scoped away.** Setpgid is set once, at fork time, before
it's known what will eventually terminate the launcher — there is no later
point at which the code could decide "this termination is the ordinary
post-exit case, so isolate" versus "this termination is something else, so
don't." That single fork-time decision has to cover every subsequent
termination path uniformly.

**In practice, not just in theory.** `freshness.RealizeTip` fires
synchronously from the `fresh` closure inside `waves.RunContinuous` (the
closure itself is defined in `main.go`), so the launcher is usually still
alive, mid-wave, when Ctrl-C lands — the SIGINT is what terminates the
launcher, not something arriving after it's already gone. (The stale-image
path is the exception: `main.go` can exit on `ErrImageStale` within seconds
of a multi-minute realize starting, in `waves/continuous.go`, putting a
later Ctrl-C in the ordinary post-exit case instead.)

**The accepted cost: Ctrl-C.** A bare Ctrl-C at a terminal running the
launcher directly — as opposed to the two-stage signalled-stop latch a
driving loop or the daemon relies on (see [Dispatch exit
codes](#dispatch-exit-codes)) — is a hard abort: the shell's own job
control sends SIGINT to the whole foreground process group. Because
Setpgid already detached the `nix build` child from that group at fork
time, the SIGINT does not reach it: the child survives orphaned, still
holding the single-user nix store lock, rather than dying alongside the
launcher and the rest of the foreground group. This is accepted, not fixed,
because the very same fork-time Setpgid is what protects the ordinary
post-exit case `NixRealizer` exists for, and it can't be scoped to spare
Ctrl-C specifically, for the fork-time reason given above.

**A mitigation considered, not wired up.** A later, signal-delivery-time
fix could still forward the launcher's own SIGINT into the child's group
with `syscall.Kill(-pgid, syscall.SIGINT)` before the launcher exits. That
isn't wired up today: `Start`'s `(func() error, error)` return signature
exposes no pid or pgid to forward against, so this would need a seam change
first. It also has no home to hang off yet — no `cmd/launcher` source
installs a SIGINT handler for this path today; the only `signal.Notify`
call in the tree, in `quickstart/maskedinput.go`, handles unrelated masked
input, and the Console TUI's bubbletea run loop installs its own, separate
SIGINT handling. So the orphaned-build cost of the Ctrl-C case is a
deliberate, currently-unmitigated cost, not one that's impossible to avoid.

---

## Review-prompt tuning measurements

[`docs/measurements/`](measurements/) holds before/after writeups for
changes that calibrate the reviewer's own rubric, e.g. [issue #2696's
coverage-severity calibration](measurements/2696-coverage-severity.md)
and [issue #3611's round-2 escalation flip
recalibration](measurements/3611-round-2-escalation-flip.md).

---

## Research dispatch

`spindrift research` (and the selective `research <nums>` form, mirroring
`dispatch <nums>`) is a second, advise-only Dispatch kind (ADR 0022): each
container reviews one posted issue from inside a fresh clone of the Target
repo, then posts a single structured comment carrying a verdict — it never
edits the issue body, never closes it, and never promotes it to
`ready-for-agent`, with one narrow exception: filing a finding worth tracking
through the Filer, which stays relay-only and host-mediated in every mode
(see [Filer](#filer)), so the Box itself never writes to the tracker even for
this. A human always acts on the verdict.

Research shares the launcher's four canonical Dispatch states with `dispatch`,
but maps them to its own disjoint `github` label family — claiming an issue
never touches `ready-for-agent`/`agent-in-progress`/`agent-complete`, so an
issue can legitimately wear both a work label and a research label at once:

| label | meaning |
|-------|---------|
| `agent-research` | dual-role: standing state and trigger — apply it to fire a research dispatch |
| `agent-research-in-progress` | a Box is reviewing the issue |
| `agent-research-recommend` | relevant and enriched — promote it |
| `agent-research-reject` | false positive, not worth doing, or a duplicate (named in the comment) — applying it closes the issue as not planned, automatically (see [Closing a rejected research issue](#closing-a-rejected-research-issue)) |
| `agent-research-unclear` | relevance needs an answer only a human has — answer, then re-apply `agent-research` |
| `agent-research-failed` | the Box crashed or produced no verdict — a human triage queue, distinct from `agent-research-reject` (a *successful* "this is a false positive" conclusion is `Complete`, never `Failed`) |
| `agent-research-finding` | filed by the Filer from a research finding (ADR 0041) — never carries a dispatch label; a human promotes it to `ready-for-agent` like any other issue |

Settle is strictly one-shot: parse the Outcome line, apply exactly one
terminal label, done — no CI watch, no self-heal fix passes, no merge, since
research never lands code. Retry is the same gesture as `dispatch`:
re-applying `agent-research`. Research dispatches also ignore blocker edges
entirely (enriching an issue is useful *especially* while it waits on a
blocker) and are homogeneous in kind — `research` and `dispatch` never mix
issues within one invocation. See the **Dispatch kind** / **Research
dispatch** glossary entries in [`CONTEXT.md`](../CONTEXT.md) for the full
vocabulary.

On GitHub, `.github/workflows/agent-research.yml` mirrors `agent-dispatch.yml`:
applying `agent-research` to an issue fires exactly one research dispatch,
claiming the issue (only the research-family labels above — a work lifecycle
label like `ready-for-agent` survives the claim untouched), building, then
running `spindrift research` against that issue. It serializes per
`agent-research-<issue-number>`, so re-labeling the same issue queues behind
itself but a research run on an issue never queues behind (or blocks) a work
run on the same issue. It takes an optional second least-privilege token — see
[Research token](#research-token-least-privilege-optional) for the scopes and
what the fallback gives up. Labels must exist on the Target repo before first
use — see [Create the research labels](#create-the-research-labels-on-the-target-repo).

### Closing a rejected research issue

`agent-research-reject` is the one verdict with a mechanical next step, and
`.github/workflows/agent-research-close.yml` takes it: applying the label
closes the issue **as not planned** — never as completed, since research lands
no code and nothing was built. Nothing else about the verdict changes; the
label stays on the issue as the record of why it closed, and the researcher's
verdict comment stays as the record of what it found.

Automating the close is not tidiness. The Filer treats a *closed* issue
carrying `agent-research-reject` as a suppressing match and never refiles a
finding that matches one (see [Filer](#filer)), so while the close depended on
a human remembering, every still-open rejection left the same false positive
free to be filed again.

The workflow is deliberately thin: one `gh issue close --reason "not planned"`
on the built-in `GITHUB_TOKEN`, with `issues: write` and nothing else. It
claims no issue, builds no image, and needs neither the worker App nor
`SPINDRIFT_GH_TOKEN` — and because `GITHUB_TOKEN` events never trigger further
workflow runs, it cannot loop. Re-applying the label to an already-closed issue
is a no-op, not a reopen-and-reclose. The trust boundary is the same one the
rest of the control plane uses: the label, not the labeler — whoever can apply
`agent-research-reject` (a triage-role human, or the research App token writing
the verdict) gets the close.

`.forgejo/workflows/agent-research-close.yml` is the Forgejo Actions mirror,
closing over REST through the `forgejo-issue-close` composite action on the
`FORGEJO_TOKEN` secret. One platform difference: Forgejo issues have no
`state_reason`, so the close cannot be marked *not planned* there — the label
left on the issue carries that meaning instead.

### Self-contained research mode

`spindrift research --self-contained` (issue #2202) is a sub-mode of the
research kind for issues that are already self-contained — everything
needed to judge and enrich them lives in the issue body and its comments,
with no repository to read against. The flag clones no repo and runs none of
the ordinary research pass's repo-exploration steps: bootstrap skips
`clone_repo`, branch recovery, the toolchain nudge, the devShell probe, and
prefetch entirely, standing up only an empty working directory before
prompt assembly. Because there's nothing to clone, this mode also needs
neither `REPO_SLUG` nor `GH_TOKEN` — startup validation relaxes the
otherwise-unconditional requirement for both when `--self-contained` is
paired with the research kind, so the natural pairing is a local issue
tracker (`ISSUE_TRACKER=local`) supplying the self-contained content, with
no forge repo configured at all.

The Box is driven by a distinct baked prompt,
`research-self-contained-prompt.md`, rather than the ordinary
`research-prompt.md` — it drops the EXPLORE-the-repo step and judges
relevance from the issue content alone. Like the ordinary research prompt,
it's overridable at runtime via the same `SPINDRIFT_PROMPT_DIR` /
`--prompt-dir` prompt-directory override (issue #2200) for zero-rebuild
iteration — a custom prompt directory ships
`research-self-contained-prompt.md` alongside `research-prompt.md` to
override both. As with the ordinary research prompt, an unmodified copy of
the checked-in template ships its two verdict injection markers unresolved
under this override — see the caveat under [Configuring the research
verdict vocabulary](#configuring-the-research-verdict-vocabulary-research_verdicts).
Its machine-checkable verdict contract otherwise renders
from the same [`RESEARCH_VERDICTS`](#configuring-the-research-verdict-vocabulary-research_verdicts)
configured set as the ordinary prompt (issue #2201), so a custom vocabulary
reaches both. Settle is unchanged: self-contained research still posts
exactly one required verdict comment through the same configurable-verdict
path and applies exactly one terminal label — no repo means no code to
explore, not a different verdict contract or a looser one-comment rule.

`--self-contained` is meaningful only on the `research` subcommand;
`dispatch` and `recover` reject it outright rather than silently ignoring
it, since a no-repo work dispatch would have nothing to branch from or land
a PR onto. See [ADR 0022's amendment for issue
#2202](adr/0022-research-is-a-dispatch-kind.md#amendment-issue-2202-a-self-contained-no-repo-research-sub-mode)
for the full rationale.

## Registry route discovery

`spindrift registry discover <repo-dir> <routes-file>` (ADR 0045) writes a
registry routes file without hand-transcription: it scans the Target repo's
own committed registry config — `.cargo/config.toml`, `.npmrc`,
`.yarnrc.yml`, `pnpm-workspace.yaml` — for declared hosts, base URLs, and
cargo registry names, then matches each host to a credential across the
operator's own credential stores, searched **in this order**: `netrc`,
`npmrc`, `cargo-credentials`, `gradle-properties`. A host that carries more
than one registry — say, one Artifactory instance serving both an internal
cargo mirror and a crates.io passthrough — still collapses to a single
route, since matching is host-keyed. It stops at the first store that has
a credential for that host, then probes the upstream base URL and reads
its `WWW-Authenticate` response header to guess the auth scheme (`bearer`
or `basic`, defaulting to `bearer` when the registry is unreachable or
answers with neither). It's a setup-time, operator-run tool, never invoked
from inside the Box — a Box-influenced route would let an Agent steer a
real credential at a host of its choosing. Once a route is configured,
how a proxied dispatch actually reaches the Box over it — socket or
loopback TCP — is a separate, cached decision; see [Registry transport
probe cache](#registry-transport-probe-cache).

The routes file it writes is references-only, matching every other
Registry route input (see the **Credential reference** glossary entry in
[`CONTEXT.md`](../CONTEXT.md)): each route's `credential` table names a
store path (plus, for `cargo-credentials`, the registry name, or for
`gradle-properties`, the property key) rather than the credential value
itself. A written route otherwise carries only `match-host` and
`auth-scheme` — the base URLs and cargo registry names discovery scanned
out of the repo's config drive the auth-scheme probe and the credential
match, not the file's contents, so neither a base URL nor any path ever
lands in it. `upstream-origin` is the one exception, and only appears
when the discovered URL's scheme or port is something `match-host` alone
can't imply (ADR 0047, issue #3261). A route's per-ecosystem declaration —
a `[routes.ecosystems.<name>]` sub-table carrying gradle's or go's typed
`path` key, or cargo's `registries` list (ADR 0048, issue #3405) — is a
surface discovery never writes at all, even where it already scanned the
value: the cargo registry names it read to drive the credential match
above go no further than that match, and Gradle and Go name no registry
host in-tree for it to scan a path out of in the first place. An operator
declares such a block by hand when a route needs one. `<repo-dir>` is the
Target repo to scan; `<routes-file>` is the path to write. It refuses to
overwrite an existing `<routes-file>`; pass `--force` to overwrite anyway.

A host discovery can't match to any store still gets a route, pointed at a
placeholder credential named `SPINDRIFT_REGISTRY_CREDENTIAL_<HOST>` (host
uppercased, non-alphanumeric characters replaced with `_`) — the route
does nothing until the operator sets that environment variable or edits
the route by hand; the command's report and the routes file's own header
comment both flag every route left in this state. Two unmatched hosts
that sanitize to the same placeholder name (e.g. `a.b.example.com` and
`a-b.example.com`, both folding their separator to `_`) would otherwise
share one env var — an operator's value for one host silently also
reaching the other — so each round re-buckets the unmatched routes by
their current placeholder name and appends `_<8 hex digits>` of the host's
own hash to every route still contested in that round. A name contested
again in a later round, because its new suffix collided with some other
route's plain or already-suffixed name, picks up a second suffix — three
hosts colliding in a chain can leave one placeholder carrying two. The
check runs against the whole routes table, not just the colliding pair, and
repeats until no two routes share a name; the outcome is a deterministic
function of the discovered host set, stable across repeated discovery
runs and independent of the order the hosts were declared in. The loop is
bounded, and hitting that bound — only reachable if two hosts' hashes
are equal — makes `spindrift registry discover` fail with an error
naming the still-contested placeholder rather than write a routes file
where two hosts share one env var. A repo with no registry declarations
at all — none of the scanned config files, or only config declaring
non-http/unusable registry URLs — writes nothing and exits non-zero,
since that's indistinguishable from a mistyped `<repo-dir>`.

A routes file can drift from the repo's own config after either changes —
a new registry the repo starts using, or a route left over from one it
dropped. `spindrift doctor` re-runs the same discovery engine in check
mode and reports any host the repo names that no route covers, as an
advisory `registry-route-drift` row (ADR 0045) — host coverage is the
whole signal: a route covers a host outright, whatever paths the repo
declares under it, so paths are not a drift category at all. The remedy
is adding routes for those hosts by hand, or regenerating the whole file
with `spindrift registry discover --force`, which discards hand edits.

## Registry transport probe cache

Deciding how a proxied dispatch reaches a Box's registry proxy — over a unix
socket, or falling back to loopback TCP — takes a live probe against the
configured container runtime (ADR 0045, issue #3111): a throwaway container
mounts the socket and reports whether it can see it, and on a
socket-incapable host (the macOS case, where the runtime runs inside a VM)
a second live sub-probe confirms the TCP fallback's own `--add-host`
host-gateway route actually works before it's trusted, at up to **three**
throwaway containers per proxied dispatch — **four** when the socket probe
comes back with no verdict and the control probe below runs. The verdict
changes only when the operator's runtime configuration changes, not on
every dispatch, so `RegistryProxyTransport` now measures it once and
remembers it (issue #3113) rather than re-probing on every run.

A socket probe that comes back with no verdict — not a clean capable
(exit 90) or incapable (exit 91) exit, but a runtime exit like 125, a
plain exit 0, a timeout, or the runtime binary never starting — doesn't
fail the dispatch outright. It triggers one further *control* probe: the
identical throwaway container and `probe-registry-socket` verb, run again
with nothing mounted at the socket's container path. With nothing to see,
the verb can only report incapable, so a clean control exit of 91 means
the image and runtime are healthy and it was the socket mount itself the
runtime rejected — read exactly like a direct incapable verdict, falling
through to the same TCP reachability sub-probe and `NETWORK_MODE`
host-loopback deny check any other socket-incapable host takes. If the
control probe also comes back with no verdict, that's a genuine
infrastructure failure — a missing image, a launcher/image version
mismatch, or a runtime daemon that's simply not up — and the dispatch
hard-errors, naming both exit codes. The control probe costs one extra
container, but only on this no-verdict path, and the TCP-vs-socket
decision it lands on is cached like any other verdict — a probe error
itself is still never cached (see below). A socket probe that *times out*
counts as no verdict too, so a wedged daemon pays the probe timeout twice
before the dispatch aborts.

The concrete case this exists for: on macOS, Rancher Desktop shares the
per-user `$TMPDIR` (`/var/folders/...`) into its VM over virtiofs. A
probe socket minted there can't be used as a bind mount source; the
daemon falls back to creating the mount source itself, and that `mkdir`
fails with `operation not supported` — `docker run` exits 125 before
the probe container ever starts (see [ADR 0044's amendment for issue
#3467](adr/0044-private-registry-credentials-live-in-a-launcher-side-proxy.md#amendment-issue-3467-the-socket-measurement-was-taken-on-an-unshared-path)
for what was and wasn't measured). Without the control probe that
reads as an unrecoverable failure and aborts the dispatch; with it,
the dispatch falls through cleanly to the TCP transport instead.

The remembered decision lives in one file,
`<working-dir>/.spindrift/registry-probe-cache.json`, written only when a
registry proxy is actually configured — a dispatch with no
`REGISTRY_PROXY_ROUTES_FILE` routes leaves no trace. It remembers the whole
transport decision, not just the socket-vs-TCP verdict: the transport kind,
the TCP host, and whether the TCP arm needs `--add-host` all travel
together, since a cache that replayed only the socket verdict would leave
the `--add-host` mode to guess.

A stored verdict is keyed on the container runtime (`RUNTIME` /
`perSystem.spindrift.infra.runtime`), the image reference, and
`NETWORK_MODE`, and a live value that disagrees with any of the three is a
miss, not a stale hit. The image reference belongs in the key for a subtle
reason: an image too old to carry the `probe-registry-socket` verb reads
back from the probe as socket-incapable — indistinguishable, from the
probe's own exit code, from a genuinely socket-incapable host — and caching
that misread would let a stale image masquerade as a permanent host
limitation long after a rebuild would have fixed it (issue #3120). That
protection only covers the reference, though, not the image's actual
content: the nix pipeline's `imageName:imageHash` tag changes on every
rebuild and so self-invalidates, but a mutable tag — the bare-launcher
`spindrift:latest` default, or an `IMAGE` override pinned to a moving tag —
does not, so a rebuild under one of those is another case the `rm
.spindrift/registry-probe-cache.json` escape hatch below covers.
`NETWORK_MODE` is keyed too: the socket-incapable path hard-errors rather
than falling back to TCP under a network mode that denies the host-loopback
route the TCP arm needs, so a verdict cached under one mode must never
replay under another.

That key can't see everything, though. Changing a VM-backed runtime's mount
type — Docker Desktop, Rancher Desktop, or Lima file sharing — changes
whether the socket can cross into the Box without touching the runtime
binary, the image, or `NETWORK_MODE`, so the cache has no way to notice on
its own. For that case, and any other change the key can't see, the
operator's escape hatch is one command: `rm
.spindrift/registry-probe-cache.json`. The next proxied dispatch finds no
file, probes live, and writes a fresh verdict — there is no flag or env var
for this, deleting the file is the whole mechanism.

The cache degrades safely on its own. A missing, truncated, corrupt, or
otherwise unreadable cache file is treated as a miss, not an error — the
dispatch falls back to probing live and, on success, overwrites the file,
the same corruption-tolerant idiom the freshness-guard state file uses. A
probe that itself fails is never cached, either: a transient infrastructure
hiccup is not a verdict, and remembering one would make it permanent.

## Dispatch exit codes

`spindrift dispatch`'s own exit codes are what both continuous dispatch
(below) and the daemon (see [Daemon](#daemon)) interpret to decide what
happens next:

| exit | meaning |
|------|---------|
| 0    | dispatched work |
| 2    | queue empty (no open issues with the dispatch label) |
| 3    | open issues exist but none are dispatchable |
| 4    | `CONTINUOUS_DISPATCH` mode: the freshness probe found the loaded host launcher is stale relative to the flake's launcher-currency attr (or, under the OCI runtime, that the loaded image would also be rebuilt against the current base-branch tip); in-flight Boxes finished, no new ones launched |
| 5    | the loaded image is stale in a way no rebuild converges (host-tainted: a host-system derivation reached the image graph, so the same base tip stays stale after a rebuild against it) |
| 6    | bootstrap rejected the config (the launcher's `exitConfigInvalid`) — e.g. a missing required setting, or flags that cannot combine |
| 7    | an operator asked the run to stop — the one-shot dispatch wave, selective dispatch, research, and the `recover` gate all share this same two-stage latch, not just `CONTINUOUS_DISPATCH` mode. On one signal the launcher stopped claiming new issues, let in-flight Boxes finish and settle, ran its normal teardown, and exited; on a second signal it instead reaped every in-flight Box and released their issues back to the dispatchable pool before exiting. Both are the same code deliberately — each means "you asked me to stop" |

Exit 4's own rebuild cost differs by which dimension is stale. When the image
dimension is stale, the launcher has already kicked a background `nix build`
of the stale tip (`freshness.RealizeTip`) during the drain itself, so
whatever builds that tip next — the daemon's following iteration, or an
operator re-running the dispatch — is usually a cache hit off that build. A
launcher-only-stale verdict has no tip to realize, since the launcher cannot
rebuild itself in place, so that build is always cold.

Under the bwrap runtime, a verdict where only the agent-closure image
dimension is stale no longer reaches exit 4 at all: the launcher
hot-swaps the realized closure in place and keeps refilling instead of
draining (ADR 0043, issue #2682). The agent-closure dimension covers the
agent files, the agent env, the in-box nix config, and the configured
`prefetch` snippet, so a merge that changes only `prefetch` is a real
staleness signal under bwrap rather than a no-op, and the hot-swap hands
every Box launched afterward the new snippet instead of the one baked at
launcher startup (issue #2954). Exit 4 under bwrap now fires only when the
launcher dimension itself is stale (alone, or alongside the image) — a
process cannot swap itself, so that case still drains and exits exactly as
before. The OCI runtime is unaffected: it never swaps, so any stale
dimension there still reaches this exit the way the table above describes.

This exit-4 "stale drain" is a distinct concept from the `MAX_JOBS` refill
drain: the stale drain is `CONTINUOUS_DISPATCH` pausing new dispatch while
the image or launcher is rebuilt, not the wave engine finishing its
`MAX_JOBS`-bounded batch. The headless launcher also appends a summary line
to `.spindrift/logs/stale-drain.log` — drain duration, free-slot-seconds
accumulated while refilling was stopped, and how many otherwise-ready issues
the drain itself held back from launching (not the full unclaimed backlog —
see `heldBack`'s own doc comment in `stale_drain_report.go` for exactly
which categories count) — so a driving loop can total those numbers across
iterations to judge whether the drain is a rounding error or the dominant
stall (#2678). Each line is space-delimited `key=value` pairs prefixed
`STALE_DRAIN `:
`durationSeconds`, `freeSlotSeconds`, and `heldBack` are the three fields
worth grepping and summing. `heldBack` can be the literal string `unknown`
instead of a number — a transient tracker error at the moment the drain
started, not a confirmed zero — so a naive summing script must skip or
special-case that value rather than parse it as an integer.

A signalled stop (exit 7) is a drain too, but not a stale drain — unless a
stale drain was already under way when the signal arrived, in which case it
still finishes and reports its `STALE_DRAIN` line before the run exits 7;
absent that, no `STALE_DRAIN` line is written. This two-stage behavior belongs
to the launcher process, not to `CONTINUOUS_DISPATCH` alone: the one-shot
dispatch wave, selective dispatch, research, and the `recover` gate all share
it. The first signal stops the launcher claiming anything new and waits on the
Boxes already in flight — including each one's settle, the merge gate's CI
watch, which a graceful drain deliberately does not interrupt: interrupting it
would throw away a landing that is one poll from done. So the real bound on
stopping is not the slowest in-flight Box's runtime alone but that runtime
*plus* its whole settle, and a settle is not a single CI wait. Every CI wait
inside a settle takes a *fresh* `MERGE_POLL_TIMEOUT` deadline (default `3600`
seconds — see [Advanced tuning](#advanced-tuning)), and one settle can
enter several: one wait per self-heal attempt, so `MAX_FIX_ATTEMPTS`
(default `3`) red gates cost four waits in all, plus one more after every
force-push, since a rebase resets the PR's required checks and the
launcher re-waits for green on the new head — once per
`MAX_REBASE_ATTEMPTS` (default `3`) conflict pass, and once more when
`PREFLIGHT_STALE_BASE` is on. The ceiling is therefore
`MAX_FIX_ATTEMPTS + MAX_REBASE_ATTEMPTS + 1` poll windows: seven at the
defaults, about seven hours of polling alone, or eight windows with the
stale-base preflight enabled. Each red gate and each conflict a rebase
cannot resolve also dispatches its own Box — up to three fix Boxes and up
to three conflict-resolve Boxes, or four with the stale-base preflight
enabled, which keeps its own retry budget — and those runtimes land on top
of the polling. The floor is the other end of the same range: a settle
that confirms green on its first poll and merges adds only seconds to the
Box's runtime. The one-shot wave has no refill loop to stop — its batch is
fixed up front — so there the first signal simply means launch nothing
further from that batch while whatever is already running finishes and
settles. The launcher prints a line when a drain begins, so an operator can
tell a drain from a hang. Teardown runs on every signalled path, and behavior
is identical under the OCI and bwrap runtimes — nothing in the signal path
branches on runtime.

A **second** signal abandons that wait: the launcher reaps every in-flight Box
and releases each of their issues off the in-progress label back to
dispatchable so they return to the pool without a human re-label, then exits 7
just as the drain does. Research releases its own label family rather than the
work family's — its abort moves an issue off `agent-research-in-progress`
back to `agent-research`, since its `forge.IssueTracker` is built from the
research label family (ADR 0022). The `recover` gate honours both stages too:
a first signal that arrives before it has adopted anything exits 7 without
parking the issue `agent-failed`, and a second signal during its CI watch
reaps and releases exactly as the wave case does. The kind of signal does not
matter — only first versus second. `SIGTERM` and `SIGINT` are both caught
and are interchangeable, so `TERM`+`TERM`, `TERM`+`INT`, `INT`+`INT`, and
`INT`+`TERM` all escalate on the second; a lone `SIGINT` drains exactly as a
lone `SIGTERM` does rather than aborting, which would make Ctrl-C more
destructive than `systemctl stop`. This is the Ctrl-C-once-is-polite,
Ctrl-C-twice-means-it convention `docker` and `kubectl` already follow.
Escalation is edge-triggered and happens exactly once, so the third and later
signals are no-ops and mashing Ctrl-C cannot race the teardown it triggered.
The abort prints its own line naming how many Boxes it is terminating, so an
abort is distinguishable from a drain and from a hang. An aborted issue is
released, never marked complete or failed, so `spindrift reconcile` never
mistakes abandoned work for finished work, and a settle goroutine that
outlives the abort abandons at its next checkpoint rather than driving the
issue to a terminal state behind it. `SIGKILL` remains uncatchable and abrupt
— the last resort no code path can intercept.

## Continuous dispatch

Setting `CONTINUOUS_DISPATCH=1` turns dispatch into a long-lived slot-refill
loop: instead of draining one bounded batch and returning, the launcher
keeps running — as each Box finishes, it re-discovers the queue and refills
the freed slot immediately, re-applying blocker readiness and the Touches
overlap gate — gated by the image-freshness probe before every launch.
Leave `CONTINUOUS_DISPATCH=` (empty, the default) for the older
one-wave-and-exit shape.

The freshness boundary is no longer every iteration: a refill launches
straight onto the already-loaded image so long as it's still fresh, and a
driving loop only needs to pull and rebuild when the launcher reports the
image has actually gone stale (build is a no-op unless the merged diff
changed the image hash) — see exit 4 in [Dispatch exit
codes](#dispatch-exit-codes) above.

**Deprecated.** Continuous dispatch is superseded by the daemon
(`apps.daemon`, `nix run .#daemon` — see [Daemon](#daemon)), which holds the
pool a different way: one single-Box launcher invocation per slot, each
pinned to its own fetched revision, rather than one long-lived launcher
process doing all the pool-holding itself. Freshness therefore stops being
something one process must orchestrate across its whole pool lifetime —
the very job the image-freshness probe, the hot-swap, and the stale-drain
exit documented in [Dispatch exit codes](#dispatch-exit-codes) above exist
to do. It is **not removed**: the knob still works, it stays available for
operators who want no daemon at all, and it remains the Console's engine
unchanged (see [docs/console.md](console.md)). See issue #3547.

See `lib/env-schema.nix`'s `continuousDispatch` entry for the full
behavior. On the command line `--continuous-dispatch` is a boolean
flag: bare `--continuous-dispatch` — or its `--continuous` alias — turns
the loop on, and `--continuous-dispatch=0` turns it off; both `spindrift
dispatch` and `spindrift research` accept either form.

## Podman machine RAM

`spindrift doctor`'s `podman-machine-memory` check (Required tier, reported
for every non-bwrap runner kind) fails when the active podman machine's RAM
is smaller than `MEMORY_LIMIT` × `MAX_PARALLEL` (plus a fixed 512MiB VM
overhead): that mismatch lets the VM's own OOM-killer kill an in-box build
— or, once enough boxes run concurrently, the whole VM — before any single
container's `--memory` cap ever bites. The check reads `podman machine
inspect`, compares its `Resources.Memory` (MiB) against the required total,
and on shortfall prints the machine RAM, the required RAM, and a fix
(`podman machine set --memory <N>`, lower `MAX_PARALLEL`, or lower
`MEMORY_LIMIT`). It's a no-op with adequate machine RAM, and reports `not
applicable` rather than a failure for a non-podman runtime, an empty
`MEMORY_LIMIT` (the schema's deliberate opt-out), or no active podman
machine — native Linux, or bwrap, which is daemonless and has no VM for the
check to inspect. With the defaults (`MEMORY_LIMIT=5g`, `MAX_PARALLEL=3`)
the computed minimum is 15872MiB, and that's the exact `--memory` value the
fix-hint prints — copy it verbatim rather than a rounded 16384 (16GiB): a
custom `MEMORY_LIMIT`/`MAX_PARALLEL` computes a different minimum than the
shipped defaults, so only the fix-hint's own number is guaranteed correct
for your config (issue #3537, issue #3544).

## Dogfood Consumer config

**Subagent roster.** The dogfood Consumer config's subagent models and
efforts, and its orchestrator review effort, are all set via `roster` in
`nix/dogfood-defaults.nix` — see [Subagent roster](#subagent-roster) for the
mechanism and dogfood's specific values.

For one-shot bwrap runs, `nix develop .#bwrap` (Linux-only, same guard as
`apps.dogfood-bwrap`) puts the bwrap-baked `spindrift` CLI on PATH together
with the host binaries the launcher execs from ambient PATH — `bwrap` and
`pasta` (issue #2666) — so
`spindrift build && spindrift dispatch <issue> --yes` works directly, with
no `nix run` prefix and no reliance on the default dev shell's toolchain.

**The bwrap twin.** `apps.dogfood-bwrap` is the A/B twin of `apps.default`
(issue #2672): the same tuned dogfood config through the daemonless bwrap
runner, overriding `runtime`, `defaults.daemonApp`, and
`defaults.daemonSelfApp`. It is built from `fixtures.dogfoodBwrapHarness`,
which reuses `nix/fixtures.nix`'s `dogfoodHarnessArgs`. `apps.default` is not:
it comes from `flake.nix`'s `spindrift = { ... }` module config, which
`dogfoodHarnessArgs` mirrors as a direct call (`fixtures.harness`) for
`flakemodule-equivalence` to compare against. A new dogfood knob therefore has
to be wired on both sides — into the module config and into
`dogfoodHarnessArgs`, off their shared leaf values in
`nix/dogfood-defaults.nix`; wiring it only into `dogfoodHarnessArgs` leaves
`apps.default` unchanged, and turns `flakemodule-equivalence` red whenever the
knob reaches the `spindrift` CLI that check compares.
`dogfood-bwrap-app-wiring` in `nix/checks/equivalence.nix` pins the bwrap
side's flake wiring: `apps.dogfood-bwrap` must exist, must be a real app, and
must carry the same `program` as `fixtures.dogfoodBwrapHarness.apps.default`,
and the top-level `packages.agent-closure` must be that same harness's (where
the freshness Probe resolves it). No check proves `apps.default` and
`apps.dogfood-bwrap` identical — they deliberately are not.
`apps.dogfood-bwrap` exists only on Linux, where
`fixtures.dogfoodBwrapHarness`'s `packages` has an `agent-closure` to build.

**Baked skills.** The dogfood Box bakes nine skills — eight pinned upstream,
one authored in this repo — via the Consumer-configured `skills` list (see
the `skills` row and `skillsDirRelative` entry above for the build-time
`/agent/skills` path and the runtime copy into the Driver's actual skills
dir), each as a `<name>/SKILL.md` directory — the only layout Claude Code
discovers, so a flat `<name>.md` file is silently ignored — so the in-box
agent can invoke them as slash commands:

- [`caveman`](https://github.com/juliusbrussee/caveman) — `/caveman`. The
  rendered issue-pass and fix-pass prompts direct the agent to default to it
  for narration and prose, compressing narration ~65% in output tokens
  without touching code, commands, error messages, commit messages, or the
  machine-parsed marker grammar (the outcome line and its `note=` field, the
  verdict line, and host-relay signal lines).
- [`tdd`](https://github.com/mattpocock/skills) and
  [`to-tickets`](https://github.com/mattpocock/skills) — `/tdd`,
  `/to-tickets` (pinned at tag `v1.1.0`). The IMPLEMENT section defers its
  test-first workflow to `/tdd` when baked.
- [`commit`](https://github.com/jordansmall/skills) — `/commit`. The COMMIT
  section defers commit-message formatting to `/commit` when baked.
- [`code-review`](https://github.com/mattpocock/skills) — `/code-review`
  (pinned at tag `v1.1.0`, the same upstream as `/tdd`/`/to-tickets`). Reviews
  a diff along Standards and Spec axes in parallel sub-agents.
- [`principle-fix-root-causes`](https://github.com/jordansmall/skills),
  [`principle-laziness-protocol`](https://github.com/jordansmall/skills), and
  [`principle-redesign-from-first-principles`](https://github.com/jordansmall/skills)
  — three engineering principles adapted from the MIT
  [`pstack`](https://github.com/cursor/plugins/tree/main/pstack) plugin. The
  fix pass and the worker path's CHECK section defer to
  `/principle-fix-root-causes` before code changes that make a failing check
  pass; IMPLEMENT defers to the other two. `/principle-laziness-protocol` and
  `/principle-redesign-from-first-principles` are deliberately absent from the
  fix pass, which already scopes itself to the smallest change and forbids
  redesign.
- `nix-checks` — `/nix-checks`. Not pinned upstream: authored in this repo at
  `skills/nix-checks/SKILL.md` and read straight from the repo tree, the same
  way the other rows read from a pinned flake input. Carries the Nix check
  discipline (scoped check target, devShell preference, git-add-before-build)
  that used to sit inline in the CHECK section — see
  [MIGRATING](../MIGRATING.md) (issue #3223).

Beyond the generic "skills available, prefer them" preamble, each of these
skills gets a deferral placed at the exact prompt section its inline guidance
would otherwise duplicate, gated on that skill being baked. The eight pinned
skills are non-flake `caveman` / `matt-skills` / `jordan-skills` inputs in
`flake.nix` (`flake.lock` owns the revs); `nix-checks` is a repo path instead
of a flake input. The full baked set — pinned and repo-local alike — lives in
`nix/dogfood-skills.nix`. See [Contributing](../CONTRIBUTING.md) for how it's
wired. To opt out of a skill, drop it from the consumer's `skills` list; each
of the nine Consumer-configured deferrals above is rendered only when that
skill's `SKILL.md` is actually present at the baked skills path, so a
consumer that skips a skill gets prompts with zero residue for it.
`auto-format`, `auto-lint`, `check-hygiene`, and `code-comments` (see the
`skills` row above) are the harness-owned skills among the mix — they bake
into every image unconditionally. The `auto-format`/`auto-lint` deferrals gate
on the `AUTO_FORMAT`/`AUTO_LINT` knobs instead; `check-hygiene` gates on the
skill's own presence (`CHECK_HYGIENE_BAKED`) exactly like the
Consumer-configured deferrals above — on a stock image that gate is satisfied
as soon as the skill bakes, so it only reads false if an operator's
skills-mount override shadows the skill away. `code-comments` is not a
prompt-side deferral at all (issue #3505): its policy body is inlined
verbatim in the four prompts that need it, unconditionally.
`CODE_COMMENTS_BAKED` is still computed
(`cmd/launcher/internal/promptassembly/gates.go`) and handed to prompt
assembly, but it now gates no fragment row — only the skill invocation
itself stays baked and available.

Issue #3223's "no flake/devShell text" rule scopes to one CHECK section:
`mkharness-prompt-check-no-nix-wording` (`nix/checks/prompts.nix`) enforces it
over the slice awked out of `issue-prompt.md` alone. The rendered fix prompt
inherits that slice byte for byte (`lib/prompt-contract.nix`'s `check` row,
injected by `injectFixSharedBlocks`), so it is covered transitively, and the
research prompts have no CHECK section to cover. What the rule leaves
unchecked is Nix wording anywhere else — the rest of `fix-prompt.md`, and
`templates/default/skills/*`. The harness-owned `auto-format` and `auto-lint`
bodies keep their own Nix mentions on purpose — auto-format's
never-`nix fmt` guardrail, auto-lint's flake/devShell checker row — because
both are ecosystem-agnostic build-tooling advice: any target repo, Nix-primary
or not, may have a `flake.nix`. Both mentions are pinned, by
`auto-format-skill-baked-into-image` (`nix/checks/image.nix`) and
`auto-lint-skill-keeps-nix-wording` (`nix/checks/prompts.nix`)
respectively (issue #3268).

## Daemon

`apps.daemon` is the unattended driving loop (issue #3538): generated per
Consumer beside `apps.default` (`lib/mkHarness.nix`), it's run as `nix run
.#daemon` — or, for spindrift's own bwrap harness,
`nix run .#dogfood-bwrap-daemon`. It takes an optional positional argument
selecting which Dispatch kinds it draws from: `dispatch` restricts it to
work, `research` restricts it to advise-only research, and omitting the
argument (the default, issue #3541) draws from both, off the single pool
described under **Pool** below. `status` is the other positional, and it is
dispatched ahead of that kind-selector parse rather than sharing its slot —
`nix run .#daemon -- status` prints the checkout's current daemon state and
exits without starting anything, needing no `--input` document (reading
status is not running a daemon). stdout is the machine-readable
`StatusReport` JSON, one object (`lockHeld`, `holder`, `live`, `stale`,
`status`), holding this binary's "stdout is the machine stream only" line
(**Event stream**, below); stderr gets one human sentence. It exits 0
whenever it produced an answer, "no daemon running" included — a scripting
caller reads `.live`, not the exit code, and this binary's own exit-code
table already spends 1 on a genuine failure. See **Status file** below for
what it reads. Both `dispatch`/`research` alone still run: `dispatch` is how an
operator who has not created the research labels runs the daemon, work-only,
exactly as before this ticket. The default flipped to both because an
operator no longer has to choose between advancing the queue and enriching
the backlog for later — a daemon left running just does both. Unlike a
single `spindrift dispatch`/`research` invocation, the daemon keeps working
the queue after it drains, so work labelled later is picked up without a
restart. It supersedes continuous dispatch as the way to hold a pool of
Boxes, which is deprecated in its favour but not removed (issue #3547) —
see **Deprecated** under [Continuous dispatch](#continuous-dispatch).

The daemon is a separate binary (`cmd/launcher/daemon`), built from the same
source tree and vendor hash as the launcher, and it's the only component
that invokes `nix` at runtime: it cannot exec the launcher store path it was
built against, since that path is precisely the stale one a rebuild exists
to replace, so each child Dispatch runs through `nix run` instead.

Each slot's own iteration resolves the tip of `BASE_BRANCH`, then pins
its next child Dispatch to that revision via a `git+file://...?rev=...`
flakeref. The resolution itself fetches — never pulls — and never
mutates the operator's working tree, so they can keep editing while the
daemon runs. A caller that arrives while a resolution is already in flight
waits on that one and takes its result, so concurrent resolutions cost one
forge request and, when the self check is on, one evaluation — the single
flight collapses every joiner onto the same leader's fetch and eval, so a
turnover of three concurrently-resolving slots costs one forge fetch and
one evaluation, not three and three. The self-path memo is a separate
saving on top, for calls that don't overlap: a later resolution that lands
on a revision the memo already covers skips the eval half outright rather
than re-evaluating a tip it's already seen. There
is no time-to-live on the sharing either way: a caller arriving after a
resolution has already finished fetches again rather than reusing a stale
one, which is what keeps the pin below exact. The pin also means a checkout
landing mid-evaluation can't produce a build of a tree that never existed
as a commit, and a child started an hour into the night is still pinned to
the tip as it was when that slot came free, not the tip at daemon startup.

**Pool.** `MAX_PARALLEL` is the daemon's own pool size (`Config.Slots`,
`cmd/launcher/internal/daemon/loop.go`): `Loop` runs that many slot
goroutines, each independently resolving and driving its own children
(concurrent resolutions share one fetch, as above), rather than one loop
iterating a single child. When both kinds are in play, they draw from that
same single pool rather than one pool apiece (`Config.Kinds`, issue #3541):
research runs through the full Box and costs exactly what work costs,
so a second, research-only pool would quietly invalidate the operator's
`MEMORY_LIMIT` × `MAX_PARALLEL` sizing by letting the daemon's real
peak exceed what that sizing accounted for. The
cap still covers everything the daemon runs — one child means one Box
(`ChildCommand` appends `--max-jobs 1 --max-parallel 1` to every
invocation, `cmd/launcher/internal/daemon/command.go`), so a slot can never
itself fan out into a second, uncounted wave, and an operator's
`MEMORY_LIMIT` × `MAX_PARALLEL` sizing (`spindrift doctor`'s
`podman-machine-memory` check, above) still bounds the daemon's real peak
just as it bounds a single `spindrift dispatch` invocation's. Nothing coordinates
which issue each slot picks up, and nothing needs to: the overlap gate
already builds its snapshot from the tracker's in-progress set plus
declared touches and open-PR changed files, and claiming already tolerates
a concurrent claimant by design (`cmd/launcher/internal/daemon/pool.go`).
Independent slots therefore coordinate through the tracker the same way
independent daemon processes or a human's `dispatch` invocation would;
daemon-side bookkeeping of who's working what would only be a second copy
of that state to keep in sync. A non-positive `MAX_PARALLEL` fails daemon
startup rather than being clamped to some default — a pool that runs
nothing while looking healthy is worse than a daemon that refuses to start.

**Discovery baton.** The coordination-free design in **Pool** above holds
only once nothing else is discovering: two children that list the queue in
the same window can both claim the same top issue, since the claim protocol
is a blind label swap with no precondition against a concurrent claimant —
and the loser can make the outcome worse than a wasted Box. `runOnce`
(`cmd/launcher/internal/dispatch/box.go`) rotates any stale log aside before
it creates its own, so a loser that slips past the runner's own
`IsRunning` pre-check in the race window rotates the *winner's* still-live
log aside before the container-name collision (issue #3633) tells it to
skip; the winner's own settle then reads an empty log and parks a genuinely
green run as `agent-failed`. The discovery baton (issue #3684) closes the
window that makes that race possible: at most one slot may be between
"started" and "announced a Box" at any moment, not just on the pool's first
wave but on every refill for the life of the process — the one-shot gate
issue #3634 first shipped only enforced that on the first wave, so two
slots whose Boxes happened to finish close together could still start
discovering at once and race for the same issue. A capacity-1 token
(`p.baton`, `pool.go`) enforces the permanent version: `newPool`
pre-assigns the initial holder to `leadSlot` (slot 0), so a cold start's
first discovering slot is fixed rather than decided by scheduler luck, but
`leadSlot` is no longer a "leader" with any lasting powers — once its first
round ends, the baton is just a token circulating to whichever slot holds
it. Acquisition (`awaitBaton`) sits immediately before the child start —
after the tip resolution and the self-build mismatch check, not before
either — because the baton guards discovery, and resolving the tip or
picking a kind selects no issue; holding the baton across a resolution
would serialize the resolutions the single-flight in `ResolveTip` (above)
exists to let sibling slots share. Apart from the pre-assigned initial
holder's very first round, a slot never holds the baton across a resolve —
every path that loops back to the top of the loop passes it first. A claim
releases the baton live, the instant the holder's child announces a Box
(`OnRecord`), not at child exit, so the hold never spans a whole Box run;
every other way a round can end without a claim passes it too
(`passBaton`) — the child returning having announced nothing (queue empty,
none dispatchable, an unrecognised exit, or a `RunChild` seam error, one
site covering all four), the Awake window shutting between the fetch and
the child start, and the holder returning for any reason at all, caught by
one deferred pass in `runSlot` that needs no guard of its own since
`passBaton` no-ops for a slot that isn't holding. Two more release reasons
exist in code — a pre-child unclassified failure reaching `backoffOrHalt`
(a fetch or self-build evaluation error), and `pickKind` finding no
runnable kind before an idle sleep — but since the baton is now acquired
after both of those points, only the pre-assigned initial holder's very
first round can ever reach them still holding it; every later round for
every slot has already passed the baton, or not yet acquired it, by the
time either can fire. `MAX_PARALLEL=1` builds no baton at all and emits
no baton event — a single slot has no sibling to stagger
against. Serialized discovery is an accepted cost, not a shortfall to work
around: a child's start-to-claim is on the order of fifteen seconds against
Box runs of tens of minutes, and a daemon runs unattended, so fill latency
does not matter, while in exchange the claim protocol stays untouched and
the daemon path needs no host-side race handling of its own — no claim
preconditions, no per-issue locks, no log-rotation guards. The runner's
container-name check remains the backstop only for a manual dispatch run
that happens to land beside a daemon. Cancellation is handled the same
way every other wait in this loop is: `awaitBaton`'s blocking select also
watches `ctx.Done()`, so a pool cancelled while a slot waits on the baton
never deadlocks that slot.

**Instance lock.** Startup also takes a non-blocking exclusive `flock` on
`spindrift-daemon.lock` (`AcquireCheckoutLock`,
`cmd/launcher/internal/daemon/lock.go`) — not under the checkout's working
tree, but inside its git dir (`git rev-parse --absolute-git-dir`,
`cmd/launcher/daemon/main.go`'s `gitDir`), since the daemon's own contract
is that it never mutates the operator's tree, and `--absolute-git-dir` is
itself per-checkout for a linked `git worktree`, exactly the granularity
"one daemon per checkout" means. Two daemons against one checkout would
each hold `MAX_PARALLEL` slots, doubling the real concurrency and making
the `MEMORY_LIMIT` × `MAX_PARALLEL` sizing above a lie; the lock is what
keeps that arithmetic honest. It covers both Dispatch kinds — two separate
daemons, one `dispatch` and one `research`, against the same checkout is
exactly the doubled concurrency the lock refuses. Running both kinds
against one checkout is what a single dual-kind daemon drawing from one
pool (**Reservation** below) already does; two daemon processes still mean
two checkouts.

The lock never blocks: a second instance finds it already held and exits
at once rather than waiting, so an operator sees "already running"
immediately instead of a hang. It does retry for a few milliseconds
first, so that a status reader's momentary lock probe cannot be mistaken
for a second daemon — see **Status file** below. Its diagnostic names the
holder — the identity line the holder stamped into the lock file
(`pid=`, `host=`, `kind=`, `started=`, `exe=`) — with enough to go find
the process. The lock lives on the open file descriptor, so the kernel
drops it the instant the holder's process ends, including an uncatchable
`SIGKILL`: a fresh daemon can always reacquire it with no manual cleanup,
and the lock file itself is deliberately never deleted on release —
deleting it would only reopen a race with a concurrent acquirer. A refused
acquire is recorded in the event stream as well as on stderr: a `halt`
event whose `reason` is prefixed `instance-lock:`, and the daemon exits 1.

**Image realize lock.** `EnsureReady` (`cmd/launcher/internal/runner/oci.go`)
takes its own lock around the realize/load/re-tag it does whenever the
configured image is absent (issue #3632): without it, concurrent Dispatch
children sharing a host — a daemon's own pool very much included — each
independently realize, load and re-tag the same absent image at once,
turning one build into as many concurrent builds as `MAX_PARALLEL`
allows. The lock sits outside any checkout, at
`<os.TempDir()>/spindrift-image-<sha256(image)[:16]>.lock`
(`imageLockPath`, `cmd/launcher/internal/runner/imagelock.go`) — hashed
because an image reference carries `/` and `:`, neither safe in a
filename, and keyed on the image reference itself rather than on the
checkout or the daemon's own instance lock, so two Consumers realizing two
different images never contend, while two checkouts dispatching the same
image tag — two separate daemons, or a daemon's pool alongside a hand-run
`spindrift dispatch` — do. "One host" there is really "one
`os.TempDir()`": `$TMPDIR` is per-user on macOS and `nix develop`
relocates it, so two users on a host, or one user in and out of a dev
shell, land on two different lock files and never contend. Unlike the
daemon's own **Instance lock** above, or the accumulation lock under
[Local code forge](#local-code-forge-code_forgelocal), which both fail
fast rather than wait, this lock blocks with no timeout: a losing child
must wait out however long the winner's build happens to take, because
bailing with an error would only trade a slow success for an avoidable
failure — the losing child needs the exact same image the winner is
already producing.

`EnsureReady` probes for the image outside the lock first, and only once
it's found absent does it acquire the lock and re-probe once more inside
it, in case the winner finished while this child was waiting to acquire.
That means the warm path — every dispatch after the one that first
realized the image — returns before ever calling `acquireImageLock`, so a
lone `MAX_PARALLEL=1` daemon slot or a hand-run `spindrift dispatch` pays
no lock syscall at all there. Its cold path does pay: the re-probe's own
`image inspect`, plus the open and `flock`, on top of the build it was
always going to run. That re-probe cannot be skipped just because this
child never waited — the winner can release between this child's outside
probe and its own uncontended acquire, and a child that skipped the
re-probe would then rebuild an image that is already present. As with the
accumulation lock's own edge case, a `SIGKILL`ed holder strands nobody:
the lock lives on the holder's open file descriptor, and the kernel drops
the underlying `flock` the instant those descriptors close, so the next
child to probe reacquires it immediately with no manual cleanup. A lock
the launcher cannot acquire, or cannot release, warns on stderr and lets
the realize proceed unlocked rather than failing it outright
(`lockImage`) — without the lock a child only duplicates the work every
child duplicated before this issue, which beats refusing to build. The
same lock guards `spindrift build`'s realize too, since `build` calls
this identical `EnsureReady` rather than a separate code path. Neither
bwrap adapter needs an image lock: `bwrapAdapter`'s `EnsureReady`
realizes nothing at all, delegating to `IsReady`, and the one that does
realize, `bwrapBuildAdapter`'s, runs `nix build` over store closures —
never a runtime image load or re-tag. That realize takes no lock of its
own either; the `lockSnapshotShared` it does take covers only the nix-var
snapshot write that follows it (`snapshotStoreDB`, and only when a
`nixConfigFileDrv` is configured), not the closure build. There is no
image tag here for concurrent children to race over.

**Child environment.** Every child the daemon starts — a dispatch child, a
research child, and the doctor preflight below — execs with the daemon's own
environment minus every key present in the `--input` document's `settings`
(`withoutKeys`, `cmd/launcher/internal/daemon/command.go`, called from both
`ChildCommand` and `DoctorCommand`; assigned to `cmd.Env` by `RunChild` and
`RunDoctor` in `cmd/launcher/daemon/runner.go`). That strips exactly the
non-secret knobs — a secret knob never enters the document at all — so a
child's only knob source becomes its own input document plus the argv the
daemon built for it. Everything else — secrets, `PATH`, `HOME`, `NIX_*`,
`XDG_*`, and any variable the document does not name — passes through
untouched, which is why the `EnvironmentFile` recipe under **Service unit**
below still reaches a child's forge credentials unmolested.
[`BOX_SIGNAL_CARRIER`](#signal-socket-box_signal_carriersocket) is one of the
values that passes through this way, and it has to: the knob is deliberately
not a `flakeOption`, so it never enters the input document's `settings` map
(`signalCarrier`, `lib/env-schema.nix`, whose own doc string spells out the
same rationale) and the daemon's environment is its only route to a child.
`BOX_SIGNAL_CARRIER=socket nix run .#daemon` therefore flips the carrier for
every child the daemon starts — set on the daemon, the knob is present in
each child; unset, it is absent and the child takes the schema default `log`
— as it does for an empty export, which rides through as a present-but-empty
entry the child's own `getenvSchema` read (`cmd/launcher/main.go`) collapses.
There is no live toggle: a restart is the flip. A value outside the schema's
choices refuses the daemon's start — exit 1, with a stderr line naming the
knob (`validateSignalCarrier`, `cmd/launcher/daemon/main.go`) — before any
child is spawned. Earlier still, right after the input document loads and
ahead of that refusal, the daemon checks each stripped key against its own
environment (`settingsKeys` and `warnStrippedChildEnv`,
`cmd/launcher/daemon/main.go`) and, for every one actually set, prints one
stderr line before the startup preflight runs:

```
MODEL=x set in environment — not forwarded to children; use the --input document's settings.MODEL
```

It never refuses to start over this, unlike the `BOX_SIGNAL_CARRIER` check
that runs after it. An exported `CONTINUOUS_DISPATCH=1` or `ISSUE_NUMBER` or
`MODEL` now configures the daemon alone — its own knob resolution is
unchanged, and an ambient value still wins there with its own, separate
deprecation warning (`lookupKnob`, same file) — it simply never reaches a
child. Anything a child's own wrapper re-sources from `harness.env` in its
working directory is outside the daemon's control and stays so.

Every child's stdin is `/dev/null`: it runs in its own process group (see
**Halting** below), and a background process group that reads the tty is
stopped with `SIGTTIN` rather than handed a prompt. A `<SECRET>_CMD` secret
command — the preferred secret form, see [Runtime
configuration](#runtime-configuration) — must therefore be non-interactive
under the daemon: unlock the vault before `nix run .#daemon`, because no
mid-loop unlock prompt can reach an operator. `dogfood.sh` used to allow
that prompt; the daemon deliberately does not.

**Startup preflight.** After the instance lock and the signal wiring, before
any slot, claim, or Box, the daemon runs the pinned child's `doctor`
subcommand exactly once (`startupPreflight`, `cmd/launcher/daemon/main.go`)
— never again per iteration. It pins doctor to the same freshly fetched tip
the first child will run at, so the preflight validates the build about to
actually run rather than the operator's possibly-stale working tree. The
daemon passes no verbosity flag, so doctor's quiet-by-default behavior
(`--verbose`/`-v` opts back into the full report) governs the preflight: a
healthy start adds no doctor report at all to the daemon's own stderr, and a
refused one adds only three things: the failing `MISSING:` rows (a missing
triage label's row is bare; a failing check's row carries an indented
`remedy:` line), the standalone `remedy:` line the connectivity fail-fast
path prints under no row at all (`Run`,
`cmd/launcher/internal/doctor/doctor.go`), and doctor's own stderr summary. The daemon's stdout stays reserved for
the event stream. It runs non-interactively, so a missing label is a refusal,
never a prompt. A non-zero doctor exit refuses the start outright,
naming what failed and its remedy both on stderr and in the event stream
(`ClassifyPreflight`, `cmd/launcher/internal/daemon/preflight.go`). The
class this catches that nothing else does is missing triage labels: without
it, a daemon with no `ready-for-agent`/`agent-in-progress`/`agent-failed`/`agent-complete`
labels on the target repo looks perfectly healthy all night while every
claim silently fails; the same pass also catches an undersized podman
machine before the VM dies mid-run rather than after. Runtime readiness is
deliberately not re-checked here — every Dispatch already validates it
before any claim, so the preflight would only be re-asking a question the
child asks itself. A missing *research* label never refuses a start,
matching doctor's advisory treatment of that family (ADR 0022). Doctor's
own exit-code table is a separate contract from the child's: the daemon
reads it through `ClassifyPreflight`, not the `Interpret` mapping the
child-dispatch table above uses, which is why every `preflight` event's
`outcome` is `doctor-`-prefixed and can never be confused with a
`child_finish` outcome (below) — including the two labels that come from
the daemon rather than from `ClassifyPreflight`'s table,
`doctor-seam-error` (the tip could not be resolved, or doctor could not be
run at all) and `doctor-cancelled`. A Ctrl-C during the preflight is a
clean stop — exit 0, the same as any other operator-requested stop — not a
refusal: a doctor child killed by the signal has no exit code at all, so the
daemon reads the cancelled context rather than trying to classify the dead
child's status as a verdict.

**Status file.** Beside `spindrift-daemon.lock`, in the same git dir, the
daemon publishes `spindrift-daemon.status` (`statusFileName`,
`cmd/launcher/internal/daemon/status.go`): one JSON object, rewritten on
every state change and never on a timer. The event stream answers what
happened overnight; the status file answers what is happening right now,
without a reader having to replay the stream (issue #3545).

Every publish stamps the same envelope onto the object regardless of what
changed: `pid` and `host` (`os.Hostname`; the literal string `unknown` if
that fails) identify the writing process, `started` and `time` are both
RFC3339 UTC — `started` fixed at process start, `time` moving on every
publish — and `kinds` names the configured kind set. `state`, `reason`,
`slots[]` and `checks[]` (below) are what actually varies between
publishes.

The lock is the liveness truth and the status file is advisory data. A
killed daemon cannot clean up its status file — that is exactly the path
with no cleanup — but the kernel does drop its `flock` regardless, so a
reader probes the lock before believing the file (`ReadStatus`), and the
file records the writing daemon's `pid` and `host` so the two can be
correlated against the lock's own identity line — both, because a pid
alone collides across the hosts a shared checkout can be mounted on: a
lock held by a *fresh* daemon that has not published yet, beside a dead
predecessor's leftover file, reads as that predecessor's leftover rather
than live.
A stale file never reads as a live daemon. The probe takes a **shared**
`flock`, not an exclusive one — it still conflicts with the holder's
exclusive lock, so success proves nobody holds it, but two concurrent
readers never refuse each other — and it never creates the lock file it
probes (`probeCheckoutLock`). A starting daemon in turn retries its own
exclusive acquire for a few milliseconds before declaring the lock held
(`AcquireCheckoutLock`), so a reader's momentary probe window can never
read as a second daemon and stop the real one from starting.

The file survives a clean stop on purpose: the last thing a stopping
daemon publishes is `halted` with its `reason`, and a reader that finds
the lock unheld already reports the file stale, so its contents are a
last-known record of how the run ended rather than a lie. Deleting it on
exit would destroy exactly that.

Writes are atomic — a temp file in the same dir, then `os.Rename` — so a
reader never sees a half-written object, and a failed write is reported to
stderr and otherwise ignored: advisory data must never fail the daemon.
It is rewritten on every `mutate` — the pool's one path to changing
anything — rather than on a timer. The publish cannot simply be folded
into whatever gets emitted, because not every state change has an event
to ride along with: a phase move, a backoff reset, and an issue append
raise no event of their own. So it is folded into `mutate` instead:
`mutate` allocates a sequence number in the same lock hold it took the
snapshot in, and hands both to `StatusWriter.Publish`, which drops any
snapshot whose sequence is older than the last one it wrote — so two
slots publishing concurrently can never leave the older state on disk,
without the publish calls themselves needing to arrive in order.

Riding the `mutate` rather than the change means a `mutate` that alters
nothing republishes the same state. That covers the pool's opening
snapshot, every slot iteration's top-of-loop `noteAwakeOpen` on an
already-open window, a re-park wake that still finds the window shut, an
`emit` of an event that carries no state of its own (`box`,
`child_finish`, `baton_hold`), and an issue announced by a slot that has
already cleared. So the cadence is "every `mutate`", which is more often
than "on state change" alone would suggest. There is still no periodic
rewrite ticker, though: every rewrite traces back to something the
daemon did, even where what it did was wake from a clock-computed sleep.

The most load-bearing part of the file is `state`, and specifically the
two ways of being idle that look identical from outside and mean
opposites:

| `state` | meaning |
|---------|---------|
| `working` | at least one slot has a child running |
| `waiting` | nothing is running, every configured kind is gated, and none of them by a none-dispatchable result — every queue is empty, ordinary overnight quiet |
| `jammed` | nothing is running, every configured kind is gated, and at least one of them by a none-dispatchable result — open issues exist and nothing can dispatch them |
| `asleep` | nothing is running and the Awake window is shut |
| `checking` | nothing is running but at least one kind is runnable — a slot is between iterations, about to check the queue |
| `halted` | the pool has halted; `reason` says why |

`waiting` and `jammed` are indistinguishable to an outside observer — both
mean "nothing running, nothing to do right now" — but the first is
healthy and the second needs an operator (merging a blocker, relabelling
an issue). Only the daemon sees each kind's last check outcome — "queue
empty" versus "open issues, none dispatchable" — and nothing downstream
could recover the distinction if the daemon did not report it (issue
#3545; see also `jam` in **Event stream**, below).

Precedence, most urgent first: `halted` outranks everything, since the
pool is already on its way out regardless of what else is true; `working`
outranks `asleep`, since a child started before the window closed is
still genuinely running even though the window has since shut; `jammed`
vs `waiting` only applies once every kind is gated; `checking` is the
fallback when nothing is gated and nothing is running.

Per-slot, `slots[]` carries `slot`, `phase`, `busy`, and for a busy slot
the `kind`, the `revision` it is pinned to, and the `issues` its child
has announced so far, deduped — a fix pass or conflict-resolve for an
issue already in the list is the same claim continuing, not a second
one, so each issue names once no matter how many boxes it takes —
enough to correlate a running Box with the commit that produced it.
`phase` is the slot's own position in its iteration,
one of five values: `idle` (parked, holding nothing), `awaiting_window`
(parked because the Awake window is shut), `resolving` (fetching the
tip, or evaluating the daemon's own self-build), `running` (a child in
flight), or `backing_off` (sleeping out a failure backoff). A slot that
has stopped for good — the pool is halting, and this slot's goroutine has
already returned — reads `idle` as well: it holds nothing, so it must not
read as engaged while a sibling drains its own child. `busy` is
exactly `phase == "running"` and nothing more — a reader that only knows
`busy` sees what it always saw. The `jam` alarm (see `jam` in **Event
stream**, below) fires only when every *sibling* slot is `idle` or
`awaiting_window`; a sibling that is `resolving`, `running`, or
`backing_off` suppresses it. The issues arrive live, appended (once per
new issue) via `ChildRequest.OnRecord` as each `box` record is read off
the report pipe (`pool.noteBox`, see **Event stream** below) — there is
no post-exit carrier at all any more, so what a slot's status shows is
always what it currently has, never a stale replay of what it once had.
Per-kind,
`checks[]` carries each configured kind's `nextCheck` (RFC3339, empty
when the kind is runnable now) and `jammed` (true when this kind's last
check found open issues none of which were dispatchable, and it is still
gated on that result) — this is what makes the pool-level `jammed` state
above recoverable down to the specific kind, rather than only "some kind
is jammed", so "nothing is happening" reads as legible rather than
alarming. A shut Awake window pushes every kind's
`nextCheck` out to the instant the window reopens, whatever that kind's
own backoff says — no slot starts a child until the window is open
again — so an `asleep` daemon says when it will next look rather than
reading as runnable now.

**Reservation.** `RESEARCH_RESERVATION` (default 1) is how many of the
pool's `MAX_PARALLEL` slots prefer research over work (`slotOrder`,
`cmd/launcher/internal/daemon/pool.go`). It is a floor, not a ceiling: a
reserved slot takes research only while research actually has queued work,
and either kind bursts into the whole pool the instant the other has
backed off into an empty result — reserved capacity never sits idle
waiting for work that isn't there. 0 is work-first, with research only on
the leftovers; a value equal to `MAX_PARALLEL` is research-first. The
reservation binds only while both kinds have work; the moment one runs dry
every slot, reserved or not, is free to fill itself from the other. The
preference itself is decided statically, per slot, from the slot's own
index against the reservation (slot index below the reservation prefers
research, the rest prefer work) rather than from a live count of who is
currently running what — a static assignment makes the floor exact without
lock-step counting across slots, and it means two slots picking a kind
concurrently can never both claim the same reserved slot, since each
computes its own answer from its own slot number alone. The knob is inert
when the daemon's positional verb already restricts it to one kind (there
is nothing to reserve slots *from*); a value above `MAX_PARALLEL` fails
daemon startup the same way a non-positive `MAX_PARALLEL` does. At
`MAX_PARALLEL=1` the default of 1 makes a dual-kind daemon research-first —
its one slot always prefers research when research has work — so an
operator who wants that single slot work-first instead sets
`RESEARCH_RESERVATION=0`. Like the idle-backoff and breaker defaults below,
1 is a defensible first cut, not a tuned final answer — issue #3541 put the
final value out of scope, and it expects a real unattended run to argue
with it.

**Cross-family discovery.** Running both kinds off one pool means the same
issue can legitimately be dispatchable and researchable at once — the two
label families are independent and, per `CLAUDE.md`'s "Research label
lifecycle" section, may both sit on one issue at once. Left alone, that
would let a researcher and a worker land on the
same issue at the same time, the researcher writing enrichment for a
worker that's already running. Each kind's own discovery query
(`queryOpenIssues`, `cmd/launcher/main.go`) skips a discovered issue that
already carries the *other* family's in-progress label, so a slot never
picks up work the other family has already claimed. This is a
discovery-time scheduling preference, not a new claim rule — the
claim-time invariant that the two label families never interact is
unchanged, and an explicitly claimed `ISSUE_NUMBER` still runs regardless
of what the other family's label says, exactly as it did before this
ticket. The filter is tracker-derived: it reads the labels the tracker
already returned with each issue rather than adding a query qualifier, so
in-progress work started by a Console session, CI, or a human is just as
visible to it as work this daemon started itself, and an operator who has
never created the research labels pays nothing for the check — an absent
label is a label no issue carries, so the filter is a silent no-op.

`DAEMON_APP` (default `.#`) is the flake app attribute the daemon
re-invokes for each child — see the `DAEMON_APP` row in [Advanced
tuning](#advanced-tuning). `DAEMON_SELF_APP` (default `.#daemon`) is its
sibling: the flake app attribute of the daemon itself, evaluated against
each fetched tip to notice its own build changed — see the
`DAEMON_SELF_APP` row in the same table and **Self-change halt** below.

Each child's exit code is interpreted the same way `spindrift`'s own exit
codes are (see the [exit-code table](#dispatch-exit-codes) in Dispatch exit
codes above, which this table's meanings link back to) — but the daemon's
*action* on each code is its own. The wait below is no longer a fixed
interval: it is an idle backoff, one
per configured Dispatch kind (`kindBackoff`,
`cmd/launcher/internal/daemon/backoff.go`) rather than one pool-wide timer
(issue #3541 split it: an empty work queue must not slow research down,
and vice versa, so each kind's own no-work streak — and the growing wait
it earns — lives and resets independently of the other's). Within a kind
the streak is still pool-wide, not per slot: any slot's no-work result
against a kind counts against that kind's one timer, the same shape as the
breaker below. The first no-work check against a kind waits `IdleFloor`
(`DAEMON_IDLE_FLOOR`, default 5 minutes, see
[Advanced tuning](#advanced-tuning)); each further *consecutive* no-work
check against that same kind doubles the wait, capped at `IdleCap`
(`DAEMON_IDLE_CAP`, default 30 minutes) — 5m → 10m → 20m → 30m, which takes
a drought from 12 checks an hour down to 2, at the price of waiting out at
most that half hour before the next check notices work that arrived just
after a capped wait began. The quantity each kind's backoff bounds is that
kind's own rate-limit spend against the forge, since an idle check still
costs a fetch, an evaluation, a bootstrap and a discovery query to learn
"nothing to do" again. A check that answers something else — exit 0 or
exit 4, below — resets that kind's wait back to `IdleFloor`, on the theory
that a check finding real work is evidence that kind's drought is over; the
other kind's own timer, if any, is untouched.

Because the backoff is now per kind, a `Wait` outcome (exit 2 or 3) no
longer sleeps the slot in place: the slot records the no-work result
against the kind it just ran and immediately loops back to the top, where
it tries the *other* configured kind at once if that kind is still
runnable — an empty work queue does not idle a slot that could be running
research, and an empty research queue does not idle one that could be
running work. Only once every configured kind is gated does the pool
actually sleep, and then for the shortest of the gated kinds' remaining
waits — the pool as a whole can move the instant any one kind's gate
lifts, even though a given slot only picks up work once it wakes and
re-checks. A daemon restricted to one kind by its positional verb never
sees this switch: with one kind configured, a `Wait` outcome always finds
every configured kind gated, so it degenerates to the old single-timer
in-place wait.

| exit | meaning | daemon action |
|------|---------|----------------|
| 0    | dispatched work | go again at once; resets this kind's idle backoff to `IdleFloor` (the other kind's timer is untouched) |
| 2    | queue empty | record it against this kind's own backoff (emit `idle`), then loop back around: switch to the other configured kind at once if it is still runnable, or sleep — via the shared `idleSleep` — only if every kind is now gated. A queue-empty gate is never itself polled mid-wait: a merge cannot create work in an empty queue, so polling for one would only spend a query for nothing |
| 3    | none dispatchable | with any sibling slot `resolving`, `running` or `backing_off` — doing anything at all but waiting for its own turn — routine: record it against this kind's backoff (emit `idle`) the same as exit 2. Only once every sibling is `idle` or `awaiting_window` is it recorded as a jam instead (emit `jam`), same routing (a single-slot daemon has no siblings at all and so reports every exit 3 as a jam). Either way the slot switches to the other configured kind at once if that kind is still runnable; only once every kind is gated, *and* at least one of them is jammed, does the shared `idleSleep` sleep in `IdleFloor`-sized slices, since a merge here *can* unblock the jam — there is no separate poll: the slot sleeps one slice, and the next round's own resolution, made before it picks a kind, is what finds out, so a jammed wait costs one fetch per slice, not one for a poll and another for the round it unblocks. The first no-work wait for a kind is exactly one `IdleFloor` slice and so still resolves nothing extra, with the next round's own resolution asked for only once that kind's backoff has grown past the floor; if that resolution reports the tip moved (`Tip.Moved`), the slot emits `tip_moved` once and resets *every currently-jammed kind's* backoff to `IdleFloor` (the observed change is evidence for all of them, not just the kind this slot was running), and goes again at once instead of riding out the rest of the wait. A mid-wait resolution that fails is treated as no change observed — it never feeds the breaker, since the iteration's own post-`pickKind` resolution is what reports a broken fetch |
| 4    | image stale | go again at once — every child is born from a freshly resolved revision, so the next iteration's pin is the rebuild; resets this kind's idle backoff to `IdleFloor` (the other kind's timer is untouched) |
| 5    | host-tainted | halt the pool |
| 6    | config-invalid | halt the pool |
| 7    | signalled stop | halt the pool once the operator's Stop latch is already closed; while Stop is still open, an unrecognised operator-external signal instead backs this slot off (see **Failures** below) |
| anything else | unrecognised (an unclassified exit code, a `RunChild` seam error, or a `ResolveTip` error — a fetch failure or a self-build evaluation failure, carrying different reasons — all land here) | back this slot off alone for `FailureBackoff` and refill it — see **Failures** below |

**Failures.** An unclassified failure — an exit code `Interpret`
(`cmd/launcher/internal/daemon/outcome.go`) doesn't recognise, a `RunChild`
seam error, or a `ResolveTip` error (a fetch failure, or a self-build
evaluation failure — see **Self-change halt** below; the two carry
different reasons, and it's the self-build one's halt class that decides
the exit code if the breaker trips on it) — no longer halts the pool
by itself: the failing slot backs off for `DAEMON_FAILURE_BACKOFF` (default 1
minute, see [Advanced tuning](#advanced-tuning)) and refills itself, and the
sibling slots never notice. A `RunChild` seam error, an unrecognised exit
code, or exit 7 while Stop was still open — every one of these reaching
`backoffOrHalt` (`cmd/launcher/internal/daemon/pool.go`) — is carved out
first and never counted at all once the operator's Stop latch was already
closed when it landed (issue #3595, now by construction): it is an
ordinary shutdown, not evidence of a systemic fault, so it can no longer
trip the breaker and turn a clean operator stop into a non-zero exit. That
alone would burn every slot on a fault no retry clears, so these
failures are also counted pool-wide by a circuit breaker
(`cmd/launcher/internal/daemon/breaker.go`): `DAEMON_BREAKER_THRESHOLD`
(default 5) of them within a trailing `DAEMON_BREAKER_WINDOW` (default 15
minutes, see [Advanced tuning](#advanced-tuning)) halts the whole daemon and
emits a `breaker_trip` event, on the theory that a systemic fault (an expired
token, a forge outage) fails every slot's child immediately. At the default 3
slots that crosses the threshold within about one backoff; at
`MAX_PARALLEL=1` the count is one slot's own retries, so it crosses after
four backoffs instead — slower, but still well inside the window, because a
lone slot that keeps failing has no sibling doing useful work for a spared
breaker to protect. All three defaults are a defensible first cut, not a
tuned answer — revisiting them is now a settings change, not a code change.
The halt-mapped exits 5 and 6 are untouched by the breaker: a tainted host
and an invalid config still halt the pool at once, since no retry clears
either. Exit 7 only joins them once the operator's Stop latch is already
closed — the operator's own request; while Stop is still open, exit 7 is
the unclassified failure above instead, and backs its own slot off rather
than halting anything.

**Self-change halt.** At each slot's iteration boundary — after that
iteration's own resolution finds the tip, before any child is launched —
the daemon evaluates its own app attribute at that revision and compares
the resulting program store path against its own, a field comparison in
`runSlot` (`cmd/launcher/internal/daemon/loop.go`) against the self-path
`ResolveTip` already returned. The evaluation is memoised by revision, so
one `nix eval` covers a given tip however many slots ask about it, and a
new tip is what triggers a fresh evaluation. A failed evaluation is never
memoised — the memo is left untouched so it can't serve a later caller at
that same revision a path it never actually got. The attribute is
`DAEMON_SELF_APP` (default `.#daemon`), the daemon's own app, distinct from
`DAEMON_APP`, the child Dispatch app the slot is about to launch — see the
`DAEMON_APP` note above. A Consumer that re-exports the daemon under a
different top-level name must set it to match; spindrift's own bwrap
harness does exactly that (`.#dogfood-bwrap-daemon`, `nix/fixtures.nix`),
and without it the check would evaluate a different harness's daemon
entirely and report a permanent, spurious change on every iteration.

The evaluation is `nix eval --raw
git+file://<checkout>?rev=<tip>&allRefs=1#apps.<system>.<attr>.program`
(`SelfCommand`, `cmd/launcher/internal/daemon/command.go`) — the same
pinned flakeref shape every child gets, with the attribute path spelled out
in full because `nix eval`'s own fragment resolution searches
`packages`/`legacyPackages`, not `apps`. The daemon's own path comes from
`SPINDRIFT_DAEMON_PROGRAM`, an env var the generated wrapper
(`lib/mkHarness.nix`'s `daemonWrapper`) exports from its own `$0` at
runtime — a derivation cannot interpolate its own store path into its own
build text. A daemon started any other way (`go run` during development,
say) cannot know its own build, so rather than guess, the check disables
itself and says so on stderr.

On a mismatch the pool halts through the same machinery every other halt
uses (below): any child already running is waited out and still gets its
`child_finish`, never killed — the halting slot simply starts no new
child. **It never re-execs itself**: a freshly merged but broken daemon
must not auto-load with nobody awake. The halt carries its own exit code
(below) precisely so an operator who *does* want self-update can compose it
with an ordinary service restart policy and get that behaviour by choice,
not by default: a restart policy that does not exempt exit 10, plus a step
that advances the checkout before each start (see **Service unit** below
for the full unit and the exit-code-to-policy mapping, and **Self-update by
choice** below for why the second half is not optional) — with the
obvious caveat that under that policy the daemon started next is whatever
just merged, so a broken merge restarts straight into the broken build,
which is exactly why this is opt-in rather than the default.

What moves the daemon's build: any change under `cmd/launcher`'s Go
module (`daemonBin`, `lib/mkHarness.nix`). Its source is the whole module
tree, so a test file or one of the sibling `driver-exec`, `orchestrator`
and `quickstart` packages moves it just as `cmd/launcher/daemon` and
`cmd/launcher/internal/daemon` do. So does a
change to any setting the wrapper bakes in — the run-input document,
prompts/skills, or the agent image/closure the Consumer's settings
resolve to. What does not: a commit touching only `docs/`. `daemonBin`
builds from a file-set source (`daemonSrc`) rooted at `../cmd/launcher`
itself, so `docs/` sits outside it and it still shares
`launcherVendorHash` — unlike `launcherBin`, whose own `src`
(`launcherSrc`) copies `../docs` alongside for its own checkPhase
(#611). So `daemonBin`'s store path, and with it
`SPINDRIFT_DAEMON_PROGRAM`, no longer moves on a documentation-only
merge, which used to halt a daemon running under the restart-on-exit-10
policy above for nothing (issue #3621).

An evaluation that *fails* — a broken flake, a network blip reaching `nix
eval` — is not treated as a change: it is the same class of unclassified
iteration-boundary failure as a fetch error (see **Failures** above), so
the slot backs off for `FailureBackoff` (a `backoff` event whose `reason`
is prefixed `self-build:`) and retries from a fresh fetch, and only a
persistent failure reaches the pool-wide breaker. It isn't simply ignored,
because silently swallowing it would leave this safety property off for as
long as the evaluation stays broken.

The daemon *process*'s own exit code is a separate, smaller taxonomy from
the child's table above — worth keeping apart, since both can appear in
the same log:

| exit | meaning |
|------|---------|
| 0    | a clean stop — an operator signal, or a child that drained and reported a signalled stop |
| 10   | the daemon's own build changed at the fetched tip and it halted at an iteration boundary |
| 11   | the startup preflight refused the start — a Required-tier `doctor` failure (missing triage labels, invalid config) or a seam failure resolving the tip/running doctor at all |
| 1    | anything else: a startup failure (including a refused instance lock), or any other halt — the event stream carries the specific reason |

10 and 11 both sit deliberately outside the 0–7 band the *child*
launcher's exit codes occupy, so neither taxonomy can be confused with the
other when both appear in one log (`ExitSelfChanged`,
`ExitPreflightFailed`, `cmd/launcher/internal/daemon/halt.go`, where
`Halt.ExitCode` derives every code in the table above from the halting
class alone — nothing re-reads the reason string). The two read very
differently to an operator composing a restart policy, though: 10 is the
one code this daemon deliberately invites a supervisor to compose with
`Restart=on-failure` (**Self-change halt** above), since restarting clears
it by loading whatever just merged — for a unit that also advances the
checkout before each start (**Self-update by choice** below). 11 is not
retryable that way — nothing a restart does fixes a missing label or an
undersized podman machine, so a supervisor must treat 11 as a standing
refusal to fix by hand, not a transient fault to bounce past.

**Halting.** A `SIGINT` or `SIGTERM` to the daemon is the first of two
signals it consumes, through the same shared relay the launcher itself uses
(`stopsignal.Notify`/`stopsignal.Relay`,
`cmd/launcher/internal/stopsignal/relay.go`): the first of either kind
closes the daemon's own Stop latch, which `announceStop`
(`cmd/launcher/daemon/main.go`) turns into a `shutdown` event before
closing the pool's own `cfg.Stop` channel in turn. The pool's own Stop
watcher (`Loop`, `cmd/launcher/internal/daemon/loop.go`) halts the pool the
moment that channel closes — the same `HaltOperatorStop` class, reason
`context-cancelled: stop requested`, a caller's own hard-cancelled context
already renders as — cancelling a context shared by every slot, so a
sibling asleep in its idle wait or blocked in a fetch stops promptly. Every
running child's own `forwardSignals` goroutine
(`cmd/launcher/daemon/runner.go`), started once per child by `RunChild`,
sends it a `SIGTERM` at that same close — the drain request, the same
gesture as the launcher's own relay. It never kills a child that is
already running its Boxes: the child chooses to
drain, and a pool halt is the same courtesy at pool scale — any child
already running is always waited out and always gets its `child_finish`
before the process exits, never abandoned mid-run. The daemon implements no
drain or reap of its own: it only forwards signals and waits, since
`daemon.Loop`'s own `wg.Wait()` is what waits every child out, before and
after the escalation below alike — the launcher, not the daemon, is what
holds the in-flight Boxes. The child is started in its own process group
(`cmd/launcher/daemon/runner.go`), so a Ctrl-C aimed at the daemon's own
foreground process group — which would otherwise deliver a group-wide
SIGINT straight to the child — spares it; only a signal the daemon
forwards explicitly reaches it.

A child that starts after Stop has already closed is still signalled,
promptly rather than by any replay: `forwardSignals`'s select on an
already-closed channel fires at once, so a child born mid-shutdown is
signalled the instant it starts, rather than racing a shared counter the
way the daemon's old children-map replay once did (issue #3626). A
signal a child receives from anywhere other than the daemon — systemd's
own `KillMode=control-group`, an operator signalling the child directly —
is a different story: the daemon never asked for it, so an exit 7 it
produces while the daemon's own Stop latch is still open is not "the
operator stopped this child", and is instead an unclassified failure like
any other (`Interpret`, `cmd/launcher/internal/daemon/outcome.go`) — it
backs that slot off alone and counts toward the breaker rather than halting
the pool with a clean exit 0. Only once the Stop latch has closed does exit
7 mean "the operator stopped this child" and halt the pool at once
(`HaltChildSignalled`) — the breaker's one carve-out (issue #3595, now by
construction; `backoffOrHalt`, `cmd/launcher/internal/daemon/pool.go`) is
exactly that: a failure of any kind that lands once the operator's Stop
latch was already closed is never counted, because it is an ordinary
shutdown rather than evidence of a systemic fault.

A second `SIGINT`/`SIGTERM` to the daemon is the escalation: the relay
closes Abort, and every running child's `forwardSignals` goroutine sends it
a `SIGINT` this time rather than a repeat `SIGTERM` — two identical
signals sent back-to-back can coalesce into a single delivery (the kernel
keeps one pending bit per signal number, not a queue), so escalation
switches kind rather than repeating the first. The child counts deliveries,
not kinds (issue #3521), so whichever kind lands as its second signal is
what makes it abort the drain — reap its in-flight Boxes and release
their issues back to the dispatchable pool — instead of waiting the drain
out to completion. The relay's own signal channel is buffered at 2 so that
an already-delivered second signal is never dropped for want of room while
the relay goroutine is between receives — it cannot buy back a repeat of
the same signal number arriving in the same instant, since that coalescing
happens earlier, in the kernel, before either delivery reaches the channel;
an operator wanting a reliable escalation should send the other kind, or
leave a gap between the two. A third and later signal is a genuine no-op:
the relay returns once it closes Abort on the second, and nothing else ever
reads the channel again — a persistent operator has no further escalation
to give past the second signal, only a `SIGKILL` on the daemon itself,
which orphans the child's isolated process group rather than killing it.

A `systemctl stop` sends the daemon a `SIGTERM`, which the daemon forwards
as that same drain request — but only under a unit that keeps systemd's
own `SIGTERM` away from the child. The default `KillMode=control-group`
signals *every* process in the unit's cgroup, and the process-group
isolation above isolates a process group, not a cgroup, so the child
launcher receives systemd's `SIGTERM` as well as the daemon's own forwarded
one — and the daemon's first forwarded signal is always a `SIGTERM` too,
so these two are the same kind, sent close together — the very coalescing
the escalation above switches kind to avoid. The kernel may fold the two
into a single delivery, in which case the child sees one signal and drains,
or it may keep them distinct, in which case the child counts two and
aborts. Under the default `KillMode` a plain `systemctl stop` is therefore
a race between draining and reaping rather than a reliable path to either
— unpredictable, which is its own reason to avoid it. Worse, systemd's
own `SIGTERM` reaches the child directly, not through the daemon: if the
kernel keeps the two deliveries distinct, the child's own exit 7 lands
while the daemon's Stop latch may still be open, which the daemon now
treats as an unclassified failure — backing that slot off and counting it
toward the breaker — rather than the clean, pool-halting exit 0 a
daemon-requested stop gets. Set `KillMode=mixed`, which sends the stop
signal to the main process alone, leaving the forwarded drain request as
the child's only signal, paired with `TimeoutStopSec=infinity` (see
**Service unit** below for the full unit).

The daemon always waits out a started child before exiting
(`daemon.Loop`), so by the time systemd sees the main process go there is
nothing left in the cgroup for the final `SIGKILL` `KillMode=mixed`
sends.

`TimeoutStopSec` has to cover that whole drain: the in-flight Box's
runtime *and that Box's settle*. Size it against the settle's *ceiling*,
not the Box runtime alone and not the Box runtime plus a single
`MERGE_POLL_TIMEOUT`. At the defaults that ceiling is
`MAX_FIX_ATTEMPTS + MAX_REBASE_ATTEMPTS + 1` fresh `MERGE_POLL_TIMEOUT`
windows — seven of them, about seven hours of polling, or eight with the
stale-base preflight enabled — plus the runtimes of up to three fix Boxes
and up to three conflict-resolve Boxes, or four with that same preflight;
see the signalled-stop drain under [Dispatch exit codes](#dispatch-exit-codes)
for the breakdown. systemd's own default `TimeoutStopSec` is `90` seconds, far
below even the floor, so leaving it at the default means `SIGKILL` lands
mid-drain — the exact stranding (orphaned Boxes, a leaked registry-proxy
socket, an issue stuck on the in-progress label) this whole drain exists
to avoid. `TimeoutStopSec=infinity` is the honest setting for a unit that
must never strand a Box; any finite value is a bet that no drain enters
self-heal or hits a merge conflict.

An operator unwilling to wait out that ceiling now reaches the same
escalation by signalling the daemon's own main process a second time —
it no longer has to reach the *child launcher* directly. `systemctl kill
-s TERM --kill-whom=main <unit>` under systemd, or a second `kill -TERM
<daemon pid>` outside it, is the daemon's second signal; a second
`systemctl stop` is not, since systemd treats a unit already deactivating
as a no-op and never resends the stop signal. `--kill-whom=main` matters
under `KillMode=mixed`: it targets the daemon alone, which then forwards
that second stop request as the child's second signal — a `SIGINT` this
time, since escalation switches kind rather than repeating `SIGTERM` (the
relay closing Abort, `forwardSignals` sending it) — the signal the child
counts as its own abort
escalation. Under the default `KillMode=control-group`, though, there is
nothing reliable left to escalate: the first `systemctl stop` already
delivered systemd's own `SIGTERM` to the child alongside the daemon's own
first forwarded `SIGTERM`, the same coalescing race described above, so
whether that single `systemctl stop` already aborted the drain depends on
whether the kernel folded those two `SIGTERM`s into one delivery or kept
them distinct — the same reason `KillMode=mixed` is the recommended
setting above.

**Service unit.** The pieces above — the restart-policy composition in
**Self-change halt**, `KillMode=mixed`, and `TimeoutStopSec` in
**Halting** — are all one unit in practice. A complete, copyable shape,
run as the operator's own user against their own checkout (the daemon
fetches into and locks that checkout, so `WorkingDirectory` has to be
it), as a systemd user unit so no `User=` line is needed (and no
`Wants=`/`After=network-online.target` either: that target is a
system-manager concept the user manager ships none of — a *system* unit
would add it, a user unit has nothing to add it to):

```
[Unit]
Description=spindrift daemon
StartLimitIntervalSec=1h
StartLimitBurst=5

[Service]
Type=simple
WorkingDirectory=%h/spindrift-checkout
EnvironmentFile=-/absolute/path/to/spindrift-secrets.env
Environment=PATH=/absolute/path/to/nix-dir:/absolute/path/to/git-dir:/usr/bin:/bin
ExecStart=/absolute/path/to/nix run .#daemon
KillMode=mixed
TimeoutStopSec=infinity
Restart=on-failure
RestartSec=30s
RestartPreventExitStatus=10 11

[Install]
WantedBy=default.target
```

Substitute the checkout path, the directories holding the real `nix` and
`git` (`command -v nix`, `command -v git` — the `nix` one feeds both
`Environment=PATH=` and `ExecStart`), and a secrets file for the
placeholders. `ExecStart` is argv, not a shell line, so it takes no shell
quoting and no `&&`. `StartLimitIntervalSec`/`StartLimitBurst` are
`[Unit]` directives, not `[Service]` ones, however much they read like
part of the restart policy.

`Environment=PATH=` matters even with an absolute `ExecStart`, and it is
the line most easily dropped from a pasted unit. The daemon execs both
`git` and `nix` by bare name — `git fetch`/`git rev-parse` at every
iteration boundary (`ResolveTip`, `cmd/launcher/daemon/runner.go`), and
`nix run`/`nix eval` for every child it starts and every distinct tip's
self-build evaluation, memoised so a tip several slots ask about is only
evaluated once (`cmd/launcher/internal/daemon/command.go`). `git fetch`/`git
rev-parse` and the self-build `nix eval` set no `cmd.Env` at all; the
child-starting `nix run` sets a knob-stripped copy of the daemon's own
environment (see **Child environment** above) that still carries `PATH`
through unchanged. Either way the unit's own `PATH` is the only place
either binary can be found; the systemd user manager's default `PATH`
carries neither on a NixOS host. Without this line the daemon does not
limp along dispatching nothing — but the two binaries are reached for
at different points in startup, so which one is missing decides both the
exit code and how the unit behaves. A missing `git` fails before the
preflight is ever reached: `repoRoot` shells out to `git rev-parse
--show-toplevel` to find the checkout root, that exec fails, and the
process exits 1 (`repoRoot`, `cmd/launcher/daemon/main.go`) — a code
`Restart=on-failure` does bounce, so the unit restarts five times
thirty seconds apart and then parks in `failed`. A missing `nix` gets
further: resolving the tip is git's work and succeeds, `doctor` then
cannot be run at all, the startup preflight refuses the start
with a `doctor-seam-error` reason, and the process exits 11
(`startupPreflight`, same file) — before any slot, any claim, any
Box. `RestartPreventExitStatus=11` above holds that one down, so it
dies once at startup and stays dead. Either way nothing is ever
dispatched, and neither exit code names the cause on its own: read the
diagnostic off the unit's journal output, which names the seam that
failed.

`EnvironmentFile` is where forge credentials belong — a 0600 file
readable only by the operator. The `-` prefix is deliberate: it makes a
missing file the *daemon's* error rather than the service manager's, so
the startup preflight runs and `doctor` names the missing credential in
the journal, instead of systemd failing the unit before `ExecStart` with
nothing but its own "failed to load environment files". Either way the
unit does not come up; only one of the two says why. That refusal is an
exit 11 too, and staying down is the expected shape of it, not a bug.

stdout is the JSON-lines event stream (**Event stream** below); systemd
captures it into the journal as-is, with no format of its own to
configure.

Run `loginctl enable-linger <user>` before installing this unit over
SSH: without it the user manager — and this unit with it — is torn down
at the last session's logout and never starts at boot, so an unattended
overnight pool installed this way is silently gone by the next morning.

Which exits mean stop and which mean restart, for the unit above — see
the daemon *process*'s own exit-code table above for what each code
means:

| exit | policy |
|------|--------|
| 0    | stop — a clean stop is never worth restarting |
| 10   | stop — self-update is opt-in, see below to turn it on |
| 11   | stop — a refusal no restart can clear |
| 1    | restart, rate-limited |

`Restart=on-failure` restarts every non-zero exit; the two codes in
`RestartPreventExitStatus=` carve 10 and 11 back out of it, which leaves
exit 1 as the only code the unit actually bounces. Exit 1 covers both the
transient (the pool-wide breaker tripping after an outage failed one
iteration boundary's fetch after another) and the permanently
unrecoverable (a refused checkout lock, an unparseable
`DAEMON_AWAKE_WINDOW`, a `WorkingDirectory` that is not a git checkout, a
`git` the unit's `PATH` does not reach),
and nothing in the exit code tells those apart — which is what
`RestartSec=` and the `StartLimit*` pair are for. Five restarts thirty
seconds apart absorb a transient; a permanent one exhausts the burst and
systemd parks the unit in `failed`, where `systemctl --user status` shows
it, rather than looping unwatched until morning.

Size `TimeoutStopSec` against the real drain bound, not the optimistic
one: the in-flight Box's runtime *plus that Box's settle*, never the Box
runtime alone. The sizing paragraph under **Halting** above works that
ceiling out; the short form is that systemd's `TimeoutStopSec=90s`
default sits far below even the floor of it, so a unit left at the
default `SIGKILL`s mid-drain and strands exactly the work this design
exists to protect. `TimeoutStopSec=infinity` above is the honest setting.
An operator unwilling to wait the ceiling out escalates instead of
shortening the timeout, by the `systemctl kill` route **Halting**
describes.

**Self-update by choice.** Two edits to the unit above, not one: drop
`10` from `RestartPreventExitStatus=`, and add a line that advances the
checkout before each start.

```
ExecStartPre=/absolute/path/to/git pull --ff-only origin main
RestartPreventExitStatus=11
```

`RestartPreventExitStatus=11` above must *replace* the unit's existing
`RestartPreventExitStatus=10 11` line, not sit alongside it: systemd merges
repeated assignments of this directive rather than replacing them, so a
pasted second line leaves `10` still exempted and self-update silently
never happens (an empty `RestartPreventExitStatus=` assignment is what
resets the list, for a unit that would rather reset than edit in place).
`origin main` is the remote and the branch `BASE_BRANCH` names, and the
checkout has to have that branch checked out — on any other branch the
fast-forward refuses and the unit never starts.

`ExecStartPre` is the half a hand-written unit is likeliest to be missing,
and without it the composition does not self-update at all: the self-change
halt compares the running daemon's own build against an evaluation at the
*fetched* tip, while `ExecStart`'s `.#daemon` builds whatever the
checkout's working tree holds — and the daemon fetches, never pulls. A
restart on its own therefore rebuilds the identical store path, mismatches
the fetched tip again, and exits 10 again having dispatched nothing: a
restart loop, not an update. The `ExecStartPre` is what moves the tree, so
the next start is the build that just merged.

That composition stays opt-in for a second reason beyond the one
**Self-change halt** gives: it needs a checkout the unit owns, and the two
ways an operator's own checkout breaks it break it differently. Local
commits make the fast-forward refuse outright, which fails `ExecStartPre`
with git's own exit status — the daemon never starts, and no exit 10 is
involved. A dirty tree the fast-forward does accept never matches an
evaluation at a clean commit anyway, so a daemon started from one does
reach its first iteration boundary and halts at 10 there, however the
restart policy is written. Point the unit at its own checkout if the
operator also wants one to edit in.

**Awake window.** `DAEMON_AWAKE_WINDOW` (default empty, `lib/env-schema.nix`)
names a daily local-time span the daemon may start a new Box in, as
`HH:MM-HH:MM IANA-zone` — e.g. `22:00-06:00 Europe/London` starts Boxes
only overnight London time. Empty (the default) means always awake: no
window is configured, so nothing gates a start. An end before the start
wraps past midnight — the window is one continuous span from start,
through midnight, to end, never two separate spans — so "run while I
sleep" is expressible in one clause.

The window gates only *starting* a Box: `runSlot`
(`cmd/launcher/internal/daemon/loop.go`) calls `pool.awaitWindow` once at
the top of each iteration, before it even resolves a revision to run
against, and checks it once more when that revision comes back — a fetch
against a remote can outlast the decision that started it, and a slot
that cleared the gate just before the close must not start a child on
the far side of it. Once a child is running, the window closing never
touches it — the child runs to completion, and so does its settle, so
closing the window never throws away work already paid for in tokens and
wall-clock.

Outside the window, `awaitWindow` parks a slot in one `clk.Sleep` straight
through to the next opening (`Window.Until`) rather than polling through
it, so a shut window costs nothing beyond that single sleep. A
none-dispatchable wait (exit 3) whose child finishes to find the window
already shut skips both its idle-backoff step and the mid-wait resolution
`idleSleep` would otherwise ask for, and parks instead — an operator
reading the stream sees no `idle`/`jam` event for that iteration, only the
`awake_close`/`awake_open` pair (below).

The zone is explicit and never inherited: the literal zone `Local` is
rejected by name, since silently falling back to the host's own zone is
exactly the inheritance this knob exists to prevent.

The window is validated in three places, each worth a different amount of
trust. `lib/awake-window.nix` rejects a malformed value at Nix evaluation
time, where the knob is declared — but an ambient `DAEMON_AWAKE_WINDOW`
env var bypasses that check entirely, so the daemon's own startup
`ParseWindow` (`cmd/launcher/internal/daemon/awake.go`) is the actual
guarantee: it fails with a clear `daemon: DAEMON_AWAKE_WINDOW: ...`
diagnostic rather than falling back to always-awake or any other default.
And Nix cannot load the IANA zone database, so the Nix-level parse only
checks the zone token's shape — only the runtime parse proves the
configured zone actually resolves.

Membership is wall-clock, so a DST transition inside the window changes
its real elapsed length: an overnight `22:00-06:00 America/New_York`
window spans a real 7 hours across the March spring-forward night (the
clock skips an hour) and a real 9 hours across the November fall-back
night (the clock repeats one), never the naive 8 either way.

**Event stream.** The daemon writes one JSON object per line to stdout — a
JSON-lines stream, so a service manager captures the run's history without
the daemon owning a log format or a rotation policy. stdout is the machine
stream only; human-facing output and the child's own stdout/stderr go to
stderr instead. Event names and fields (`cmd/launcher/internal/daemon/events.go`,
`loop.go`). The stream is the history; the **Status file** (above) is the
present — a reader wanting only "what is happening right now" reads that
file instead of replaying the stream from the top:

Every per-slot event — `child_start`, `box`, `settled`, `child_finish`, `idle`, `jam`,
`tip_moved`, `backoff`, `breaker_trip` — carries a `slot` (0-based, the pool slot the
event belongs to); `breaker_trip`'s `slot` is whichever slot's failure was
the one that crossed `BreakerThreshold`, since the breaker itself counts
across the whole pool but the crossing is always attributable to one
slot's failure. `awake_close` and `awake_open` are pool-wide transitions,
not per-slot events — however many slots park on one closing, the stream
carries exactly one `awake_close`, and `awake_open` never appears without
a preceding `awake_close` — but each still carries a `slot`: whichever
slot happened to observe the transition. `baton_hold` and `baton_pass` are the
discovery-baton pair (see **Discovery baton** above): both carry a `slot`,
`baton_hold`'s naming the slot that parked waiting for the baton and
`baton_pass`'s naming the slot that just gave it up. Unlike the old gate
pair, `baton_pass` is not a once-per-process event: every discovery round
any slot runs ends in a pass, whether or not another slot was waiting on
it, so the stream carries one `baton_pass` per round, not one per process.
`awaitBaton`'s non-blocking receive still means a slot preempted right at
that instant can log its own `baton_hold` after a `baton_pass` it lost the
race to see first, so the pairing is not a strict ordering. Reading a wait
out of the stream is still mechanical: a slot's `baton_hold`, then a
`baton_pass` naming the slot that handed the baton on and why, then that
waiting slot's own `child_start` once it finally acquires — the pass
always names the slot giving the baton up, never the slot receiving it, so
the receiver is identified by the `child_start` that follows, not by the
pass event itself. `halt` and `shutdown` are the only events with no `slot` at
all, since neither belongs to one slot: `halt` is the pool-wide exit, and
`shutdown` is emitted from `announceStop` (`cmd/launcher/daemon/main.go`),
which runs outside every slot's own goroutine.

| event | fields | when |
|-------|--------|------|
| `awake_close` | `time`, `kind`, `slot`, `wait`, `reason` | the first slot parks on a shut Awake window — not the close instant itself, so a pool still busy at the close reports the transition, and computes `wait` (how long until the next opening), at that later parking |
| `awake_open` | `time`, `kind`, `slot`, `reason` | the Awake window reopens after a prior `awake_close`; never emitted for a daemon that starts inside an already-open window |
| `baton_hold` | `time`, `slot`, `reason` | a slot reaches `awaitBaton` (`pool.go`) and finds another slot still discovering, so it parks — one event per park, since each parking slot logs independently on its own pass through the loop; `reason` is the fixed `batonHoldReason`, `"waiting for the discovery baton: another slot's child is still discovering"`, matching the other wait events in this stream |
| `baton_pass` | `time`, `slot`, `reason` | a slot's discovery round ended and it handed the baton on; `slot` is always the passing slot, never the slot about to receive it (see the events prose above) — a single-slot pool (`MAX_PARALLEL=1`, see **Pool** above) emits neither `baton_hold` nor `baton_pass`, since it has no sibling to stagger against. `reason` names whichever release path fired, one of `pool.go`'s `batonPass*` consts: a live claim while the holder's child is still running (`batonPassClaimed`, `"the holder's child announced a Box: discovery is over, passing the baton to the next waiting slot"`, fired the instant the child's `box` record arrives via `OnRecord`, not at child exit), the holder's child returning having announced nothing — queue empty, none dispatchable, an unrecognised exit, or a `RunChild` seam error (`batonPassChildEnded`, `"the holder's child ended without announcing a Box: passing the baton to the next waiting slot"`), an unclassified failure reaching `backoffOrHalt` before the holder ever started a child (`batonPassFailed`, `"the holder's round failed before it could start a child: passing the baton rather than holding the pool through its backoff"`) — reachable only for the pre-assigned initial holder's (`leadSlot`) very first round, since the baton is now acquired after the resolve that can produce this failure — the Awake window shutting between the holder's fetch and starting its child (`batonPassWindowClosed`, `"the Awake window closed before the holder could start a child: passing the baton rather than holding the pool through the shut span"`), `pickKind` finding no runnable kind for the holder (`batonPassIdle`, `"no kind is runnable for the holder: passing the baton rather than holding the pool through its idle wait"`) — likewise reachable only for that same initial round, since the baton is acquired after `pickKind` runs — or the holder returning for any reason at all before its round otherwise resolved (`batonPassStopped`, `"the holder stopped before its discovery round resolved: passing the baton so no sibling waits on a slot that has already exited"`, the deferred catch-all in `runSlot`) |
| `preflight` | `time`, `revision`, `exit`, `outcome` (`reason` instead of `exit` on the paths with no doctor exit code to report — a seam failure, or an operator's stop) | emitted exactly once, at startup, before the first slot, on every path the preflight can take: a pass, a refusal, a seam failure, or an operator's Ctrl-C — so "ran and was healthy" and "never ran" cannot look identical the morning after; `outcome` is `ClassifyPreflight`'s own `doctor-`-prefixed label (`doctor-healthy`, `doctor-required-labels-missing`, `doctor-config-invalid`, `doctor-connectivity`, `doctor-unclassified`, `doctor-unknown`) or one of the two the daemon itself adds on the paths that never reached a verdict (`doctor-seam-error` for a failure resolving the tip or running doctor at all, `doctor-cancelled` for a stop signal during the preflight), never `Interpret`'s child-outcome vocabulary, so it can never be confused with a `child_finish` outcome |
| `child_start` | `time`, `kind`, `revision`, `slot` | just before a child is launched; carries no `issue` — the daemon cannot know which issue a freshly started child will work until it reports a `box` record, and waiting on that would either hide the child from the stream for its whole queue scan or, for a child that never claims, emit nothing at all |
| `box` | `time`, `kind`, `issue`, `phase`, `revision`, `slot` | once per Box the child reported over the report pipe (`initial`, `fix-pass-N`, `conflict-resolve`) — not once per issue: a fix pass and a conflict-resolve for the same issue each get their own row, distinguished by `phase`, and this is how the revision a given Box ran at is recovered later |
| `child_finish` | `time`, `kind`, `issue`, `revision`, `exit`, `outcome`, `slot` | after the child exits; `issue` is whichever issue the child last claimed (the last `box` record's issue), or absent if it claimed none. `outcome` is the same stable label `Interpret` maps the exit code to (`dispatched`, `queue-empty`, `none-dispatchable`, `image-stale`, `host-tainted`, `config-invalid`, `signalled-stop`, `error`). `signalled-stop` is exit 7's label whichever way the pool then acts on it — it no longer by itself implies a halt: the pool halts on it only while the operator's Stop latch was already closed, and otherwise backs that slot off like any other unclassified failure (see **Failures** above). `exit` is omitted on the one path with no exit code to report — the seam itself failing (the child could not be started, or its wait failed with no exit status), which always carries `outcome: "error"` |
| `settled` | `time`, `kind`, `issue`, `state`, `note`, `revision`, `slot` | emitted once per issue, at the end of that issue's settle path — never live at each terminal transition, so `state` carries the issue's last word, never two contradicting rows. Only the dispatch settle path defers through a `flushSettled` latch (issue #3627), since it alone can reach a terminal state twice for the same issue (a green `completeLanding` that `verifyMerged` later demotes to Failed); the research settle path and `recoverFailed` each reach exactly one terminal state per issue and emit inline, already once. `state` is the launcher's own dispatch-state vocabulary (`complete`, `failed`, `recoverable`, `ambiguous`), never `child_finish`'s `outcome` vocabulary above, and the two must not be confused: a `child_finish` reports how the *child process* ended, a `settled` reports what the *issue* ended up at, and the two can disagree (a child can exit `dispatched` for an issue that itself settles `failed`). `note` carries the settling site's own reason wherever one is live — the research path's `"no verdict comment block"` was the original motivating case, and the dispatch settle path now fills it too (the blocked/ambiguous outcome's own note, the unresolved path's classification detail, `verifyMerged`'s demotion reason, the recoverable reason) rather than always passing `""`: with the daemon's terminal gone, that reason previously survived nowhere on disk |
| `idle` | `time`, `kind`, `wait`, `slot` | recording a no-work result against `kind` after `queue-empty`, or after `none-dispatchable` with a sibling slot `resolving`, `running`, or `backing_off`; `wait` carries `kind`'s own idle backoff, so a widening `wait` across successive `idle` events for the same `kind` is how that kind's growing backoff reaches the stream |
| `jam` | `time`, `kind`, `revision`, `slot`, `wait`, `reason` | recording a no-work result against `kind` after `none-dispatchable` with every sibling slot `idle` or `awaiting_window` — nothing running, resolving, or backing off anywhere else in the pool, and nothing dispatchable, worth reporting loudly since only an operator (merging a blocker, relabelling an issue) clears it; `wait` carries `kind`'s own idle backoff, same as `idle` above |
| `tip_moved` | `time`, `revision`, `slot`, `reason`, `kinds` | any resolve that finds `BASE_BRANCH`'s tip differs from the revision the Runner most recently handed out — the baseline is Runner-global, not per-slot, so it tracks whichever slot resolved last, whichever kind it was running. That resolve can be the ordinary one made after `pickKind` on any iteration, or the opportunistic one made mid-wait during a jammed idle sleep (there is no separate poll — see exit 3's row above); either way the slot reset every currently-jammed kind's backoff, and, on the mid-wait path, started its next iteration at once instead of sleeping out the rest of the wait. `kinds` names that reset set; no singular `kind` is stamped, since several kinds can be jammed at once and a moved tip is evidence for all of them, not whichever kind this slot happened to be running when it went to sleep — `reason` carries the prose explanation. `revision` is the new tip, not the stale one the child ran at. Worth reading as the explanation for a `jam` (or `idle`) with a long `wait` immediately followed by a `child_start` well before that wait could have elapsed |
| `backoff` | `time`, `kind`, `revision`, `slot`, `wait`, `reason` | a slot backing off for `FailureBackoff` after an unclassified failure, before it refills itself; `reason` is prefixed by cause — a failed fetch, a failed child seam, an unrecognised exit code, and now a failed self-build evaluation too (`self-build: …`, see **Self-change halt** above) |
| `breaker_trip` | `time`, `kind`, `slot`, `failures`, `wait` | the pool-wide breaker reached `BreakerThreshold` unclassified failures within `BreakerWindow`; `slot` names whichever slot's failure crossed the threshold, `failures` is the count that tripped it (always exactly `BreakerThreshold` — only one crossing is ever reported), `wait` carries the breaker window, and a `halt` follows unless a sibling slot had already halted the pool for its own reason |
| `shutdown` | `time`, `reason` | the signal handler consumed a stop signal; `reason` is `signalled stop: forwarding a drain request to every running child` for the first signal and `second signal: forwarding the escalation so every child reaps and releases` for the second — a third and later signal is a no-op the handler never sees, so `shutdown` never appears more than twice in one run |
| `halt` | `time`, `kind`, `reason`, and `revision` when a child was involved | the process is about to exit — the loop is returning, or, for `instance-lock:`/`preflight:`, never started; `reason` is prefixed by cause, now including `instance-lock: …` (a second daemon found this checkout's lock already held, see **Instance lock** above), `preflight: …` (the startup doctor preflight refused the start, see **Startup preflight** above — a preflight cancelled by an operator signal instead carries the same `context-cancelled: …` reason a context cancellation anywhere else in the loop does), and `self-changed: …` (the daemon's own build changed at the fetched revision, naming both store paths and the revision, see **Self-change halt** above) alongside a halt-mapped child outcome, a context cancellation, a tripped breaker, or an invalid startup config |

**How `box` and `settled` reach the stream.** The child (a Launcher process,
whether dispatch or research) does not write these two events to its own
stdout — stdout already belongs to the Box and its subprocesses, and a
daemon scraping human-facing output for machine facts is exactly the
fragility this event stream replaced. Instead the daemon opens a pipe per
child before it starts one, hands the write end down as the child's first
extra descriptor (`exec.Cmd.ExtraFiles`' first entry, which always lands at
fd 3), and names that same descriptor in the child's environment as
`SPINDRIFT_REPORT_FD` (`cmd/launcher/internal/report`). This is daemon-to-child
plumbing, not a knob: no operator sets it, it appears in no knob table above,
and a launcher run by hand sees it unset and writes
nothing — the reporting package is silent unless a daemon put a pipe there
first. The child checks by `fstat` that the descriptor really does name a
pipe and refuses — one line to stderr, no crash — if it names anything
else; `FromEnv` accepts any `S_IFIFO` descriptor, so this catches a
misconfigured fd pointing at a file or socket, not a fd spoofed as a pipe.
The child never opens the descriptor itself — it inherits it via
`exec.Cmd.ExtraFiles` — but `mainRun` marks it close-on-exec at the top of
the function, before this process spawns its first Box, runtime, or
subprocess, so none of them can inherit or hold the write end open. With
the write end no longer shared with anything the child launches, the
child's own stdout is free to be
relayed to the daemon's stderr byte-for-byte, with no userspace scan or
copy in between — what the daemon's log shows is exactly what the child
printed, unmodified by this event stream sitting alongside it on a
different descriptor. On the read side, an unknown record event is ignored
with no error, and a malformed line is reported once to stderr and skipped
rather than aborting the read loop — so a child built from a newer source
tree than its daemon, reporting an event type the daemon doesn't recognise
yet, degrades to silently dropping that one record rather than crashing the
daemon's read loop (in practice the two are built from one source tree and
change atomically, so this is a safety margin, not an expected occurrence).
One record is one line, and a line is bounded: the reader will not buffer
past `report.MaxLine` bytes (newline included) before discarding a line, so
the writer clips the record's only unbounded field, `note`, to fit — a
trailing `…` marks a reason that was cut. That bound is one shared constant
rather than a number each side declares for itself, because a `settled`
record is most valuable exactly when its reason is longest (a Box's
multi-kilobyte `status=blocked note=…`), and a reason too long to carry must
cost the tail of the note, never the whole record.
The doctor preflight (**Startup preflight**, above) gets neither the
variable nor the descriptor — it dispatches nothing and settles no issue,
so it has nothing to report.

**What this first cut doesn't do.** The instance lock, the queryable status
file (issue #3545, above), the self-change halt (issue #3543, above) and
per-kind backoff are all in now, alongside the
pool concurrency this daemon does own (`MAX_PARALLEL`, above) and does
override the Consumer's own
`dispatch.maxJobs`/`dispatch.maxParallel` for every child it starts — each
child is pinned to exactly one Box (`--max-jobs 1 --max-parallel 1`, see
**Pool** above) regardless of the Consumer's configured wave size, since
the daemon, not any one child, is what now decides how many Boxes run at
once.

Continuous dispatch (`CONTINUOUS_DISPATCH`, above) is deprecated in the
daemon's favour — the daemon is its replacement as the way to hold a pool
of Boxes — but not removed: the knob still works for an operator who wants
no daemon at all, and it remains the Console's engine unchanged, see
**Deprecated** under [Continuous dispatch](#continuous-dispatch).

## Shell completion

`spindrift` ships bash, fish, and zsh tab-completion, generated from the same
schema as `--help` and the man page: subcommands (`dispatch`, `research`,
`preview`, `build`, `recover`, `doctor`) complete as the first word, every
flag (including the `--issue` alias and the secret `--*-file` flags) completes
anywhere after it, a `--*-file` flag's argument completes as a filesystem path,
and an enumerable flag's argument (`--merge-policy`, `--forge-backend`,
`--tracker`, `--overlap-gate`) completes to its fixed set of legal values
(e.g. `--merge-policy <TAB>` offers `immediate auto manual`).

`dispatch`, `preview`, and `recover` additionally complete their positional
issue-number argument dynamically: `spindrift dispatch <TAB>` queries the
configured issue tracker (honoring the effective `LABEL`, `REPO_SLUG`, and
`ISSUE_TRACKER`) and offers the numbers currently in the dispatchable queue —
zsh and fish annotate each with its title, bash offers the bare numbers.
`build` and `doctor` take no issue argument and complete none. The query goes
through a hidden `spindrift __complete-issues` sub-verb (not a documented
subcommand — it exists solely for the completion scripts to shell out to) and
is bounded by a short timeout: a slow, offline, or erroring tracker yields no
candidates instead of blocking or erroring your shell mid-`<TAB>`.

**bash.** `nix develop` puts the completion script on
`share/bash-completion/completions` under `spindrift`'s store path; source it
directly to enable it in your shell:

```sh
source "$(dirname "$(command -v spindrift)")/../share/bash-completion/completions/spindrift"
```

**fish.** Same coverage as the bash slice plus a one-line description on every
flag. `nix develop` puts the completion script on
`share/fish/vendor_completions.d` under `spindrift`'s store path; fish's
`vendor_completions.d` convention loads it automatically once that directory is
on `$fish_complete_path`, or copy/symlink it into
`~/.config/fish/completions/spindrift.fish`.

**zsh.** Same coverage (including value completion for enumerable flags), plus a
per-flag description drawn from the same `doc` string, so `spindrift --<TAB>`
shows each flag's one-line purpose alongside its name. `nix develop` puts the
completion function on `share/zsh/site-functions/_spindrift` under `spindrift`'s
store path; add that directory to `fpath` before `compinit` runs:

```sh
fpath=("$(dirname "$(command -v spindrift)")/../share/zsh/site-functions" $fpath)
autoload -Uz compinit && compinit
```

## Unattended runs

For a standing pool that keeps working the queue unattended, the daemon is the
recommended path — see [Daemon](#daemon), and **Service unit** there for the
systemd unit, the restart policy, and how to size the stop timeout. The
scheduled-invocation shapes below suit a bounded, one-shot wave instead.

`spindrift dispatch` is just a command, so wrap it however you schedule things —
`cron`, `launchd`, a systemd timer, or a CI job on a Linux runner (where the
image builds with no Linux-builder dance). In non-interactive contexts invoke the
CLI by its store path or via `nix run .#default -- dispatch` rather than relying
on a dev-shell PATH.
