# spindrift

A nix-based harness that launches waves of headless coding agents into
disposable, nix-built containers, one per issue. The agent CLI, the issue
tracker, the code forge, and the container runtime are all build-time seams
(**Driver**, **Issue Tracker**, **Code Forge**, **runner**), so nothing below is
specific to one vendor. This glossary defines the vocabulary of the harness and
the parties around it.

## Language

**Harness**:
spindrift itself: the flake, the launcher, and the in-container entrypoint that
together build the image and launch agent waves. It is the thing being imported.
_Avoid_: tool, framework, runner (the runner is specifically the container).

**Consumer flake**:
The downstream flake that imports the Harness and configures it (toolchain,
packages, prompt, settings). It is a role, not necessarily a separate repo, and
may be the same repo as the Target repo.
_Avoid_: client, user repo, parent flake.

**Target repo**:
The repository whose issues the agents work. The Box always clones it fresh
from a git *remote*, `REPO_SLUG` on GitHub under a `github` Code Forge or a
plain remote URL under a `git` Code Forge, and never reads it from a host
checkout. So it stays a distinct role from the Consumer flake even when they are
the same repo.
_Avoid_: source repo, project repo.

**Agent**:
A single headless Driver process, `claude -p …` or `opencode run …` run with
permission prompts skipped, working one issue inside one container.
The Agent is the running process; the Driver is which CLI it is.

**Driver**:
The swappable agent CLI baked into the Box. `claude` (the default) and
`opencode` are the Drivers today. It is a
build-time seam (one Driver per image, picked beside `runtime`), like the Forge
and runner seams. Each Driver normalizes its tool's quirks at its own boundary
and has two coordinated halves keyed by one name: a nix-generated in-box half
(invocation, agent-config, outcome extraction) and a Go host-side strategy in
the launcher (transient classification, heartbeat, usage
extraction). _Provisional
name_. It may be renamed (e.g. "agent harness"). _Avoid_: engine, backend, tool.

**driverkit**:
The shared leaf package under the Driver seam's host-side half (ADR 0009) that
both Driver strategies (`claude`, `opencode`), the parent driver package, and
the orchestrator import, so the code every strategy needs is declared once
instead of hand-mirrored per strategy. It owns the transient-classification
vocabulary (the `Class`/`Reason`/`Classification` types the driver package
re-exports as true type aliases), the shared transient-pattern base table
behind a `MatchTransient` that takes per-Driver extras, the buffered NDJSON
line-framer both heartbeat writers use, the not-found-returns-zero-value
log-scan degrade helper, and the role constants (implementor, reviewer, default
subagent). It imports nothing from the driver package, so a new Driver author
inherits the seam instead of copying the `claude` strategy. _Avoid_: driver
core, shared driver lib.

**Provider**:
The model backend a Driver talks to. Distinct from the Driver: the `claude`
Driver is locked to Anthropic in practice, while `opencode` can use many
Providers, so "GitHub Copilot support" is the opencode Driver pointed at the
`github-copilot` Provider, with `MODEL` provider-namespaced
(`github-copilot/…`). See [opencode Driver: github-copilot Provider
credential](docs/reference.md#opencode-driver-github-copilot-provider-credential).
_Avoid_: model host, vendor, backend.

**driver-exec**:
The in-box, nix-built Go unit that runs one Driver invocation. It takes the
prompt/agents/session file paths, the Driver's bin and flags, and a
`--devshell` switch. It spawns the Driver (via `nix develop --command` when
asked), tees the stream to the Box log, filters heartbeats in-process
(replacing the former standalone heartbeat-filter binary), and returns the
Driver's exit code. It owns process mechanics. Invocation data and outcome
extraction stay with the Driver's nix half (ADR 0009). It replaced
entrypoint.sh's temp-file/eval marshalling across the devShell process
boundary (issue #626). Its `bundle-out` verb (issue #1808) extends it beyond
process mechanics into CODE_FORGE=local's harness-owned code-out. After the
Driver exits, it bundles the base..agent-branch range into the outbox itself
instead of trusting the Agent to run `git bundle create`. The Agent's own
contract there shrinks to "commit on the branch," the same as for every other
Code Forge. When the range is empty but the Agent claimed a `ready` outcome,
driver-exec writes a corrective `status=blocked` SPINDRIFT_OUTCOME line instead
of settling as a false ready.
_Avoid_: runner (that is the Box isolation seam), wrapper, shim.

**Filer**:
The opt-in subagent role (beside the scout and reviewer) that turns findings
into issues on the Issue Tracker. It has two callers. The work loop hands it
the non-blocking review findings escalated for a human. That is not the whole
Non-blocking section: the Agent fixes cheap, in-scope findings inline in the
same effort, and only design trade-offs, out-of-scope work, or too-large
changes reach the Filer. A [[Research dispatch]] hands it the run's [[Research
finding]]s. The Filer files one issue per surviving finding, merging only
findings that are the same change, after a dedup search over previously filed
findings in any state (a closed finding is a human triage decision, never
refiled). On the relay path the Launcher repeats this check host-side, by site
key against the open backlog, before filing. Its issues carry a provenance
label naming the caller, `agent-review-finding`
from the work loop and `agent-research-finding` from research. The Filer can
never make them dispatchable itself. A human promotes them, which keeps the
rule that a human is the launch button. Filing is best-effort: a Filer failure
never blocks the PR or alters the outcome. Provisioning it (a roster model)
is the only switch for both callers. It is off by default.
_Avoid_: triager (it does not triage), reporter (collides with outcome
reporting).

**Box**:
The disposable per-issue isolation boundary that makes
`--dangerously-skip-permissions` safe. It comes in two flavors, chosen by the
`runtime` knob. The first is an **OCI container**. `podman`/`docker` name the
binary directly. `rancher` is an operator-facing alias for Rancher Desktop's
containerd mode and is the first value that differs from the binary it execs
(`nerdctl`). That one alias lives in the runner package, shared by adapter
construction and validation. The second is a **bwrap sandbox** (`bwrap`),
daemonless, Linux-only, and built from no image at all. The flavors have
different properties. Uid mapping, resource limits, and lifecycle naming each
differ, so a statement true of one is not automatically true of the other.
_Avoid_: runner (that is the adapter that drives a Box, not the Box), worker.

**Harness plumbing**:
The language-agnostic tools every Agent needs regardless of the Target: shell,
git, gh, the Driver CLI, jq, CA certs, nix. They are always baked into the image
and always kept on PATH, even when the Agent operates inside the Project
toolchain.
Distinct from the Project toolchain: plumbing is spindrift's, the toolchain is
the Target's.
_Avoid_: base image, harness deps, system tools.

**Project toolchain**:
The Target's language/build tools (rustc, node, sqlx, …), sourced devShell-first.
When the cloned Target has a usable devShell the Agent operates inside it via
`nix develop` (the default, zero-config path). Otherwise it falls back to the
baked `packages` list. Baking is an opt-in *speed* knob, a warm store so the
runtime `nix develop` substitutes nothing. It is not the primary source
(ADR 0014).
_Avoid_: packages, baked toolchain, dependencies.

**Registry proxy**:
The launcher-side authenticating proxy that lets the Project toolchain resolve
dependencies from a private registry without the credential ever entering the
Box (ADR 0044). It holds the credential in launcher memory, attaches it on the
way upstream, and exposes an unauthenticated endpoint to the Box over a per-Box
unix socket. It is a **read-only mirror**: `GET`/`HEAD` only, credential
attached only for the configured upstream host, and never carried across a
redirect. The Agent can *use* the channel and can never *read* the secret. The
guarantee is structural, not a denylist, which is what distinguishes it from
the Driver-credential scrub hooks (`agent/env-credential-scrub.sh`) it
deliberately does not reuse.
ADR 0045 supersedes the scalar model in ADR 0044. The proxy serves
Registry routes and gains a shape-keyed response-rewrite table (a cargo
index's `dl`, an npm packument's tarball URLs, re-pointed at the Forwarder)
so that URLs a registry embeds in its responses stay on the credentialed
path too. Each Ecosystem row declares its rows of that table and hands them
to the proxy, which never reads an ecosystem's committed config itself
(ADR 0048).
_Avoid_: mirror, cache, credential helper, MITM (it does not intercept TLS).

