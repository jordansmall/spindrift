# spindrift reference

Full technical reference for the spindrift harness. For first-time setup see
the [README](../README.md); for vocabulary see [`CONTEXT.md`](../CONTEXT.md).

---

## The `spindrift` CLI

`nix develop` (or `direnv allow`) puts a single `spindrift` binary on your PATH
— it is the primary surface, and everything runs through it:

| command                          | what it does                                                                    |
| -------------------------------- | ------------------------------------------------------------------------------- |
| `spindrift dispatch`             | launch one container per `ready-for-agent` issue, in dependency waves          |
| `spindrift dispatch 42 57`       | dispatch exactly these issues, bypassing the label/barrier gates                |
| `spindrift dispatch --no-build`  | fail fast if the image is absent instead of building it first (split build/run) |
| `spindrift dispatch --yes`       | skip the confirmation prompt when dispatching unlabeled issues (alias `--force`)|
| `spindrift research`             | advise-only research dispatch: launch one container per `agent-research` issue, post a verdict comment, apply the terminal label — see [Research dispatch](#research-dispatch) |
| `spindrift research 42 57`       | research exactly these issues, same selective semantics as `dispatch <nums>`    |
| `spindrift preview [issue...]`   | dry run: show what `dispatch` would pick up, and the wave ordering               |
| `spindrift build`                | realize/load the agent image (or store closures) without running any agent      |
| `spindrift recover <issue>`      | re-run the merge gate for one issue (adopt a stranded `agent-in-progress`)       |
| `spindrift doctor`               | check forge credentials, repository connectivity, and label presence — the four triage labels (fatal if missing) and the six `agent-research*` labels (ADR 0022, advisory only); when run interactively (TTY attached) and labels are missing it offers to create them with default colors and descriptions; in CI (no TTY) it reports missing labels and exits non-zero only if a triage label is missing |
| `spindrift reconcile`            | local-tracker bookkeeping sweep: close issues whose recorded `landing` PR merged (ADR 0029) — a clear no-op on `github`/`jira`; also auto-invoked at the end of a `dispatch` run when `ISSUE_TRACKER=local` — see [`reconcile`: closing a local issue](#reconcile-closing-a-local-issue) |
| `spindrift --help`               | concise usage: subcommands, common flags, and pointers to the full reference    |
| `spindrift --help --all`         | the full flag reference, grouped by category (same content as `man spindrift`)  |
| `man spindrift`                  | the manual page (installed alongside the binary on your PATH)                    |
| `spindrift --version`            | installed version and revision                                                  |

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
would.

| option      | scope          | type                        | default            | meaning                                                              |
| ----------- | -------------- | --------------------------- | ------------------ | -------------------------------------------------------------------- |
| `nixpkgs`   | shared         | flake input                 | your `nixpkgs`     | locked nixpkgs the image and host commands build from                |
| `system`    | **auto-supplied** | string                   | perSystem's system | your host system; mapped to its Linux twin for the image; see the note above (`lib/flakeModule.nix`) |
| `overlays`  | shared         | list                        | `[]`               | overlays applied to the instantiated nixpkgs                         |
| `config`    | shared         | attrs                       | `{ allowUnfree = true; }` | nixpkgs config attrs                                          |
| `packages`  | shared         | `pkgs -> [pkg]`             | `[]`               | project build/test tools baked into the image (the toolchain surface)|
| `prefetch`  | shared         | shell snippet               | `""`               | runs in the work tree after the clone, to warm dependency caches     |
| `prompt`    | shared         | string                      | bundled starter    | agent prompt template baked into the image; changing it requires a rebuild (`spindrift build`). The SPINDRIFT_OUTCOME contract is harness-owned: `spindrift build` appends it automatically if a custom `prompt` omits it (idempotent — a prompt that already has it is untouched) |
| `scoutPrompt` / `reviewPrompt` / `filerPrompt` | **`mkHarness` only** | string | bundled starters | system prompts for the read-only scout and reviewer subagents and the opt-in filer subagent (see [Filer](#filer)); not settable on `perSystem.spindrift.*` — override at runtime via `SPINDRIFT_PROMPT_DIR` regardless of which caller baked the image |
| `skills`    | shared         | list of path/derivation/`{ name; src; }` | `[]`  | skills baked into the image at `/home/agent/.claude/skills`, each as a `<name>/SKILL.md` directory (the only layout Claude Code discovers — a flat `<name>.md` is ignored) so the headless agent can `/invoke` them; a `{ name; src; }` content entry (name + SKILL.md body) is realized with the image's own Linux `pkgs` rather than copied from a pre-built host derivation, keeping the agent-image drvPath host-independent (issue #597); `SPINDRIFT_SKILLS_DIR` mounts over them at runtime |
| `settings`  | shared         | submodule, grouped by section (see below) | `{}` | non-secret run defaults baked into the `spindrift` CLI |
| `runtime`   | shared         | `"podman"` \| `"docker"` \| `"rancher"` \| `"bwrap"` | `"podman"` | runner the `spindrift build`/`dispatch` commands drive: an OCI runtime (`"rancher"` is an alias for Rancher Desktop's containerd mode, driven via `nerdctl`), or the daemonless bubblewrap sandbox (`bwrap`, Linux-only, no image build/load) |
| `driver`    | shared         | string                      | `"claude"`         | the agent CLI Driver baked into the image and threaded to the launcher (ADR 0009); `"claude"` is the only Driver today |
| `nixInBox`  | shared         | bool                        | `true`             | bake a usable nix (binary + registered store DB + sandbox-off config) into the box so `nix flake check` / `nix develop` work inside it; set `false` for a lean, nix-free image (ADR 0008) |
| `nixStoreWritable` | shared  | bool                 | `false`            | self-test mode (ADR 0018): make `/nix/store` itself (not its existing contents) agent-writable so in-box `nix flake check` can substitute/build new paths instead of hitting EACCES; new paths live only in the container's ephemeral copy-on-write layer. Not hermetic — the entrypoint prints a loud `==> WARNING`; OCI runners only, the bwrap runner keeps its read-only store bind |
| `extraClosures` | shared     | `pkgs -> [pkg]`         | `[]`               | extra derivations, as a function of the (Linux) `pkgs` (like `packages`), whose closures are baked into the image and registered in the store DB alongside the runtime closure, so in-box nix sees them as already present (ADR 0018) |
| `nixBuilderImage` | **`mkHarness` only** | string        | `"docker.io/nixos/nix@sha256:bf1d938835ab96312f098fa6c2e9cab367728e0aad0646ee3e02a787c80d8fb8"` | Nix image `spindrift build` uses as a fallback Linux builder when the host can't realize the image; pinned by digest for supply-chain safety (see [Building on macOS](#building-on-macos)) |

The `settings` submodule bakes run knobs into the Launcher input document the
`spindrift` CLI passes to the launcher binary via `--input`; an explicit
`--flag` at dispatch time re-points a value without a rebuild. Knobs are
grouped by section — the same headings as `spindrift --help --all` — so the
flake surface is self-documenting and stays in lockstep with the CLI help.
Sections and knobs derive from `lib/env-schema.nix`; unknown section or knob
names are rejected at eval time by the NixOS module system.

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
  concurrency     = { maxParallel = 3; maxJobs = 0; };
  models          = { model = "claude-sonnet-5";
                      scoutModel  = "claude-haiku-4-5-20251001";
                      reviewModel = "claude-opus-4-8";
                      filerModel  = ""; };
  sandbox         = { devShellName = "default"; devShellProbeTimeout = 300;
                      memoryLimit = "5g"; pidsLimit = "512";
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
same way; each is composed into `--agents` independently by its own knob, so
emptying one drops only that subagent, never both. `filerModel` is the same
shape but opt-in — empty by default, so the filer is not provisioned at all
until a model is set; see [Filer](#filer).

The **prompt is baked into the image**: changing `prompts/issue-prompt.md`
requires an image rebuild (`spindrift build`). Point `SPINDRIFT_PROMPT_DIR`
at any directory to override it at runtime for zero-rebuild iteration.

Opt-in prompt steps (the skill preamble, the caveman-default narration
directive, `FILE ISSUES`, `AUTO-FORMAT`, `AUTO-LINT`, `CI FAILURE`, and the
`OPEN A PULL REQUEST` ticket-reference line) are each one row in a nix-owned
**Conditional fragment registry**
(`lib/fragments.nix`) — a gate variable, a fragment file under
`prompts/fragments/`, and a substitution variable — rendered into the
entrypoint's single fragment loop and its substitution allowlist together,
so a fragment can never reference a variable the substitution step doesn't
know about. Adding an opt-in prompt step is a nix-only change: one registry
row plus one fragment file, no entrypoint edit. All instruction prose —
conditional or not — lives with the rest of the prompt surface rather than
as heredocs in the entrypoint script. `SPINDRIFT_PROMPT_DIR` therefore
overrides fragments the same way it overrides `prompts/issue-prompt.md`
itself: a directory that enables a knob (`AUTO_FORMAT`, `AUTO_LINT`, a filer
model, etc.) must ship the matching `fragments/*.md` file, exactly as it
already must ship `filer-prompt.md` when the filer is configured — the
entrypoint reads the fragment unconditionally once its gate is on, with no
baked-in fallback.

The `OPEN A PULL REQUEST` ticket-reference line is the one row with three
mutually exclusive fragments (`pr-body-closes.md` / `pr-body-local-ref.md` /
`pr-body-local-noref.md`) instead of one on/off pair: `ISSUE_TRACKER` and
`LOCAL_ISSUE_REFERENCE` together pick exactly one gate, so issue-prompt.md
concatenates all three substitution variables and only the active one ever
renders — see [Local issue tracker](#local-issue-tracker-issue_trackerlocal)
for the three cases.

The caveman-default step is keyed on the baked skill itself rather than a
separate knob: whenever `DRIVER_SKILLS_DIR/caveman/SKILL.md` is present at
runtime, both the issue pass and the fix pass (via the shared COMMS block,
see below) direct the agent to use `/caveman` for narration and prose,
exempting code, commands, error messages, and commit messages. A Consumer
that never bakes the skill gets a prompt with zero mention of it.

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
  to `$HOME`. Required; the harness bakes skill files there and the runner
  adapters mount `SPINDRIFT_SKILLS_DIR` overrides over the same path.
- `sessionCacheDirRelative` — where the agent CLI's session transcripts
  live, relative to `$HOME`. Optional; a Driver that omits it has no
  resumable session state (see above).

The registry validates every entry against this required-attribute list at
eval time (`assertShape` in `lib/drivers/default.nix`): an entry missing one
of the required fields above fails the build with a message naming the
Driver and the missing attribute, before an image is ever produced.
`sessionCacheDirRelative` is the one field this check treats as optional.
Cross-half parity with the Go registry (`cmd/launcher/internal/driver`)
stays name-only by design (ADR 0009) — each half enforces its own entries'
completeness independently.

The registry also owns rendering: `renderPreamble` turns a validated entry
into the `DRIVER_*` variable block (`DRIVER_BIN`, `DRIVER_FLAGS_COMMON`,
`DRIVER_SKILLS_DIR` — the last baked as an absolute path under
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
*host*-side half: the image bake pre-creates each declared directory
agent-owned (so podman/bwrap never fabricate a root-owned parent when the
launcher mounts over it), and both are exported as absolute paths
(`DRIVER_SKILLS_DIR`, `DRIVER_SESSION_CACHE_DIR`, rendered by
`lib/preambles.nix`'s `renderDriverMountPreamble` — a separate renderer from
the registry's own `renderPreamble` above, consumed by the launcher wrapper
process rather than the in-box entrypoint) that the Go launcher's OCI and
bwrap adapters mount over — no Driver-specific path literal lives in the
runner adapters or the image staging step.

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
      systems = [ "aarch64-darwin" "aarch64-linux" "x86_64-linux" ];
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
   env rather than dying. That relaunch logs one line to the box's own
   stderr; it's an in-box observability detail, not itemized here alongside
   the operator-facing behavior.
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
spindrift-pat"`) in `harness.env`, your shell, or direnv, or pass a one-off
`--<secret>-cmd` flag; the mechanism is tool-agnostic and works with `rbw`,
`op`, `pass`, `vault`, or any command that prints the secret on stdout. The
launcher execs the command and captures its stdout into memory, trimming
a trailing newline the same way `--<secret>-file` does —
the secret never touches disk. Resolution precedence, first non-empty wins:
`--<secret>-cmd` flag > `<SECRET>_CMD` env > `--<secret>-file` flag >
`<SECRET>` direct env — so a `_CMD` variant overrides a direct value, and
migrating to a vault is a matter of adding the command, not first removing
the old value. Supplying both `--<secret>-cmd` and `--<secret>-file` for the
same secret is a configuration error. A failing or empty command aborts the
launch with a named, value-free error; the fetched value is never logged,
baked into the nix store, or written to the launcher input document — only
the command string itself (which reveals a vault item name, not the secret)
may appear in host-side logs. The plaintext direct-value and
`--<secret>-file` forms remain fully supported and are not deprecated; with
this in place, `harness.env` is expected to hold fetch recipes rather than
live credentials. See `spindrift --help --all` for the full
`--<secret>-cmd`/`--<secret>-file` flag list.

| var                       | default                | meaning                                  |
| ------------------------- | ---------------------- | ---------------------------------------- |
| `REPO_SLUG`               | — (required unless `CODE_FORGE` and `ISSUE_TRACKER` are both `local`; baked via `settings.repository.repoSlug`) | target repo, `owner/repo` |
| `GH_TOKEN`                | — (required unless `CODE_FORGE` and `ISSUE_TRACKER` are both `local`) | GitHub token for `gh` inside containers (secret; env only) |
| `GH_TOKEN_REFRESH_FILE`   | — (baked via `settings.repository.ghTokenRefreshFile`) | path the launcher polls to keep `GH_TOKEN` current past an installation token's ~1h lifetime — see [GitHub App installation token](#github-app-installation-token-recommended) |
| `CLAUDE_CODE_OAUTH_TOKEN` | — (one auth required)  | from `claude setup-token` (secret; env only) |
| `ANTHROPIC_API_KEY`       | —                      | alternative to the OAuth token (secret; env only) |
| `GIT_USER_NAME`           | host `git config`; baked via `settings.repository.gitUserName` | commit author name (applied repo-locally inside the Box — see [Hermetic git config](#hermetic-git-config)) |
| `GIT_USER_EMAIL`          | host `git config`; baked via `settings.repository.gitUserEmail` | commit author email (applied repo-locally inside the Box — see [Hermetic git config](#hermetic-git-config)) |
| `CODE_FORGE`              | `github` (baked)       | code-landing backend: `github` (open PR, watch CI, merge), `git` (push-only to `CODE_FORGE_REMOTE_URL`; no PR, CI-watch, or merge gate — see [ADR 0013](../docs/adr/0013-issue-tracker-and-code-forge-are-independent-seams.md)), or `local` (host-mediated landing onto the Accumulation repo's Integration branch; no PR, CI-watch, or network — see [ADR 0033](../docs/adr/0033-host-mediated-local-code-forge.md)) |
| `CODE_FORGE_REMOTE_URL`   | — (required when `CODE_FORGE=git`) | plain git remote URL to clone from and push to (self-hosted git, gitea, GitLab-without-MRs, a bare server repo) |
| `CODE_FORGE_ACCUMULATION_REPO_DIR` | `.spindrift/accum.git` under the launcher's working directory when `CODE_FORGE=local` (auto-created and seeded); an explicit value overrides it | host path to the bare Accumulation repo, mounted read-only into the Box and landed into host-side |
| `BOX_FORGE_AND_ISSUE_ACCESS` | `read-write` (baked)   | a third axis, orthogonal to `CODE_FORGE`/`ISSUE_TRACKER` (issue #1914): `read-write` (the Box writes directly, unchanged) or `read-only` (the Launcher host-mediates every write instead), gated at startup by capability — `read-only` is permitted only when the selected forge implements bundle-relay and host-side draft-PR-create and the selected tracker implements host-posted comments; `local` backends already satisfy the gate, `github` does not yet |
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
| `MERGE_MODE`              | `manual` (baked)       | post-green merge policy: `manual` (leave the green PR for a human), `immediate` (rebase-merge on green), `auto` (enqueue GitHub native auto-merge — repo must have *Allow auto-merge* on). Under `CODE_FORGE=git`, `manual`/`immediate` map to remote pushes instead (leave the pushed branch / push straight to the target branch); `auto` has no meaning off `github` and fails fast at startup. Under `CODE_FORGE=local`, only `immediate` relays the seam bundle into the Accumulation repo — `manual`/`auto` have no meaning under `local` and fail fast at startup. |
| `MERGE_GUARD_PATHS`       | `.github/**,**/CLAUDE.md,**/AGENTS.md,.claude/**,.opencode/**` (baked) | comma-separated globs; a green PR touching a matched path downgrades to manual regardless of `MERGE_MODE` (`github` Code Forge only; empty disables — see [Merge guard](#merge-guard)) |
| `MODEL`                   | `claude-sonnet-5` (baked) | Claude model the in-container implementor runs |
| `SCOUT_MODEL`             | `claude-haiku-4-5-20251001` (baked) | scout subagent model tier (empty drops the scout entry from `--agents`) |
| `REVIEW_MODEL`            | `claude-opus-4-8` (baked) | reviewer subagent model tier (empty drops the reviewer entry from `--agents`) |
| `FILER_MODEL`             | `` (baked)             | filer subagent model tier; empty (default) means the filer is not provisioned — setting a model is the opt-in (recommended: `claude-haiku-4-5-20251001`); see [Filer](#filer) |
| `IMAGE`                   | `spindrift:latest`     | image tag to run                         |
| `SPINDRIFT_PROMPT_DIR`    | baked prompt store path | hot-override the mounted prompt dir (not bakeable) |
| `SPINDRIFT_SKILLS_DIR`    | baked skills store path | hot-override the mounted skills dir (not bakeable) |

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

### Advanced tuning

These knobs are rarely changed. All except `SPINDRIFT_PROMPT_DIR`,
`SPINDRIFT_SKILLS_DIR`, and `ISSUE_NUMBER` can be baked via `settings` (see
[Option surface](#option-surface)) or overridden at dispatch time via
`--flag` (env still works this release too, deprecated — ADR 0020). See
`lib/env-schema.nix` for
the authoritative list.

| var                    | default | `settings` section | meaning                                                |
| ---------------------- | ------- | ------------------ | ------------------------------------------------------ |
| `MAX_JOBS`             | `0`     | `concurrency`      | caps the wave size (`0` = uncapped) |
| `CONTINUOUS_DISPATCH`  | `` (off) | `concurrency`     | opt-in slot-refill dispatch mode: refills each freed slot from a live re-discovery, gated by the image-freshness probe before every launch; exits with a new documented code when the probe finds the loaded image stale (see the [exit-code table](#dogfood-loop)) |
| `MAX_FIX_ATTEMPTS`     | `3`     | `selfHealing`      | fix-box passes when CI is genuinely red before `agent-failed` (`0` disables self-healing) |
| `MAX_REBASE_ATTEMPTS`  | `3`     | `selfHealing`      | rebase-and-retry passes when a green PR conflicts with the base after a sibling merge (`0` disables rebase retries); also caps the opt-in [Stale-base preflight](#stale-base-preflight)'s rebase budget |
| `PREFLIGHT_STALE_BASE` | `` (off) | `selfHealing`    | opt-in: proactively rebase a green-but-behind PR (no conflict) and re-green it before merging — see [Stale-base preflight](#stale-base-preflight); off by default merges a green-but-behind PR as-is |
| `MERGE_POLL_INTERVAL`  | `30`    | `branches`         | seconds between CI-status polls in the merge gate      |
| `MERGE_POLL_TIMEOUT`   | `1800`  | `branches`         | seconds to wait for CI green before abandoning the merge |
| `OVERLAP_GATE`         | `defer` | `concurrency`      | declared `## Touches` overlap policy: `defer` (hold a Dispatchable issue whose declared touch-set intersects an in-progress issue's, retrying once the collider completes) or `off` (disable the check — see [Declared touch-set overlap](#declared-touch-set-overlap)) |
| `TRANSIENT_RETRY_MAX`  | `3`     | `selfHealing`      | retries for transient box exits (529/network backoff; consecutive 429 holds) |
| `TRANSIENT_BACKOFF_SECS` | `30`  | `selfHealing`      | base linear backoff per transient retry                |
| `HOLD_JITTER_SECS`     | `5`     | `selfHealing`      | jitter added to a 429 hold-until-reset before re-dispatch |
| `DEV_SHELL_NAME`       | `default` | `sandbox`        | which devShell to enter; set `ci` to use a lean headless shell distinct from the interactive `default` |
| `DEV_SHELL_PROBE_TIMEOUT` | `300` | `sandbox`        | seconds before the in-box devShell probe is abandoned for the baked toolchain |
| `MEMORY_LIMIT`         | `5g`    | `sandbox`          | per-container `--memory` cap (OCI only; empty disables) |
| `PIDS_LIMIT`           | `512`   | `sandbox`          | per-container `--pids-limit` cap (OCI only; empty disables) |
| `PODMAN_NETWORK`       | —       | `sandbox`          | `--network` value for podman run; set `pasta` to restrict egress |
| `BWRAP_UNSHARE_NET`    | —       | `sandbox`          | non-empty adds `--unshare-net` to the bwrap runner      |

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
             └─ claude -p "<prompts/issue-prompt.md>" --dangerously-skip-permissions
                └─ implement → check → commit → push → self-review (reviewer subagent)
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
           │           auto      → enqueue GitHub native auto-merge
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
`github` and is rejected at startup.

`immediate`'s merge-and-push to `CODE_FORGE_REMOTE_URL` runs on the
**launcher host** (a throwaway local clone, not inside a Box), reusing
`GIT_USER_NAME`/`GIT_USER_EMAIL` as the merge commit's identity. The host
needs its own push credentials for that remote (e.g. an SSH key or
credential helper covering `CODE_FORGE_REMOTE_URL`) — separate from the
Box's `GH_TOKEN`, which only covers the Issue Tracker. A push auth failure
surfaces as a `merge-blocked` comment on the issue, not a crash.

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
  `logs/issue-<n>.log` and re-labels to retry. A fix box's transient exits get
  the same in-session retry as the initial run — a 429 mid-fix-pass holds
  until the reset time and re-dispatches rather than burning one of the
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
  runner, re-running the merge gate on its open non-draft PR.

Rename any of these with the `inProgressLabel` / `failedLabel` / `completeLabel`
knobs under `settings.lifecycleLabels` (baked) or the
`IN_PROGRESS_LABEL` / `FAILED_LABEL` / `COMPLETE_LABEL` env vars (runtime).

#### Issue Tracker backends

Per [ADR 0013](adr/0013-issue-tracker-and-code-forge-are-independent-seams.md),
the Issue Tracker (where issues live) and the Code Forge (where code and CI
live) are independent axes. `ISSUE_TRACKER` selects the tracker; the Code
Forge stays `github` regardless (Jira issues, GitHub PRs).

The [Quickstart wizard](../README.md#quick-start) always provisions
`github`; it never prompts for a tracker. `local` and `jira` are
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
  `settings.repository` / `settings.lifecycleLabels` / `settings.issueDiscovery`
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
does not, however, defend against a fully adversarial Agent. Under the
`github` Code Forge, [the launcher — never the Agent — owns the merge
decision](#how-a-run-works): the Agent must not run `gh pr merge`. What
follows describes what breaks that contract, not what a cooperative Agent
does — the GitHub token that opens the PR is the same token that can merge
it, so an Agent that defies the contract and runs `gh pr merge` against its
own PR bypasses the launcher-side check entirely. See
[ADR 0016](adr/0016-merge-guard-bounds-drift-not-adversaries.md) for that
boundary and the two-actor-separation hard mode.

The guard exists **only on the `github` Code Forge merge path**. The
push-only `git` forge has no launcher in the merge path and therefore no
guard at all.

Configure it via `settings.branches.mergeGuardPaths` (baked) or the
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
exists only on the `github` Code Forge merge path for the same reason the
merge guard does — see [ADR
0026](adr/0026-preflight-stale-base-before-merge.md) for the root-cause
writeup and the trade-off against gating GitHub branch protection or
downgrading every stale PR to manual, and [ADR
0028](adr/0028-stale-base-preflight-is-opt-in.md) for why it is now opt-in.

#### Filer

An opt-in subagent, alongside the scout and reviewer, that turns the
non-blocking findings a review surfaces into tracked issues — but only the
ones the work loop escalated for a human, not the whole Non-blocking section.
Off by default; setting `FILER_MODEL` (empty by default, recommended
`claude-haiku-4-5-20251001`) is the opt-in — an unset `FILER_MODEL` means zero
behavior change and zero prompt residue in the rendered issue prompt.

The work loop triages Non-blocking findings before the filer ever runs: it
fixes inline, in the same effort, every finding whose fix is cheap and in
scope (nits, smells, dead code, doc updates for a surface the diff already
touches), and escalates only what genuinely needs a human — a design
trade-off, out-of-scope work, or a change too large to fold in. This keeps
the filer from turning every nit into churn: one issue closed should not spawn
five more. Missing or inadequate tests are Blocking, not filer fodder — they
are fixed in the current work, never deferred to an issue.

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
  (false-positive, not-worth-doing, duplicate); neither is ever refiled. A
  plain closed issue carrying neither label does **not** suppress filing — a
  problem that was fixed and later regressed can still be refiled;
- files one issue per surviving finding (merging only findings that are the
  same change), each with a conventional title, the finding's file:line refs
  and reviewing rationale, a `Found by review during #<issue> (PR <url>)`
  provenance line, and an acceptance-criteria checklist.

Filed issues carry `agent-review-finding` and **never** the dispatch label
(`LABEL` / `ready-for-agent`) — a human promotes them, the same launch-button
rule that gates every other issue. The PR body then lists the filed issue
URLs instead of the raw findings.

Filing is strictly best-effort: a filer failure or timeout never blocks the
PR or changes the outcome line — the main agent falls back to pasting the
escalated findings into the PR body, exactly as when the filer is off.

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

The research label family (ADR 0022) is a fixed, non-configurable vocabulary
— `agent-research.yml` and the research prompt key off these names directly.
`spindrift doctor` checks and, in interactive mode, offers to create these
too, but treats them as advisory: unlike the four triage labels above, a
missing research label never fails the check (so CI `doctor` runs stay green
for deployments that don't use research yet). To create them manually:

```sh
gh label create agent-research             --repo owner/repo --color fbca04 --description "Apply to fire a research dispatch"
gh label create agent-research-in-progress --repo owner/repo --color bfd4f2 --description "A Box is reviewing this issue"
gh label create agent-research-recommend   --repo owner/repo --color 2cbe4e --description "Relevant and enriched — promote it"
gh label create agent-research-reject      --repo owner/repo --color e11d21 --description "False positive, not worth it, or a duplicate — close it"
gh label create agent-research-unclear     --repo owner/repo --color d4c5f9 --description "Needs a human answer — answer, then re-apply agent-research"
gh label create agent-research-failed      --repo owner/repo --color b60205 --description "Box crashed or produced no verdict; needs human triage"
```

#### Caveat: a killed launcher can strand an issue

The label swaps are best-effort. If the launcher is killed mid-run (Ctrl-C, a
crashed host, a laptop closing) an issue can be left in `agent-in-progress` with
no container running. `spindrift dispatch` never reconciles this on its own —
a bare `agent-in-progress` label is indistinguishable from an issue a live
runner is still working, so automatic adoption would risk force-pushing or
merging over that runner's in-flight commits (#600). Recovery is always an
explicit, opt-in operator action:

- **An open non-draft PR already exists** — label the issue `agent-recover`.
  `agent-recover.yml` claims it and runs `spindrift recover <n>`, re-running the
  merge gate on that PR.
- **No PR was opened yet (or only a draft PR)** — there is nothing to adopt.
  Move it back to `ready-for-agent` to re-dispatch (or to `agent-failed` to park
  it).

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
fetches an unrelated real issue on the Target repo. So for `ISSUE_TRACKER=local`
the launcher instead bind-mounts `LOCAL_ISSUES_DIR` read-only into the Box at
`/issues` (the one documented exception to the Box's zero-shared-host-filesystem
rule — see [ADR 0032](adr/0032-host-mediated-local-issue-content.md)); the agent
reads `/issues/${ISSUE_NUMBER}.md` directly and follows its `## Blocked
by`/`parent` links to any linked issues in the same folder. The mount is skipped
when `LOCAL_ISSUES_DIR` doesn't exist at dispatch time. `github` (and `jira`)
Dispatches are unchanged — they keep reading and writing in-box via `gh issue
view`/`gh issue comment`.

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
landing: https://github.com/owner/repo/pull/123
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
`settings.issueDiscovery.localIssueReference`):

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

See [`docs/adr/`](adr/) for the full architectural decision records (0001–0012),
including the Go launcher ([ADR 0007](adr/0007-runtime-logic-is-a-nix-built-go-binary.md)),
the pluggable OCI/bwrap runner ([ADR 0006](adr/0006-box-isolation-is-a-pluggable-runner.md)),
and nix-in-the-box ([ADR 0008](adr/0008-nix-is-a-first-class-default-in-the-box.md)).

---

## Research dispatch

`spindrift research` (and the selective `research <nums>` form, mirroring
`dispatch <nums>`) is a second, advise-only Dispatch kind (ADR 0022): each
container reviews one posted issue from inside a fresh clone of the Target
repo, then posts a single structured comment carrying a verdict — it never
edits the issue body, never closes it, and never promotes it to
`ready-for-agent`. A human always acts on the verdict.

Research shares the launcher's four canonical Dispatch states with `dispatch`,
but maps them to its own disjoint `github` label family — claiming an issue
never touches `ready-for-agent`/`agent-in-progress`/`agent-complete`, so an
issue can legitimately wear both a work label and a research label at once:

| label | meaning |
|-------|---------|
| `agent-research` | dual-role: standing state and trigger — apply it to fire a research dispatch |
| `agent-research-in-progress` | a Box is reviewing the issue |
| `agent-research-recommend` | relevant and enriched — promote it |
| `agent-research-reject` | false positive, not worth doing, or a duplicate (named in the comment) — close it |
| `agent-research-unclear` | relevance needs an answer only a human has — answer, then re-apply `agent-research` |
| `agent-research-failed` | the Box crashed or produced no verdict — a human triage queue, distinct from `agent-research-reject` (a *successful* "this is a false positive" conclusion is `Complete`, never `Failed`) |

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

## Dogfood loop

`dogfood.sh` drives spindrift building itself, with `CONTINUOUS_DISPATCH=1`
on by default: instead of draining one bounded batch and returning, the
launcher runs a long-lived slot-refill loop — as each Box finishes, it
re-discovers the queue and refills the freed slot immediately, re-applying
blocker readiness, the Touches overlap gate, and blocker-failed cascade —
gated by the image-freshness probe before every launch. An operator can still
set `CONTINUOUS_DISPATCH=` (empty) in `harness.env` to fall back to the older
one-wave-and-exit shape.

The freshness boundary is no longer every iteration: a refill launches
straight onto the already-loaded image so long as it's still fresh, and
`dogfood.sh` only pulls and rebuilds when the launcher reports the image has
actually gone stale (build is a no-op unless the merged diff changed the
image hash).

**Parallel by default.** `MAX_JOBS` defaults to `MAX_PARALLEL` (default 3),
so the slot pool holds that many Boxes at once. Set `MAX_JOBS` explicitly to
run a larger or unbounded pool.

**Podman machine RAM.** `dogfood.sh` refuses to start against a podman machine
whose RAM is smaller than `MEMORY_LIMIT` × `MAX_PARALLEL` (plus a fixed 512MiB
VM overhead): that mismatch lets the VM's own OOM-killer kill an in-box build —
or, once enough boxes run concurrently, the whole VM — before any single
container's `--memory` cap ever bites. The check reads `podman machine
inspect`, compares its `Resources.Memory` (MiB) against the required total,
and on shortfall prints the machine RAM, the required RAM, and a fix
(`podman machine set --memory <N>`, lower `MAX_PARALLEL`, or lower
`MEMORY_LIMIT`) before exiting 1 — no box is dispatched. It's a no-op with
adequate machine RAM, and skips cleanly when there's no active podman machine
(native Linux, or a non-podman runtime). With the defaults (`MEMORY_LIMIT=5g`,
`MAX_PARALLEL=3`) the computed minimum is 15872MiB, and that's the exact
`--memory` value the fix-hint prints — copy it verbatim rather than a rounded
16384 (16GiB): a custom `MEMORY_LIMIT`/`MAX_PARALLEL` computes a different
minimum than the shipped defaults, so only the fix-hint's own number is
guaranteed correct for your config.

**Termination.** The loop is driven entirely by the launcher's exit code:

| exit | meaning | loop action |
|------|---------|-------------|
| 0    | dispatched work | pull + rebuild, then continue |
| 2    | queue empty (no open issues with the dispatch label) | exit cleanly |
| 3    | open issues exist but none are dispatchable | stop and print a triage message — typically a failed blocker needs re-labeling before the queue can drain |
| 4    | `CONTINUOUS_DISPATCH` mode: the image-freshness probe found the loaded image would be rebuilt against the current base-branch tip; in-flight Boxes finished, no new ones launched | pull + rebuild, then re-invoke — the same boundary exit 0 runs |

Set `CONTINUOUS_DISPATCH=1` to opt into the slot-refill dispatch mode in a
driving loop other than `dogfood.sh`; see `lib/env-schema.nix`'s
`continuousDispatch` entry for the full behavior.

**Research.** `dogfood.sh` drives `spindrift dispatch` (the work kind) by
default; set `DOGFOOD_KIND=research` to drive `spindrift research` instead —
the same slot-refill loop, `MAX_JOBS`, and exit-code contract apply
unchanged, since both kinds share `cmdDispatch`'s exit codes (ADR 0022). Kinds
are homogeneous per invocation (`research` and `dispatch` never mix issues in
one run) — run `dogfood.sh` twice, once per kind, to drive both queues.

**Baked skills.** The dogfood Box bakes five pinned upstream skills into
`/home/agent/.claude/skills`, each as a `<name>/SKILL.md` directory — the
only layout Claude Code discovers, so a flat `<name>.md` file is silently
ignored — so the in-box agent can invoke them as slash commands:

- [`caveman`](https://github.com/juliusbrussee/caveman) — `/caveman`. The
  rendered issue-pass and fix-pass prompts direct the agent to default to it
  for narration and prose, compressing narration ~65% in output tokens
  without touching code, commands, error messages, or commit messages.
- [`tdd`](https://github.com/mattpocock/skills) and
  [`to-tickets`](https://github.com/mattpocock/skills) — `/tdd`,
  `/to-tickets` (pinned at tag `v1.1.0`). The IMPLEMENT section defers its
  test-first workflow to `/tdd` when baked.
- [`commit`](https://github.com/jordansmall/skills) — `/commit`. The COMMIT
  section defers commit-message formatting to `/commit` when baked.
- [`code-review`](https://github.com/mattpocock/skills) — `/code-review`
  (pinned at tag `v1.1.0`, the same upstream as `/tdd`/`/to-tickets`). Reviews
  a diff along Standards and Spec axes in parallel sub-agents.

Beyond the generic "skills available, prefer them" preamble, each of these
skills gets a deferral placed at the exact prompt section its inline guidance
would otherwise duplicate, gated on that skill being baked. The pins are
non-flake `caveman` / `matt-skills` / `jordan-skills` inputs in `flake.nix`
(`flake.lock` owns the revs); the baked set lives in `nix/dogfood-skills.nix`.
See [Contributing](../CONTRIBUTING.md) for how it's wired. To opt out of a
skill, drop it from the consumer's `skills` list; each per-skill deferral is
rendered only when that skill's `SKILL.md` is actually present at the baked
skills path, so a consumer that skips a skill gets prompts with zero residue
for it.

## Shell completion

`spindrift` ships bash, fish, and zsh tab-completion, generated from the same
schema as `--help` and the man page: subcommands (`dispatch`, `research`,
`preview`, `build`, `recover`, `doctor`) complete as the first word, every
flag (including the `--issue` alias and the secret `--*-file` flags) completes
anywhere after it, a `--*-file` flag's argument completes as a filesystem path,
and an enumerable flag's argument (`--merge-mode`, `--code-forge`,
`--issue-tracker`, `--overlap-gate`) completes to its fixed set of legal values
(e.g. `--merge-mode <TAB>` offers `immediate auto manual`).

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

`spindrift dispatch` is just a command, so wrap it however you schedule things —
`cron`, `launchd`, a systemd timer, or a CI job on a Linux runner (where the
image builds with no Linux-builder dance). In non-interactive contexts invoke the
CLI by its store path or via `nix run .#default -- dispatch` rather than relying
on a dev-shell PATH.