**Credential reference**:
What a Consumer declares to feed the Registry proxy: a Registry route's
`credential` table names exactly one documented source key (`env`, `file`,
`netrc`, `cargo-credentials`, `exec`, `npmrc`, `gradle-properties`) and the
env var name or file path to read it from, never a value. A route may omit
the table entirely, which is how a Consumer declares an unauthenticated
pass-through route, but a table it does write must name a source. This rule
keeps secrets out of the nix store by construction rather than by
discipline, since nothing secret is ever evaluable. It extends to each route's
`match-host` and optional `upstream-origin`, which are not credentials but
are still private, and so are runtime inputs rather than flake values.
The launcher resolves it once at
startup and unsets it from its own environment immediately after, so
the ambient environment cannot carry it into a Box. Within that resolution
step, the launcher unsets every `env` source before any `exec` source runs.
An earlier `validate`-time peek and doctor's route gate run before the unset
and rely on the allowlist instead, so on every path an `exec` helper sees
only a small allowlisted environment, never another route's credential or
the launcher's own tokens (issue #3151; see ADR 0045).
_Avoid_: secret, credential (the value itself), token path.

**Registry route**:
One record in the routes file (ADR 0045; landing via its spec A): a private
registry the Registry proxy serves, declared as `match-host`, an optional
`upstream-origin`, `auth-scheme`, and a Credential
reference, all bound in the *same record*, so the Box can select a route but
can never pair one route's credential with another route's host. It replaces
the retired scalar `REGISTRY_PROXY_*` knob family. N routes share one proxy and
one Forwarder. A per-route path prefix tells them apart. It is slugged from
the route's own `match-host` and minted once at route synthesis, never
re-derived mid-run. The proxy
routes strictly by that prefix and refuses a request under no known prefix
before dialling any upstream. Every route is host-rooted (ADR 0047). It
serves the whole matched host, and every request is checked, with no off
switch, against a path-set derived from the Target repo's own committed
config. An optional `allow` list is the only way to widen that set. A route may
also carry one **ecosystem declaration** per Ecosystem row (ADR 0048). That is
a block keyed by the ecosystem's name, holding what that ecosystem needs and
the repo's committed config cannot say. Examples are a declared path for an
ecosystem with nothing committed to scan (go, gradle), or which of the repo's
named registries the route serves (cargo, which binds by source replacement
rather than by rewriting the repo's config). The declared path joins the
route's enforced path-set as operator-owned policy. The Ecosystem row
validates the rest. A route's credential and auth scheme are per host and
shared by every ecosystem on it, never declared per ecosystem. The
routes file is a runtime input (private hostnames stay out of the nix store)
and carries credential references, never values.
_Avoid_: registry config, upstream (alone), credential map, per-ecosystem
route.

**Ecosystem row**:
The one record per dependency ecosystem (cargo, npm, yarn, pnpm, go, gradle)
holding everything the Harness knows about it: the lockfiles that identify it,
how to read its committed registry config, what a Registry route may declare
for it, how the proxy rewrites its registry's responses, and how it binds to
the Registry proxy (ADR 0048). Every consumer walks the rows in their fixed
precedence order, and none spells an ecosystem's name, so adding an ecosystem
means adding one row. A capability the ecosystem lacks is absent from its
row, not stubbed.
_Avoid_: ecosystem support, extractor table, plugin, driver (that is the
agent CLI seam).

**Route discovery**:
How a routes file gets written without hand-transcription (ADR 0045):
`spindrift registry discover` parses the Target repo's own committed registry
config to extract hosts, base URLs, and cargo registry names. For npm, yarn,
and pnpm it reads the same files the in-tree rewrite substitutes. For cargo it
reads `.cargo/config.toml` left exactly as the repo wrote it, since cargo binds
by source replacement rather than a rewrite. It then matches credentials in
host-keyed stores, searched in this documented order (netrc, npmrc, cargo
credentials.toml, gradle.properties), probes the auth scheme, and writes
Registry routes.
Only the operator runs it, and only at setup time.
Nothing inside the Box feeds discovery, because Box-influenced route
creation would let an Agent steer a real credential toward any host the
operator's stores hold. `doctor` re-runs it in check mode and reports drift.
The in-Box scan may *warn* that a host is uncovered, never add a route.
_Avoid_: auto-configuration, zero-config, runtime discovery.

**Binding**:
How a Project toolchain is pointed at the Registry proxy. It is owned end to
end by `driver-exec bind-registry` (`cmd/launcher/driver-exec/bindregistry_cmd.go`),
the sixth verb in the ADR 0036 dispatch chain, not by per-ecosystem bash
phases. It has three independent modes, not one shared apply/revert.
Classification mode
(`-work-dir`+`-ecosystem-env-output`, `runBindRegistryClassification`,
unchanged from issue #2930) scans the clone for lockfiles and writes a
sourced `NUDGE_ECOSYSTEM` env file; its one live call site is
`phase_toolchain_nudge` (`agent/entrypoint.sh:589`), with its own sourced
env file distinct from bindings mode's. Bindings mode
(`-bindings-env-output`, `runBindRegistryBindings`) writes go/npm/pnpm/yarn
berry env overrides to a sourced env file, plus two direct home-level writes
with no revert of their own: a user-level `$CARGO_HOME/config.toml`
(`ecosystem.CargoConfigTOML`, cargo's own mechanism, issue #2849) and a
Gradle init script under `$GRADLE_USER_HOME/init.d/`
(`ecosystem.GradleInitScript`: `beforeSettings`/`projectsEvaluated`/
`settingsEvaluated`, plus a plain top-level hook for buildscript classpath).
In-tree mode (`-intree-action=apply|revert`, `runBindRegistryIntree`) is the
only mode with a revert. Its rewrite is a textual host substitution of a
tracked config file for cargo, npm, yarn, and pnpm, tagged `skip-worktree`
so the Agent neither sees nor commits it, table-driven off one row per
ecosystem in `bindregistry.InTreeBindings()`
(`cmd/launcher/internal/bindregistry/intreebinding.go`), itself a filtered
view of the one shared `ecosystem.Table`, so an ecosystem's name is spelled
once. Apply first probes for an already-listening
Forwarder and spawns one if needed, gating the whole rewrite all-or-nothing
on TCP readiness (AC5: a Forwarder that never becomes ready leaves every
in-tree file untouched, with no partial rewrite, `bindregistry_cmd.go:340-364`).
Appliedness has no sentinel of its own. `ApplyInTreeBinding`/
`RevertInTreeBinding` derive it only from the `skip-worktree` bit plus
working-tree-vs-HEAD content on each call, never from state left over from a
prior run. `agent/entrypoint.sh` itself now only sequences the calls: one
classification-mode call, one bindings-mode call, one `source` of each
call's emitted env file, in-tree apply/revert/re-apply calls wrapped around
clone and branch recovery, and a fourth in-tree call site, a defensive
best-effort revert in `phase_conflict_resolve`'s rebase-abort path
(`agent/entrypoint.sh:941`).
_Avoid_: adapter, registry config, ecosystem support.

**Forwarder**:
The small in-Box process that presents the Registry proxy's endpoint as a
local TCP endpoint, because package managers take a URL and not a socket.
The transport is probed per Dispatch, not assumed (ADR 0044, #3111 amendment).
Where the configured runtime carries a unix socket across, the Forwarder is
`socat` over the mounted socket and no host TCP port opens. Where it cannot
(macOS VM sharing layers, remote-context daemons), the launcher serves
secret-gated loopback TCP instead and the Forwarder is the
`forward-registry-tcp` verb (`bindregistry.SpawnHTTPForwarder`,
`cmd/launcher/internal/bindregistry/tcpforwarder.go`), which attaches the
per-run secret. That mode is mutually exclusive with
`networkMode=no-host-loopback`, which fails the Dispatch loudly rather than
composing. The `bind-registry` verb spawns it itself (`bindregistry.SpawnSocat`,
`cmd/launcher/internal/bindregistry/forwarder.go`), not a separate bash phase.
The same verb probes readiness in-process (`EnsureForwarderReady`). It
spawns only if nothing is already listening, then polls the TCP port until
ready or a timeout elapses. Readiness is never an external convention
crossing a process boundary.
_Avoid_: proxy (that is the launcher-side half), shim, tunnel.

**Issue Tracker**:
The seam that supplies work and carries dispatch state: listing dispatchable
issues, reading an issue's body/title/state, transitioning its Dispatch state,
and posting comments. One of two independent axes (the other is the Code Forge).
Implemented adapters: `github` (issues via `gh`), `forgejo` (Forgejo/Gitea REST
API; Codeberg the default instance), `jira`, and `local` (issues as files in the
Target repo, no server). The launcher reasons in canonical
Dispatch states, never in a backend's native mechanism.
_Avoid_: issue source, ticketing, backlog.

**Content plane**:
An issue's body and comments, meaning the text a Dispatch reads to do its work
and the comment it writes back, as distinct from the Dispatch lifecycle (its
state transitions). The Launcher is the only writer on both planes. The
Launcher injects the subject issue as `ISSUE_TEXT` for every tracker (issue
#3445). Reads beyond it depend on the tracker: in-box for a remote tracker,
and host-injected for `local`, whose whole link chain goes in that same
var (ADR 0050, superseding ADR 0032's read half).
_Avoid_: issue data, payload.

**Remote / local (in-box reachability)**:
The split on *both* backend axes by whether a backend is reachable from
inside the Box. The Box reaches **remote** backends (`github`;
`gitlab`/`bitbucket`/`jira` as they land; the `git` code forge) in-box, over
the network via their own client, so it reads and lands directly. **`local`**
is unreachable in-box (no server, git-ignored, absent from the fresh clone), so
it is **host-mediated**, but the two axes are no longer host-mediated the same
way. On the issue plane the Box reads host-injected text, not a mount. The
Launcher resolves the subject issue and its transitive linked-issue chain
host-side and renders them into the prompt as `ISSUE_TEXT`. The Box still
never writes the tracker. It emits a comment for the Launcher to post instead
(ADR 0050). On the code plane the Box still clones a read-only mount of the
Accumulation repo and emits a bundle for the Launcher to land (ADR 0033).
These code-plane mounts, plus the writable outbox, remain the documented
exceptions to zero-shared-host-filesystem. The issue plane is no longer one
of them.
_Avoid_: online/offline, connected/disconnected.

**Code Forge**:
The seam through which the Harness lands code. Its narrow core is what every
adapter implements with real behavior: agent branch naming, rebase,
merge/landing under `MERGE_MODE`, and a connectivity probe. It is a second axis
independent of the Issue Tracker, and combines with any of them. A git endpoint
always exists, split by in-box reachability like the Issue Tracker.
**Reachable** endpoints (`github`, `forgejo`, `git`) let the Box clone from and
push to them directly. The **host-mediated** endpoint (`local`) is not
reachable in-box, so the Launcher mediates. There are four values:

- `github` runs the full flow: open a PR, watch the CI rollup, rebase, merge.
  It is the `gh`-exec adapter, and one of two values (with `forgejo`) that also
  implement **PRForge** (see below).
- `forgejo` runs the same full flow (open a PR, watch the CI rollup, rebase,
  merge) against a Forgejo/Gitea instance (Codeberg by default). It is the
  second value that implements **PRForge**, and is a native REST client, not a
  CLI-exec adapter (ADR 0038).
- `git` is **push-only** to a plain git remote URL (self-hosted git, gitea,
  GitLab-without-MRs, a bare server repo). It clones, commits to a per-issue
  branch, pushes, and stops. It has no PR, no CI, and no merge gate. It
  implements CodeForge only, with no stub methods. `MERGE_MODE` maps to remote
  pushes. `manual` pushes the feature branch, `immediate` pushes straight to
  the target branch, and `auto` is the forge's native auto-merge and has no
  meaning here.
- `local` is **host-mediated**, the code-plane mirror of the `local` tracker
  (ADR 0033). The Box clones from a read-only mount of the Accumulation repo and
  emits its branch as a git bundle through a writable outbox. The Launcher lands
  it host-side by rebasing it onto the per-ticket Integration branch's current
  tip and fast-forwarding. History is always linear, never a merge commit
  (issue #1889). The code plane uses no network. It shares the `git` adapter's
  code for everything except landing (branch naming, `Rebase`, `Probe`,
  `BranchExists`). `Merge` is its own rebase-and-fast-forward override, not the
  shared adapter's `--no-ff` merge.

ADR 0013 **cut** the fully-local code path ("a git remote is a hard
requirement") and ADR 0033 **reopened** it on new terms. It is not a read-write
mount of the operator's repo but the host-mediated `local` value above: a
read-only clone mount in, a Launcher-landed bundle out. For solo/private use,
choose `local` here (private issues *and* private local code) or pair `git`
with `ISSUE_TRACKER=local` (private issues, published code).
_Was_: "Forge" (a single seam over the Target repo host); split into Issue
Tracker + Code Forge once issues and code host became independent axes.
_Avoid_: GitHub adapter, API layer, client wrapper.

**PRForge**:
The optional PR, CI-rollup, and auto-merge interface (`OpenPRForBranch`,
`PRForBranch`, `PRState`, `CheckState`, `FailureDetail`, `ListPRFiles`,
`CanAutoMerge`, `EnqueueAutoMerge`) split out of Code Forge (ADR 0013
amendment, issue #517). The `github` and `forgejo` adapters implement it. Callers
discover it with a type assertion, `pr, ok := cf.(forge.PRForge)`, the
standard Go optional-interface pattern, rather than a `PushOnly()` capability
flag. `internal/settle` is the main consumer. It resolves `PRForge` once at
construction and branches on its presence to skip the CI-wait/merge-gate
entirely for a push-only forge.
_Was_: a `PushOnly()` bool on the combined Code Forge interface, plus six
stubbed methods on the `git` adapter.
_Avoid_: PR client, GitHub-only interface.

**Accumulation repo**:
The Launcher-owned bare git repo that the `local` Code Forge (ADR 0033)
accumulates code in, the code-plane sibling of the `.spindrift/issues/`
directory. It defaults to `.spindrift/accum.git` under the launcher's working
directory, and `CODE_FORGE_ACCUMULATION_REPO_DIR` overrides it. The Launcher
auto-creates and seeds it (base branch ref from the operator's local
checkout, offline) before any Box runs, and does so idempotently on every run
after that. Each seam clones it through a read-only mount and lands onto an
Integration branch inside it. It is deliberately *not* the operator's working
checkout. A bare repo has no checked-out branch, keeps agent refs and
objects out of the operator's repo, and resets with `rm -rf`.
_Avoid_: mirror, staging repo, local remote.

**Integration branch**:
The branch in the Accumulation repo where all the seams of one broad ticket
converge, keyed on *each seam's own* local issue's `parent` frontmatter
(`integration/<sanitized-parent>`). It is never a single knob shared across a
run, so a mixed-parent dispatch batch converges each seam onto its own branch
(ADR 0033, issue #1734). An issue with no `parent:` set is its own broad ticket,
keyed on its own sanitized slug instead of a shared fallback branch. `parent`
is opaque and operator-authored. spindrift never resolves it against another
tracker. It only sanitizes it into a git-ref-safe token (lowercased, each run
of non-`[a-z0-9]` characters collapsed to a single dash, leading/trailing
dashes trimmed) before forming the branch name. Each seam lands by a
host-side rebase-and-fast-forward onto it, always linear, never a
merge commit (issue #1889). Once every one
of a broad ticket's seam issues is closed, the Launcher auto-surfaces its
current tip into the operator's checkout as a local branch named after the
ticket (issue #1730). The operator still publishes the single team PR by
hand. It is distinct from a seam's per-issue agent branch, which merges *into*
it. A broad ticket may itself be a tracked issue, but it is never one of its own
seams. An issue is the broad ticket rather than a seam of itself exactly when
its resolved key equals its own sanitized slug *and* at least one other issue
resolves to that same key. The exception: if excluding every such colliding
issue would leave the group empty, none is excluded. So a
middle issue in a three-level chain still gates its grandparent's group
normally by carrying its own `parent:` key into it (issue #3439).
_Avoid_: feature branch, epic branch, accumulation branch.

**Landing**:
The sealed `forge.Landing` value (`cmd/launcher/internal/forge`, issue
#1809) a stored landing string parses into, via one `ParseLanding` function,
at the two seams that consume it: settle's post-merge upgrade and
Reconcile's verification. It has three variants: `PRURL` (`github`'s landing
grammar), `BranchRef` (a raw pre-merge branch name, which is `local`'s landing
string before a merge lands and `git`'s only shape), and
`IntegrationRef` (`local`'s post-merge `<Integration branch>@<sha>`, ADR
0029/0033). Storage (issue frontmatter, the outcome line) and every
remote-tracker interface keep the plain string unchanged. Only the two
consuming seams match on the typed variant, instead of each resolving the
three-grammars-in-one-string ambiguity itself. A `BranchRef` that is already
an ancestor of its Integration branch means the merge landed but the
post-merge upgrade never ran. Reconcile repairs it in place (upgrades the
recorded landing to `IntegrationRef`, closes the seam) instead of leaving it
stuck open silently forever. If it is not yet an ancestor, Reconcile prints a
loud stuck verdict naming the branch.
_Avoid_: landing ref, landing string (fine for the untyped stored form;
ambiguous once the typed value is in scope).

**localloop**:
The `cmd/launcher/internal/localloop` package: CODE_FORGE=local's per-issue
Code Forge construction, outbox resolution, and parent resolution, plus the
reconcile/surface hookup, behind one `Wire` constructor (issue #1806,
campaign #1803 T1). The launcher's command path and the package's own
composed **local loop** test (seeded Accumulation repo, then fixture commit,
bundle in the outbox, settle, reconcile, and surface) drive the same
composition. A regression in any gate then shows up in one place instead
of in two independently maintained copies that drift apart. `Wire` resolves
each issue's parent exactly once, sealed as `local.SanitizedParent`, a struct
that only the parent-resolution function can mint. It hands that one value
to the forge constructor, the launcher's BASE_BRANCH resolver, and surface
grouping. `IntegrationBranch` and its siblings accept only the sealed type,
so an unsanitized string can't reach a branch name (issue #1810).
_Avoid_: local-loop glue, launcher wiring (both too vague — `localloop` is
the package name).

**SeedScope**:
`waves.SeedScope` (issue #2150, spec #2144 D2) is the opaque seed-branch
scope that a dependent's blocker gate resolves against under CODE_FORGE=local.
The wave engine holds it via `Config.SeedScopeOf` and hands the whole value to
the local Code Forge's containment query, but never constructs or parses the
`integration/<parent>` ref grammar itself. The local adapter's
`local.IntegrationBranch` renders the branch label the scope prints, so the
operator-facing hold diagnostic names the Integration branch while the wave
engine stays adapter-neutral. `localloop.SeedScopeOf` is the single seam that
builds it. Both the dispatch command path and the Console use it, so the
two can never disagree about which blocker landing gates a dependent.
_Avoid_: seed parent, `ParentOf` (the pre-#2150 name of the now-deleted
resolver closure and Config field).

**Verdict**:
`localloop.Verdict` (issue #1811, campaign #1803 C4) is Surface's
one-line-per-broad-ticket report. Both Reconcile entry points (the
auto-run after dispatch and the standalone `spindrift reconcile`) print it, so
no touched broad ticket is ever silent. It has two shapes. `VerdictSurfaced`
names the branch Surface made current and how many seams it took
(`surfaced → branch <name> (N seams)`). `VerdictHeld` names the first unmet
gate, in this order: open seam, stuck landing (reusing the wording of the typed
**Landing**'s stuck-verdict repair check), target branch checked out,
diverged, never landed (`held — <reason>`). Those gates evaluate over the
group's seams, so a broad-ticket issue excluded from its own group (issue
#3439) never produces a `stuck landing` verdict for its own landing.
`reconcile`'s `status=stuck` line reports that instead (issue #3440). A
parented broad ticket's surfaced branch keeps ADR 0033's sanitized-parent
name. A parentless one surfaces under its own sanitized ticket title
instead (falling back to its slug when the title sanitizes empty). The
Integration branch key stays the stable sanitized slug in every case,
so an edited title never changes a ticket's identity mid-flight. The
Console may render the same value later.
_Avoid_: surface notice, skip reason (both pre-#1811 names for the scattered
prints Verdict consolidates).

**Guard**:
`freshness.Guard` (issue #2155, campaign #2144 D5) is the sealed home of the
continuous-dispatch host-taint halt rule and its prior-rev memory. It wraps
the single-line persisted prior-stale-rev file and classifies a stale
`freshness.Probe` `Result` directly. The empty-tip-tag post-condition (an
empty derived tag means a stuck eval/derive failure, never structural host
taint) therefore lives beside the same-rev non-convergence check it guards.
`Classify` returns a `Disposition`, either `Rebuild` (content staleness: record
the rev, the loop rebuilds and retries) or `HostTainted` (the loop already
rebuilt against the same base tip and it is still stale: clear the memory, the
loop halts). Record and clear stay internal so a future edit cannot
reintroduce the perpetual-rebuild loop (#2113, #2128) by clearing state at the
wrong moment. The launcher only maps `HostTainted` to exit code 5, and `Reset`
clears the memory on the queue-drained path. The outcome type is deliberately a
`Disposition`, **not** a Verdict. That term stays reserved for the localloop
Surface report above and research verdicts.
_Avoid_: staleRevTracker, classifyStaleOutcome (both pre-#2155 names for the
main-package glue Guard absorbs); naming its outcome type Verdict.

**Conformance contract**:
The executable contract for the forge seams. It is a shared `forgetest` suite
that every adapter of a seam interface, the shared test Fake included, must
pass. It runs hermetically via per-adapter scripted-backend harnesses (stubbed
`gh` exec, `httptest` Jira, tempdir local tracker) with seeding and
fault-injection hooks. It replaces fidelity-by-comment in the Fake ("mirroring
the real adapter's…") with a test failure on drift. Three contracts landed in
this order: Issue Tracker (#1544), Code Forge (#1545), PRForge (#1546). Decided
2026-07-18.
_Avoid_: parity test, integration suite (it is hermetic), mock verification.

**Backend matrix**:
Issue Tracker and Code Forge are two independent, freely-combinable axes
(`ISSUE_TRACKER` × `CODE_FORGE`). All cells are permitted. The harness does not
reject "incoherent" pairings (e.g. github-issues + no-code-forge), and an
operator who selects one owns the consequences. `local × local` is the
fully-specified cell, the **local loop**, with both planes host-mediated and
offline (ADR 0033). `CODE_FORGE=local`'s per-ticket Integration branch assumes a
tracker that supplies a `parent`/epic link. `local` does today, and other
trackers will as their sub-issue links land. An issue with no such link is its
own broad ticket rather than an unspecified case.
_Avoid_: preset, profile, mode.

**Launcher input**:
The nix-rendered JSON document that carries every nix-computed value from the
generated wrapper to the launcher through a single `--input` store path: a
`settings` section (the resolved knob values after the Consumer flake's
`settings` are applied) and an `artifacts` section (built references: image
archive, agent files, driver name, …). Knob precedence is document < explicit
CLI flag. Ambient env no longer configures knobs (staged: warn, then error)
and remains only for secrets and plumbing from the launcher to the Box. The
document carries the Consumer flake's settings, flags carry the operator's
per-run choices, and env carries secrets. Decided 2026-07-13 (ADR 0020),
replacing the exported
`VAR="${VAR:-baked}"` run preamble whose env-wins fallback let ambient
variables silently override flake settings.
_Avoid_: config file (generated, never operator-edited), env preamble,
defaults preamble.

**Dispatch**:
The per-issue execution, from claim to verdict: every Box launched for one
issue (initial run, fix passes, conflict-resolve), plus its results and its
Driver-cache entry. The Dispatch lifecycle names its states. It is distinct
from the Driver's resumable conversation session, which the Driver-cache entry
preserves across a Dispatch's fix passes.
_Avoid_: session (collides with the Driver's conversation session), run, job.

**Dispatch kind**:
The axis naming what a Dispatch delivers: `work` (the original kind, which
lands code through the Code Forge) or `research` (lands a verdict and
enrichment comments on the Issue Tracker and never touches the Code Forge).
Kinds share the canonical Dispatch lifecycle. On the `github` tracker each kind
maps the states to its own label family.
_Avoid_: mode, dispatch type, pipeline.

**Driving loop**:
Whatever keeps invoking Dispatches against the queue unattended, across many
invocations: the [[Daemon]] today, `dogfood.sh` before it. It outlives a
single Launcher invocation.
_Avoid_: scheduler, cron, orchestrator (the in-box review role).

**Daemon**:
The shipped [[Driving loop]] (`apps.daemon`). It is a long-lived host process
holding a pool of slots. Each slot holds one single-Box Dispatch invocation
pinned to a fetched revision, and the pool draws from both Dispatch kinds. The
Daemon supervises and re-invokes. It never runs a Box itself.
_Avoid_: continuous dispatch (the deprecated in-process pool), dogfood loop,
service, supervisor.

**Awake window**:
The daily local-time span, in an explicit zone, during which the Daemon may
start a new Box. A Box already running when the window closes finishes. The
window gates starting, never stopping.
_Avoid_: schedule, quiet hours, cron window.

**Stop signal**:
The first operator signal to a Launcher or a Daemon. It means launch nothing
further and let in-flight work finish on its own.
_Avoid_: drain (the effect, not the request), graceful shutdown, SIGTERM (the
mechanism).

**Abort signal**:
The second operator signal, after a [[Stop signal]]. It means reap every
in-flight Box and release its claim.
_Avoid_: escalation (the transition from Stop to Abort, not the request),
kill, SIGINT (the mechanism).

**Research dispatch**:
A Dispatch whose Agent (the "researcher") reviews a posted issue from inside
the Box, exploring the Target repo for real context, then posts an
enrichment comment and a verdict. It is advise-only for code and for the source
issue. It never promotes an issue to dispatchable and never closes one. A human
acts on the verdict, which keeps the rule that a human is the launch button.
Its one write power beyond the verdict comment is filing [[Research
finding]]s as new issues through the [[Filer]]. That filing is host-mediated,
never a Box-side tracker write. Verdicts are a closed set carried by `Complete`:
`recommend` (relevant, enriched, ready to promote), `reject` (false
positive, not worth doing, or duplicate, with the reason in the comment; also
the normal close for a survey-style ticket whose deliverable was its filings),
and `unclear` (relevance needs answers only a human has; answer, then re-apply
the trigger label to re-research). Filing is independent of the verdict. Any
verdict may carry filings, listed in the verdict comment. A crashed or
verdict-less Box is `Failed`, never a verdict. A filing failure never makes it
`Failed`. The finding falls back inline into the comment. On `github` the label
family runs from `agent-research` (dual-role: standing state and trigger) to
`agent-research-in-progress` to the terminals `agent-research-recommend` /
`-reject` / `-unclear` / `-failed`.
_Avoid_: triage (the human action on `Failed` issues), scout (the in-box
subagent role).

**Research finding**:
A discovery a [[Research dispatch]] makes beyond its verdict, such as a bug, a
missing feature, or a chore. It is durably recorded in exactly one of two
places: filed as its own issue through the [[Filer]], or, when filing fails,
inline in the verdict comment (title and summary), so no finding is ever
silently lost. A filed finding carries the `agent-research-finding` provenance
label, an optional type from a closed set (bug / enhancement / chore) that the
host maps to labels (the Box never names a label), and a backlink to the
source research issue. Like every agent-filed issue, the agent can never make
it dispatchable itself. A human promotes it.
_Avoid_: research issue (ambiguous with the issue being researched),
side-effect issue, enrichment (that is the comment's context, not a new
issue).

**Butler**:
A proposed third Dispatch kind (ADR 0056, working name) that keeps a Target
repo tidy on its own initiative. One read-only Box scans one [[Chore]] and
files what it finds through the [[Filer]]. It is keyed by chore, not by issue,
so no label starts it. The [[Daemon]] runs it only when no dispatch
or research work is waiting. It is advise-only for code, but an opt-in,
host-gated subset of its findings may be filed already dispatchable.
_Avoid_: sweep, patrol, scheduler, cron, audit.

**Chore**:
One standing topic of upkeep a [[Butler]] works, such as bugs, refactors, or
documentation drift, with its own interval and the classes of finding it
may promote. Working name.
_Avoid_: lens, task, job.

**Ledger**:
A [[Chore]]'s durable record in the Target repo. It holds the history of every
Butler run on it (how far it has swept, where it is in the tree, what it
filed and spent) and the claim that stops two runs working the same chore
at once.
_Avoid_: state file, log (the Box log), report.

**Dispatch lifecycle**:
The canonical dispatch states the launcher reasons in, independent of how any
one Issue Tracker stores them. `Dispatchable` (a human marked the issue ready,
which is the launch button) moves to `InProgress` (a Box has been dispatched;
re-runs skip it), then to `Complete` or `Failed`. `Complete` means the agent
has nothing left to do because its landing path has settled: a
merged/handed-off PR on `github`, a landed branch when the Code Forge is
push-only/absent, or a posted verdict for a research dispatch. `Failed` means
the Box crashed or never reached green past MAX_FIX_ATTEMPTS. On `github` it
also means a force-pushed head from rebase-retry or an agent conflict-resolve
box never re-confirmed green. `Failed` needs human triage; re-transition to
retry. Each Issue Tracker adapter maps these states to its native mechanism:

- `github` uses labels (`ready-for-agent`, then `agent-in-progress`, then
  `agent-complete`/`agent-failed`), swapped atomically. This is the original,
  unchanged mechanism.
- `jira` uses a configurable status mapping (from each state to that project's
  workflow status name/id), since there is no universal Jira workflow to assume. When a
  state is unmapped, or the mapped transition isn't available on the issue's
  current workflow, it falls back to swapping a Jira label for that state (like
  `github`'s labels) so the lifecycle always makes progress.
- `local` uses a `state:` field in the issue file (frontmatter), rewritten in
  place. Separately, a local issue also carries a native open/closed axis (a
  `closed:` field, absent = open), the same way a GitHub issue is open/closed
  independent of its dispatch labels. Reconcile drives that axis, not the
  launcher's lifecycle (ADR 0029).

On `github`, the launcher swaps to `Complete` once the landing path settles, not
at first green. `immediate` mode can still do real agent work after green
(rebase-retry, an agent-dispatched conflict-resolve box, a post-force-push CI
re-wait), and the issue stays `InProgress` throughout that work so the label
never claims "nothing left to do" while a Box may still run. MERGE_MODE governs
that landing path. `immediate` merges automatically (locally, via a push that
updates a clean checked-out branch). `manual` (default) leaves the branch/PR
for a human. `auto` is the forge's native auto-merge, which works on any
`PRForge` backend (`github`, `forgejo`). A merge failure after green leaves the
issue `Complete` with a merge-blocked note, never `Failed`, once that landing
attempt settles. The exceptions are when the post-force-push re-wait (after
rebase or conflict-resolve) ends red or times out, or the conflict-resolve
dispatch itself fails. There the force-pushed head never went green, so the
issue ends `Failed` instead.
_Was_: "Label lifecycle". Labels were GitHub's storage mechanism, mistaken for
the states themselves.
_Avoid_: status, queue, state machine.

**Dispatch order**:
The order in which `ListIssues` hands work to the launcher. It is oldest-first,
which is now the *tiebreaker within a [[Dispatch priority]] tier* rather than
the whole rule. Priority is the primary sort key and oldest-first the secondary.
Identity is opaque to the launcher (`Number` is a string), so the launcher never
parses or compares IDs numerically. Each Issue Tracker adapter returns issues
already in canonical oldest-first order using its own order key: issue number
for `github`, created time for `jira`, and a `created` frontmatter timestamp for
`local`. The launcher applies the priority sort over that order once, centrally.
_Avoid_: issue number, sequence.

**Dispatch priority**:
The primary sort key over the dispatchable pool, layered above [[Dispatch
order]]. The launcher orders ready issues by priority tier first, oldest-first
*within* a tier. There are four tiers, Critical > High > **Normal** > Low, where
Normal is the default an *unlabeled* issue occupies. Priority can raise an
issue (Critical/High) or lower it (Low) relative to the untouched middle. A
disjoint label family carries it (`agent-priority-critical` / `-high` / `-low`,
`agent-`-namespaced to never collide with a repo's own `priority-*` project
labels). Each Issue Tracker adapter resolves those labels into one canonical
`Priority` value on the returned issue, the same way dispatch-state labels
become a canonical [[Dispatch lifecycle]] state. So the launcher sorts once,
centrally (in the plan), never in a backend's mechanism. It applies to the
discovered pool (queue-drain and continuous-refill hydration) and the Console
[[Backlog]]. A hand-picked selective list keeps the operator's typed order
(ADR 0011), and priority is a no-op on the single-issue workflow trigger. It is
only a sort key. It never overrides a dependency edge (a blocker runs before its
dependent regardless of tier, and a blocker does **not** inherit its dependents'
priority, since a [[Wave]]'s ready set is ordered by each issue's own tier). It
never authorizes a dispatch (an issue still needs the launch-button label, and a
`priority-*` label is not a trigger). It applies no aging. Lower tiers starve
under sustained higher-tier inflow, which *is* the meaning of a low tier. The
highest label wins if an issue carries more than one.
_Avoid_: severity (an issue-quality axis, not a dispatch-order axis), rank,
weight, SLA.

**Wave**:
One batch of Dispatches launched concurrently. With no blocker edges the whole
ready-set is a single wave. Declared edges split a run into dependency waves.
Edges come from each Issue Tracker adapter's `DepsOf`. `github` and `jira`
both prefer native dependency relationships (GitHub's issue-dependencies API,
Jira's "is blocked by" issue links) and use body/prose refs only as a
fallback where the adapter has one. Native wins whenever it's non-empty and is
never merged with body text. `DepsOf` tags each ref with the source it
resolved from (native vs body). The tags travel alongside the edges as
`Sources` and appear in every operator-facing blocker rendering: `preview`'s
blocker annotations, blocked-skip notices, and the blocked-claim marker (and the
release comment posted from it). Drift between a stale body section and
changed native links is therefore visible instead of silent.
Every dispatch invocation runs at most one wave (ADR 0019). `MAX_PARALLEL`
caps the number of concurrent Boxes within a wave (default 3). `MAX_JOBS`
caps the wave size (default 0 = uncapped). Held issues stay on the dispatch
label and the next invocation picks them up. A driving loop (the
[[Daemon]], CI, or a human re-running) drains a dependency graph one fresh wave
at a time. No in-process poll waits for later waves. No label-based gate
serializes issues. Ordering comes only from dispatch order and blocker edges.
_Was_: "fan-out". The launch act and the batch carried two names, now unified
on the batch noun.
_Avoid_: fan-out, batch (as loose prose for a wave — `waves.Batch` is a
distinct type, see [[Batch]]), round.

**Batch** (`waves.Batch`):
Discovery's sealed result type: the candidate Issues plus the blocker graph
resolved for them (Edges, Sources, Failed). A Discoverer returns it, and Input
and Plan embed it, so those four fields move and stay in sync as one value
instead of drifting independently. It is distinct from [[Wave]]. Batch is
discovery-time data (what to dispatch and why it's ready). Wave is launch-time
scheduling (what actually launches together).

**Queue** (`waves.Queue`):
The wave engine's work-supply seam (`cmd/launcher/internal/waves/queue.go`,
issues #2937/#2938/#2939): where dispatchable work comes from, how it's
claimed, how many candidates remain queued, and where a finished stale-drain
report goes. It has four methods, each with a fuller doc comment on the
interface itself. `Discover` returns the current dispatchable [[Batch]]. `Claim`
(embedded via the one-method `Claimer` interface, so the one-shot `Dispatch`
entry point can take just that subset) marks an issue claimed immediately
before dispatch. `Pending` is a quiet, side-effect-free count of how many
candidates remain queued, taking the caller's own claimed set as a
parameter (issue #3035). `ReportStaleDrain` delivers a finished stale-drain
report to wherever an implementation's operator watches. There are two real
implementations. The headless adapter (`NewHeadlessQueue`, same file)
performs the real `Dispatchable` to `InProgress` claim and owns the
operator-facing stale-drain stdout print plus log-file append. The Console
adapter (`runContinuousQueue`, `cmd/launcher/internal/console/launcher.go`)
has a no-op `Claim` (Console's own `Queue.Discover`, `console/queue.go`,
already claims as a side effect of discovering a ready pick), reads the
session queue's own still-queued depth for `Pending`, and routes
`ReportStaleDrain` to the session banner. It is distinct from the Console's own
session `Queue` (`console/queue.go`, holding
`Pick`s), which is one implementation's backing store, not the seam itself.
_Was_: the per-caller Config closures `PreResolved`, `PendingCount`,
`DiscoverReporting`, `OnStaleDrainReport`, and the label-zeroing convention
(`Label`/`InProgressLabel` left empty to mean no-op claim), all deleted
from `waves.Config`.

**Readiness**:
The query seam answering "may this issue dispatch now, and if not, why"
before anything launches. It returns per-issue blocker status (ready /
blocked-by / check-failed) with the native-vs-body `Sources` tags carried
through. The pre-dispatch consumers, the Console's held picks (#650) and
`preview`'s blocker annotations, use it. The wave engine keeps its own internal
gate, which now always runs. A caller that has already resolved readiness
through this seam, like Console's `Queue.Discover`, hands back an
empty blocker graph, so the gate has nothing left to check rather than
needing to be told to skip it. Decided 2026-07-18 (#1547), replacing
the exported blocker functions and the empty-edges construction.
_Avoid_: blocker check, gate (that is the engine's internal act), preflight
(collides with the stale-base preflight).

**Driving loop**:
The outer loop that re-invokes the launcher. It owns what a single invocation
structurally cannot do: advance the checkout, rebuild the image, and
decide whether to run again. ADR 0019 makes the invocation the
image-freshness boundary, so every driving loop exists to supply the other
half of that boundary. Instances differ by who decides to re-run (an
operator for the Console, the [[Daemon]], a workflow for CI), not by what the
role is.
_Avoid_: supervisor, runner (that is the Box sandbox seam), harness. Note
"daemon" names one instance ([[Daemon]]), never the role itself, and never
the container-runtime daemon the bwrap runtime is "daemonless" of.

**Daemon**:
The shipped unattended [[Driving loop]]: `apps.daemon`, which `mkHarness`
generates per-Consumer beside `apps.default`. It holds `MAX_PARALLEL` slots and
fills each with one single-Box launcher invocation pinned to a fetched
revision, drawing from both [[Dispatch kind]]s against that one pool. That makes
it the owner of dispatch concurrency. It supersedes continuous dispatch, which
is deprecated in its favour but kept for operators who want no daemon and
retained as the Console's engine. It is distinct from the *container-runtime*
daemon (podman/docker) that the bwrap runtime is "daemonless" of. spindrift's
Daemon is runtime-agnostic and drives a bwrap harness as readily as an OCI one.
_Avoid_: service, scheduler, supervisor, dogfood loop.

**Awake window**:
The wall-clock span during which the [[Daemon]] may *start* a launcher
invocation, expressed as one `start-end` string in a configured timezone.
A window whose end precedes its start wraps past midnight. The overnight
case is supported, not an error. It gates only starting work. A Box in
flight when the window closes runs to completion, and so does the settle that
follows it, because killing work already paid for saves nothing.
_Avoid_: schedule, uptime, business hours, quiet hours (it names when the
Daemon is permitted to act, not when the repository is quiet).

**Console**:
The interactive driving loop. It is a launcher session in which an operator
chooses the running work by Picking issues (promoting them as needed), watches
live Dispatches, drills into each Dispatch's work, and ends them (Unpick,
Terminate). Discovery is picks-only. Nothing launches that the operator did
not Pick, and "pick all ready" is an explicit bulk gesture, not standing
discovery. The issue listing is advisory and the claim authoritative, so a stale
listing can only produce a failed claim, never a wrong dispatch. The session
queue is in-memory. Durable state lives on the Issue Tracker alone. The Console
is a peer of the headless driving loops (the [[Daemon]], CI), not a
replacement for them.
_Avoid_: TUI (names the rendering, not the role), dashboard (it drives, not
merely displays), monitor.

**Section**:
A named slice of the session's issues the Console shows one at a time: Backlog,
Running, Held, Settled, or Failed. The Backlog section is the pick source. The
rest slice the work queue of already-Picked issues by their state.
_Avoid_: tab (names the widget, not the slice), view, filter (the Backlog's own
label filter is a separate thing).

**Backlog**:
The Console section listing queueable issues an operator can Pick. It is the
pick source, distinct from the work queue of issues already Picked.
_Avoid_: inbox, todo, queue (that is the picked work, not the source).

**Viewport**:
The Console-internal geometry module owning scroll state for every scrolling
pane: offset, cursor, cursor-follow, and clamp-on-shrink behind a small
interface, with height 0 meaning unbounded. It is pure geometry. It returns
visible-slice bounds and hidden-above/below counts, and the view renders the
hint strings ("… N more below"), so restyling never touches it. One
implementation serves the backlog/queue columns, the drill-in pane, and the
rebuild-output pane. Decided 2026-07-18 (#1540).
_Avoid_: scroll region, pager, window (that names its output value, not the
module).

**Layout**:
The pure resolver (`cmd/launcher/internal/console/layout.go`,
`resolveLayout(m Model) layout`) that decides the console's render geometry
once per Model snapshot. That covers the branch (docked sidebar, floating
sidebar modal, fullscreen sidebar, fullscreen detail modal, or plain) plus every
pane budget, wrap width, and scroll-clamp height. View's branch pick and
Update's viewport clamps use the same value instead of separately
re-deriving it (issue #2922, retiring a decision that used to drift out of
sync between the two, e.g. issues #829/#1501/#1755).
_Avoid_: render (Layout decides geometry; it draws nothing itself —
layout_purity_guard_test.go pins it, issue #3019).

**Quickstart**:
The pre-CLI interactive scaffolder. It is a nix app (`nix run
…#quickstart`, `apps.quickstart`), not a Harness subcommand, and it takes an
operator from zero to a validated, buildable Consumer flake in one command. It
runs *before* the `spindrift` binary exists. That is why it cannot be a
subcommand: `runtime`/`driver` are baked into the wrapper the binary is built
from, and the fields it sets live in `flake.nix`, not env. It is a TTY-only
wizard that detects what it can (container runtime by `podman → docker →
nerdctl(⇒ rancher) → bwrap`, host git identity, repoSlug from `git remote`,
an ambient token) and asks only for the rest. It then writes a minimal
generated `flake.nix`, a secrets-only `harness.env`, a `.gitignore`, and an
`.envrc` (no `prompts/`, since the Harness defaults every prompt), and finishes
through `spindrift doctor` and `spindrift build` (ADR 0027). It refuses to
overwrite an existing flake without `--force`.
_Avoid_: init, wizard (names the UI, not the role), bootstrap (the launcher's
internal launch-context wiring).

**Answers**:
The Quickstart's collected operator decisions, as one value (runtime, driver,
repo slug, git identity, tracker settings, …). The wizard's prompt/detect phase
produces it, and a pure scaffold render consumes it and returns the generated
files as values. The injection-guarding nix escaping happens at that single
seam. The wizard keeps the writes, the clobber/backup policy, and the final
doctor/build step. Decided 2026-07-18 (#1548).
_Avoid_: config (that names the generated files), wizard state, form.

**Pick**:
The operator's act of selecting an issue into the running session's queue for
dispatch. Picking an issue that is not yet `Dispatchable` promotes it through
the normal lifecycle transition first. The pick *is* the human launch button,
recorded durably on the Issue Tracker. A picked issue waits in the queue until
a parallelism slot frees. Queued-but-unlaunched picks hold at `Dispatchable`,
never `InProgress`.
_Avoid_: select, schedule, enqueue.

**Unpick**:
Retracting a queued-but-not-yet-launched Pick. It only edits the session queue,
with no Issue Tracker interaction. The issue stays `Dispatchable`.
_Avoid_: cancel (ambiguous with Terminate), dequeue, remove.

**Terminate**:
The operator-initiated ending of a live Dispatch, valid anywhere from claim to
verdict. It reaps any running Box, abandons the settle wherever it stands
(CI watch, fix pass, merge gate), and returns the issue to `Dispatchable`.
The issue is never `Failed` (nothing to triage; the human just decided) and
never in a new lifecycle state. Terminate abandons watching and never un-lands
work. Pushed branches and open PRs stay put, and the terminate comment on the
issue links them so a later re-dispatch can adopt rather than collide. The
ending is recorded outside the state machine, in a terminal line in the Box log
and that comment. It is distinct from Unpick, which retracts a Pick that never
launched.
_Avoid_: kill, cancel, abort.

**Reconcile**:
The `local`-tracker bookkeeping sweep that makes a local issue's native
open/closed axis match Code Forge reality. It is the only thing that closes a
local issue (ADR 0029). It only observes and never lands code. For each open
issue it closes the issue when its recorded landing PR is merged, discovers a
PR by agent branch when no landing was recorded (a box that died before its
outcome line), and flags one whose PR was closed unmerged. Only behind a
composite death signal (no PR/branch, a stale Box log, and an absent container
when the runtime is reachable) does it reset an orphaned `InProgress` to
`Dispatchable`. That signal is the liveness check #600 required before any such
reset. Against a push-only `local` Code Forge it parses the recorded string
into a [[Landing]] instead. An `IntegrationRef` closes on a confirmed merge
exactly like a PR. Reconcile repairs a `BranchRef` that is still an ancestor of
its own Integration branch in place (landing upgraded, seam closed) rather than
leaving it stuck open, and prints a stuck verdict naming the branch for one
that isn't (issue #1809). It runs automatically at the end of a `dispatch` run
and is available standalone (`spindrift reconcile`) for the between-runs cases:
a runner that died, or a PR waiting on approval. It does not merge. Landing
stays with `dispatch` and the explicit `recover` gesture. _Avoid_: sync, sweep, cleanup, recover
(the operator's explicit per-issue adopt-and-merge gesture, a different
act).

**Recoverable**:
A terminal lifecycle state in its own right (ADR 0039) that a
`CODE_FORGE=local` push-only Dispatch reaches when its work is stranded but
salvageable. A bundle is in the outbox and the driver's advisory self-report
says success, yet no genuine `status=ready` [[Outcome line]] ever settled it.
This covers the Window-3 false-`blocked` and the die-with-a-bundle cases.
Settle transitions into it *instead of* `Failed`, so the deliverable is neither
auto-landed on an unauthenticated signal (a local fast-forward has no draft-PR
review catch, unlike the PR-shaped forge that auto-adopts) nor lost to the
`Failed` triage queue. [[Reconcile]] skips it and never resets it toward
`Dispatchable`, so it is never silently re-dispatched. `spindrift recover` (the
`SettleRelayedBranch` arm, issue #2225) lands it. `doctor` and the
Console show a count. The operator's action becomes *recover*, not
*restart*. _Avoid_: stranded, salvageable, failed-recoverable (it is a state of
its own, not a flavor of `Failed`).

**Transcript**:
The Driver-rendered record of a Dispatch's work across its pass logs. It is
the full content the live-tail sidebar shows on toggle, with a raw-JSONL toggle
for the byte-exact form. It is a *view* of logs the Dispatch already produced,
not a separately stored stream. The condensed Activity feed is a second view of
the same logs, not a distinct timeline behind them.
_Avoid_: narrative log, running log, log.

**Activity feed**:
The Console's condensed, timestamped view of a running Dispatch's work, with
one line per Driver step, derived by replaying the Dispatch's pass log through
the heartbeat parser. It is the live-tail sidebar's default, following the
newest line until the operator scrolls back. A key toggles it to the full
[[Transcript]]. Like the Transcript, it is a view of logs the Dispatch already
produced, not a separately stored stream.
_Avoid_: heartbeat log, status log, activity log, event stream.

**Outcome line**:
The machine-readable final line a Box writes to stdout. The Launcher parses it
to learn where the deliverable landed and whether the Dispatch is ready for
settle, blocked, or failed. Grammar:
`SPINDRIFT_OUTCOME issue=<num> landing=<ref> status=<status> note=<text>`
where `note` may contain spaces and `=`. `landing` is the landing reference:
a PR URL (`github` Code Forge), a branch ref (push-only `git`), or a
verdict-comment URL (research dispatch). `status` values are scoped to the
Dispatch kind (`ready`/`blocked`/`ambiguous` for work, the verdicts plus
`blocked` for research). An optional trailing `synthetic=true` field marks
the line as the ADR 0036 backstop the Launcher adds host-side when a
Box never printed a real outcome line. The synthetic `blocked` mentioned
below is one such line. Unlike the mid-run signal channels below, this line carries no
per-run control nonce (`RUN_NONCE`, issues #1937/#1939). **Structure** defends
`SPINDRIFT_OUTCOME` instead. In-box, the per-driver extractor
jq-scopes to the agent's own final message (claude
`select(.type=="result")`, opencode `select(.type=="text")`) and re-emits one
**bare, leading** line. Host-side, `lastInLog` treats only a leading-token
line as a candidate. Untrusted text reaches the log only as a
`tool_result` (wrong event type) or buried mid-JSON in the raw transcript
(non-leading), so it cannot win regardless of any nonce. ADR 0055 records the
decision to retire the nonce here, since structural scoping is the freshness
boundary. It keeps the nonce as the _sole_ replay defense for the mid-run
signal channels (`SPINDRIFT_COMMENT` / `SPINDRIFT_PR_INTENT` /
`SPINDRIFT_ISSUE_INTENT`), which cannot be structurally scoped because they
survive only as a token embedded in one JSON-escaped line. The line carries only what the Launcher cannot know without the
Box, never backend identity or other run config, which the Launcher already
holds authoritatively. The `cmd/launcher/internal/outcome` package is the
authoritative spec and implementation. `Parse` validates the grammar, `Line`
produces the canonical form, and `lastInLog` scans a Box log while
skipping lines too large for the scanner buffer. The line is *evidence*, not
the verdict. The Launcher alone decides a run's
disposition (ADR 0039). It reads the line alongside the outbox bundle (if
present, commits exist), the driver's advisory self-report, and the Box exit
class. So the Launcher promotes a synthetic `blocked` or an absent line whose
work landed from that evidence rather than taking it at face value. The Box
advises and the Launcher decides.
_Was_: `pr=<url>`, renamed once the field carried branch refs and comment
URLs. PR-vs-issue is a GitHub-ism that confuses on split backends.
_Avoid_: result line, output line, status line.

**Review verdict marker**:
The `"VERDICT: APPROVE"` / `"VERDICT: BLOCK"` marker that review-prompt.md
requires a review-owned pass's output to carry.
`passmachine.Scan(rendered string, kind PassKind) ScanResult` (issue
#2980) scans for it and is the single owner of the match rule. It replaced ad
hoc scanning duplicated between `orchestrator/run.go`'s `scanPassLog` and
`scanReviewLog` (now thin wrappers over it). It is distinct from [[Verdict]].
That one is `localloop.Verdict`, Surface's per-broad-ticket report. This one
is the orchestrator's own review-loop marker, which a Box produces mid-run. The
English word is the same but the concepts are unrelated, which is why this
entry is named "Review verdict marker" rather than bare "verdict". `Scan`
applies one of two match rules keyed by `PassKind`. `KindReview` gets the
strict rule (first line, last-block-wins), because review-prompt.md's own
contract puts the marker at the top of the review pass's own top-level message.
Every other kind falls back to a BLOCK-dominant fold, because a verdict can
otherwise reach a non-review pass's transcript only through a nested subagent's
`tool_result`, not a top-level message of its own. Issue #2980 scoped that
fold structurally. `RenderTranscriptWithRole`
(`driver/claude/transcript_render.go`) tags a `tool_result` line with a
completed subagent's own role only when it structurally answers that
subagent's Task/Agent spawn, and the fold now reads only lines tagged
`[reviewer]`. A plain Bash/Read `tool_result`, or one tagged with some
other subagent's role, can no longer satisfy a verdict. That closes a
prompt-injection gap where planted text anywhere in the transcript could
flip a non-review pass's fold by substring match alone. The fold
itself stays as an extra layer of defense, but it is no longer the only
one. `lib/prompt-contract.nix`'s `markerChannels` registry carries this
channel's trust model as the `review-verdict` row: `defense = "structural"`
(its own extractor is the `[reviewer]` tag plus kind-scoped `Scan`, the same
category outcome's row occupies for its own, different mechanism), `carrier
= "subagent-first-line"`.
_Avoid_: verdict marker (too easily read as the Surface [[Verdict]]).

**Signal carrier**:
Which path a mid-run signal channel (`SPINDRIFT_COMMENT` /
`SPINDRIFT_PR_INTENT` / `SPINDRIFT_ISSUE_INTENT`) takes across the Box seam,
chosen per Dispatch by `BOX_SIGNAL_CARRIER`. It is either `log`, the
nonce-guarded marker line the Launcher scrapes from the driver log after Box
exit, or `socket`, the [[Signal socket]]. One knob moves all three channels
together. The [[Outcome line]] stays on the log either way (ADR 0052).
_Avoid_: transport (the unix-versus-TCP layer beneath a carrier), mode.

**Signal socket**:
The Launcher-owned per-Box listener that carries the mid-run signal channels
when the [[Signal carrier]] is `socket`. The Box posts a comment, PR intent
or issue intent through the `driver-exec signal` verb and receives a
synchronous accept or reject. The Launcher validates on receipt, buffers the
accepted body in memory, and performs the write at settle. It carries the
*content* of a write, never its destination, and shares the registry proxy's
transport plumbing without sharing its listener (ADR 0052).
_Avoid_: relay (the bundle act, see [[Mediation]]), control socket, RPC.

**Resolved outcome**:
The value `outcome.Resolve` (issue #2260) returns. Resolve is the one seam that
decides what a Dispatch's [[Outcome line]] evidence actually says, across every
pass log, in a single call a test can drive end-to-end. Before it existed, the
`outcome` package hosted six log scanners with overlapping-but-divergent
nonce/synthetic/last-wins selection rules. "What really happened in the
Box" came out of four layers spread across three packages with no single seam
to test (issue #2251). Resolve is that seam. It walks a Dispatch's
ordered pass logs once and chooses among three tiers in strict precedence: a
genuine driver-authored outcome line, the outcome backstop's synthetic line
(ADR 0036), and, only when neither turns up anywhere, the driver's
unauthenticated self-report fallback (structurally scoped, never nonce-gated,
ADR 0055). It names which tier won on `Resolved.Provenance` (`ProvenanceGenuine` /
`ProvenanceSynthetic` / `ProvenanceSelfReport`). The per-log scanners the
tiers walk (`lastInLog`, `lastSelfReportInLog`) are now unexported and
reachable only through Resolve. The one other exported entry point is
`LastSelfReport`, a single-log read for a caller that wants the self-report
signal unconditionally, alongside a possibly already-found genuine outcome,
rather than weighed against it as Resolve's own last-resort tier does.
The seam's consumers now read through it. `dispatch.Result` carries the
whole `Resolved` value, settle branches on `Result.Resolved`
(`Provenance`, `SelfReport`) without ever scanning a log itself, and
dispatch's own single-log scans (`successResult`, `settledOutcome`)
deliberately exclude the self-report tier, settling only on a genuine or
synthetic outcome.
_Avoid_: box truth, verdict.

**Guardrail prompt**:
The harness-owned prompt carried in the system slot of every harness-issued
Driver invocation, stating the trust boundaries to the model. Issue bodies and
comments are untrusted data, credentials are never disclosed, work stays on the
dispatched issue, and edits to the guarded paths force a human merge.
The Agent and issue/comment authors cannot override it. The Consumer flake,
which is trusted by construction, can. It is a soft control. It hardens
harness-issued invocations but cannot bind a fresh Driver process the Agent
spawns itself. That containment belongs to the Box and token scope.
_Avoid_: system prompt (ambiguous with the Driver's own), jailbreak prompt,
safety prompt.

**Conditional fragment**:
An opt-in prompt step rendered into an Agent prompt only when its gate is on.
Each is one registry row (gate variable, fragment file, substitution variable)
in a harness-owned nix registry. A single entrypoint loop reads the rows and
also derives the substitution allowlist from them. Gates are normalized
to env-nonempty on launcher-delivered Box plumbing. Computed gates (skills
discovery, filer, caveman) are precomputed into variables before the loop.
It replaces the six hand-unrolled "conditional residue" blocks in
`phase_prompt_assembly`. Decided 2026-07-13; lands with the prompt-registry
work. _Avoid_: prompt toggle, feature flag, optional section.

**Settle**:
Driving a Dispatch from Box-exit to its terminal lifecycle state, whatever
that takes: interpreting the Outcome line, watching CI, self-heal fix passes,
the merge or push-only landing under `MERGE_MODE` and the Merge guard, and
merged-verification. "Merge gate" informally names the segment from green to
merge within a settle.
_Avoid_: gate (a checkpoint — narrower than the whole settle), finalize, report.

**Mediation**:
Settle's host-mediated PR hand-off module (`settle.Mediation`). It puts relay,
intent, closes-ref, and create/adopt behind one interface.
_Avoid_: Handoff (the prompt-assembly descriptor), relay (the bundle act).

**Merge guard**:
The launcher-side check, outside the Box and beyond the Agent's influence,
applied between green CI and merge. If the change touches a guarded path, the
merge downgrades to manual regardless of `MERGE_MODE`, and a note on the PR
says why. It limits injection-induced *drift* (an agent following its normal
flow), not a fully adversarial Agent, which holds a token that can merge (see
Two-actor separation). It downgrades and never blocks, so the cost of a hit is
one human read.
_Avoid_: path filter, merge block, review gate.

**Instruction surface**:
The repo-carried files a Driver reads as trusted instructions on every fresh
clone (`CLAUDE.md`, `AGENTS.md`, `.claude/`, `.opencode/`), plus the CI config
under `.github/` and `.forgejo/`. They are how an attack persists across runs:
a poisoned instruction file merged once feeds every future Agent as trusted
input. The Merge guard's default path set covers them.
_Avoid_: config files, dotfiles, prompt files.

**Two-actor separation**:
The opt-in hard mode where the Box token's user cannot update the base branch
(repository ruleset) and only a second, launcher-held token can merge. It is
the only configuration in which the Agent cannot influence the Merge guard at
all. The single-token default gives that up for operator simplicity.
_Avoid_: dual token, split credentials, bot pair.

**Tripwire**:
The launcher's detection that a PR was merged by an actor other than itself
while the issue was `InProgress`, which is evidence the Agent (or someone else)
merged around the gate. It only detects and does not prevent. It flags the
merge for human triage.
_Avoid_: audit, alert, monitor.

**internals**:
`mkHarness`'s return-value attrset holding the outputs checks/fixtures reach
for but that aren't themselves part of the Consumer contract (build/run
fixtures, contract files, `driverExecBin`, `roster`, …). It is the counterpart
to the Consumer-facing `image`/`spindrift`/`packages`/`apps` keys. The versioned
Consumer contract (ADR 0010) covers only those few Consumer-facing keys.
`internals` sits outside it and is free to churn, the nix-attrset equivalent of
`cmd/launcher/internal/*` (issue #2529). Some `internals` keys (manpage,
completions) are also separately reachable via `packages.spindrift-*`.
_Avoid_: private outputs, test outputs.
