# spindrift

A nix-based harness that launches waves of headless Claude Code agents into
disposable, nix-built containers — one per GitHub issue. This glossary pins the
vocabulary of the harness and the parties around it.

## Language

**Harness**:
spindrift itself — the flake, the launcher, and the in-container entrypoint that
together build the image and launch agent waves. The thing being imported.
_Avoid_: tool, framework, runner (the runner is specifically the container).

**Consumer flake**:
The downstream flake that imports the Harness and configures it (toolchain,
packages, prompt, settings). A role, not necessarily a separate repo — it may be
the same repo as the Target repo.
_Avoid_: client, user repo, parent flake.

**Target repo**:
The repository whose issues the agents work. Always cloned fresh inside the Box
from a git *remote* — `REPO_SLUG` on GitHub under a `github` Code Forge, or a
plain remote URL under a `git` Code Forge — never read from a host checkout. So
it stays a distinct role from the Consumer flake even when they are the same repo.
_Avoid_: source repo, project repo.

**Agent**:
A single headless Driver process — `claude -p …`, run with
`--dangerously-skip-permissions` — working one issue inside one container.
The Agent is the running process; the Driver is which CLI it is. (`opencode
run …` is the seam's design target — see **Driver** below — not a CLI that
ships today.)

**Driver**:
The swappable agent CLI baked into the Box. `claude` is the only Driver
implemented today; `opencode` is designed for (ADR 0009) but not yet built. A
build-time seam (one Driver per image, picked beside `runtime`), analogous to
the Forge and runner seams. Each Driver normalizes its tool's quirks at its own
boundary and has two coordinated halves keyed by one name — a nix-generated
in-box half (invocation, agent-config, outcome extraction) and a Go host-side
strategy in the launcher (transient classification, heartbeat, usage
extraction). _Provisional
name_ — may be renamed (e.g. "agent harness"). _Avoid_: engine, backend, tool.

**Provider**:
The model backend a Driver talks to. Only Anthropic is available today, via
the `claude` Driver, which is effectively locked to it; GitHub Copilot and
OpenAI are design targets (ADR 0009), not yet built. Distinct from the
Driver — once `opencode` ships, it is meant to be provider-flexible, so
"GitHub Copilot support" would be the opencode Driver pointed at the
`github-copilot` provider, with `MODEL` provider-namespaced (`github-copilot/…`).
_Avoid_: model host, vendor, backend.

**driver-exec**:
The in-box, nix-built Go unit that runs one Driver invocation: it takes the
prompt/agents/session file paths, the Driver's bin and flags, and a
`--devshell` switch; spawns the Driver (via `nix develop --command` when
asked), tees the stream to the Box log, filters heartbeats in-process
(absorbing the former standalone heartbeat-filter binary), and returns the
Driver's exit code. Owns process mechanics — invocation data and outcome
extraction stay with the Driver's nix half (ADR 0009). Replaced
entrypoint.sh's temp-file/eval marshalling across the devShell process
boundary (issue #626). Its `bundle-out` verb (issue #1808) extends it beyond
process mechanics into CODE_FORGE=local's harness-owned code-out: after the
Driver exits, it bundles the base..agent-branch range into the outbox itself
instead of trusting the Agent to run `git bundle create` — the Agent's own
contract there shrinks to "commit on the branch," identical to every other
Code Forge. An empty range against a claimed `ready` outcome gets a
corrective `status=blocked` SPINDRIFT_OUTCOME line instead of settling as a
false ready.
_Avoid_: runner (that is the Box isolation seam), wrapper, shim.

**Filer**:
The opt-in subagent role (beside the scout and reviewer) that turns into issues
on the Issue Tracker the non-blocking findings the work loop escalated for a
human — not the whole Non-blocking section: cheap, in-scope findings are fixed
inline in the same effort, and only design trade-offs, out-of-scope work, or
too-large changes reach the Filer. One issue per surviving finding, merging
only findings that are the same change, after a dedup search over previously
filed findings in any state (a closed finding is a human triage decision, never
refiled). Its issues carry the
`agent-review-finding` label and are never dispatchable by its own hand — a
human promotes them, preserving the rule that a human is the launch button.
Filing is best-effort: a Filer failure never blocks the PR or alters the
outcome. Off by default.
_Avoid_: triager (it does not triage), reporter (collides with outcome
reporting).

**Box**:
The disposable per-issue podman container — the isolation boundary that makes
`--dangerously-skip-permissions` safe. The `runtime` knob picks the OCI CLI
that drives it: `podman`/`docker` name the binary directly; `rancher` is an
operator-facing alias for Rancher Desktop's containerd mode and is the first
value that differs from the binary it execs (`nerdctl`) — the one alias lives
in the runner package, shared by adapter construction and validation.
_Avoid_: sandbox, runner, worker.

**Harness plumbing**:
The language-agnostic tools every Agent needs regardless of the Target — shell,
git, gh, the Driver CLI, jq, CA certs, nix. Always baked into the image and
always kept on PATH, even when the Agent operates inside the Project toolchain.
Distinct from the Project toolchain: plumbing is spindrift's, the toolchain is
the Target's.
_Avoid_: base image, harness deps, system tools.

**Project toolchain**:
The Target's language/build tools (rustc, node, sqlx, …). Sourced devShell-first:
when the cloned Target has a usable devShell the Agent operates inside it via
`nix develop` (the default, zero-config path); otherwise it falls back to the
baked `packages` list. Baking is an opt-in *speed* knob — a warm store so the
runtime `nix develop` substitutes nothing — not the primary source (ADR 0014).
_Avoid_: packages, baked toolchain, dependencies.

**Issue Tracker**:
The seam that supplies work and carries dispatch state: listing dispatchable
issues, reading an issue's body/title/state, transitioning its Dispatch state,
and posting comments. One of two independent axes (the other is the Code Forge).
Implemented adapters: `github` (issues via `gh`), `jira`, and `local` (issues
as files in the Target repo, no server). The launcher reasons in canonical
Dispatch states, never in a backend's native mechanism.
_Avoid_: issue source, ticketing, backlog.

**Content plane**:
An issue's body and comments — the text a Dispatch reads to do its work and
the comment it writes back — as distinct from the Dispatch lifecycle (its
state transitions). The Launcher is the sole writer on both planes; reads,
though, are per-tracker — in-box for a remote tracker, host-mediated for
`local` (ADR 0032).
_Avoid_: issue data, payload.

**Remote / local (in-box reachability)**:
The organizing split on *both* backend axes: whether a backend is reachable from
inside the Box. **Remote** backends (`github`; `gitlab`/`bitbucket`/`jira` as
they land; the `git` code forge) are reached in-box — over the network via their
own client — so the Box reads and lands directly. **`local`** is unreachable
in-box (no server, git-ignored, absent from the fresh clone), so it is
**host-mediated**: a read-only mount in, a Launcher-applied artifact out. On the
issue plane the Box reads its issue through a read-only view of the issues
directory and never writes it, emitting comments for the Launcher to post (ADR
0032); on the code plane the Box clones a read-only mount of the Accumulation
repo and emits a bundle for the Launcher to land (ADR 0033). These read-only
mounts, plus the writable outbox, are the documented exceptions to
zero-shared-host-filesystem.
_Avoid_: online/offline, connected/disconnected.

**Code Forge**:
The seam through which the Harness lands code — the narrow core every adapter
honors with real behavior: agent branch naming, rebase, merge/landing under
`MERGE_MODE`, and a connectivity probe. A second axis independent of the Issue
Tracker, freely combinable with any of them. A git endpoint always exists,
split — like the Issue Tracker — by in-box reachability: **reachable** endpoints
(`github`, `git`) let the Box clone from and push to them directly; the
**host-mediated** endpoint (`local`) is not reachable in-box, so the Launcher
mediates. Three values:

- `github` — the full flow: open a PR, watch the CI rollup, rebase, merge. The
  `gh`-exec adapter; the only value that additionally implements **PRForge**
  (see below).
- `git` — **push-only** to a plain git remote URL (self-hosted git, gitea,
  GitLab-without-MRs, a bare server repo): clone, commit to a per-issue branch,
  push, and stop. No PR, no CI, no merge gate — it implements CodeForge only,
  with no stub methods. `MERGE_MODE` maps to remote pushes — `manual` pushes
  the feature branch; `immediate` pushes straight to the target branch; `auto`
  is native GitHub auto-merge and has no meaning here.
- `local` — **host-mediated**, the code-plane mirror of the `local` tracker
  (ADR 0033): the Box clones from a read-only mount of the Accumulation repo and
  emits its branch as a git bundle through a writable outbox; the Launcher lands
  it host-side by merging onto the per-ticket Integration branch. No network on
  the code plane. Shares the `git` adapter's substrate internally (branch
  naming, the temp-clone landing helper); differs only in the host-mediated
  code-out channel.

The fully-local code path was **cut** by ADR 0013 ("a git remote is a hard
requirement") and **reopened** by ADR 0033 on new terms: not a read-write mount
of the operator's repo, but the host-mediated `local` value above — a read-only
clone mount in, a Launcher-landed bundle out. Solo/private use is served either
by `local` here (private issues *and* private local code) or by pairing `git`
with `ISSUE_TRACKER=local` (private issues, published code).
_Was_: "Forge" (a single seam over the Target repo host); split into Issue
Tracker + Code Forge once issues and code host became independent axes.
_Avoid_: GitHub adapter, API layer, client wrapper.

**PRForge**:
The optional PR, CI-rollup, and auto-merge surface (`OpenPRForBranch`,
`PRForBranch`, `PRState`, `CheckState`, `FailureDetail`, `ListPRFiles`,
`CanAutoMerge`, `EnqueueAutoMerge`) split out of Code Forge (ADR 0013
amendment, issue #517). Only the `github` adapter implements it; callers
discover it with a type assertion — `pr, ok := cf.(forge.PRForge)` — the
standard Go optional-interface pattern, rather than a `PushOnly()` capability
flag. `internal/settle` is the primary consumer: it resolves `PRForge` once at
construction and branches on its presence to skip the CI-wait/merge-gate
entirely for a push-only forge.
_Was_: a `PushOnly()` bool on the combined Code Forge interface, plus six
stubbed methods on the `git` adapter.
_Avoid_: PR client, GitHub-only interface.

**Accumulation repo**:
The Launcher-owned bare git repo that the `local` Code Forge (ADR 0033)
accumulates code in — the code-plane sibling of the `.spindrift/issues/`
directory. Defaults to `.spindrift/accum.git` under the launcher's working
directory; `CODE_FORGE_ACCUMULATION_REPO_DIR` overrides it. The Launcher
auto-creates and seeds it (base branch ref from the operator's local
checkout, offline) before any Box runs, idempotently on every run
thereafter; each seam clones it through a read-only mount and lands onto an
Integration branch inside it. Deliberately *not* the operator's working
checkout: a bare repo has no checked-out branch, keeps agent refs and
objects out of the operator's repo, and resets with `rm -rf`.
_Avoid_: mirror, staging repo, local remote.

**Integration branch**:
The branch in the Accumulation repo where all the seams of one broad ticket
converge, keyed on *each seam's own* local issue's `parent` frontmatter
(`integration/<sanitized-parent>`) — never a single knob shared across a run,
so a mixed-parent dispatch batch converges each seam onto its own branch (ADR
0033, issue #1734). An issue with no `parent:` set is its own broad ticket,
keyed on its own sanitized slug instead of a shared fallback branch. `parent`
is opaque and operator-authored — spindrift never resolves it against another
tracker, it only sanitizes it into a git-ref-safe token (lowercased, each run
of non-`[a-z0-9]` characters collapsed to a single dash, leading/trailing
dashes trimmed) before forming the branch name. Each seam's landing is a
host-side merge onto it; once every one
of a broad ticket's seam issues is closed, the Launcher auto-surfaces its
current tip into the operator's checkout as a local branch named after the
ticket (issue #1730) — the operator still publishes the single team PR by
hand. Distinct from a seam's per-issue agent branch, which merges *into* it.
_Avoid_: feature branch, epic branch, accumulation branch.

**Landing**:
The sealed `forge.Landing` value (`cmd/launcher/internal/forge`, issue
#1809) a stored landing string parses into, via one `ParseLanding` function,
at the two seams that consume it: settle's post-merge upgrade and
Reconcile's verification. Three variants: `PRURL` (`github`'s landing
grammar), `BranchRef` (a raw pre-merge branch name — `local`'s landing
string before a merge lands, and `git`'s only shape), and
`IntegrationRef` (`local`'s post-merge `<Integration branch>@<sha>`, ADR
0029/0033). Storage (issue frontmatter, the outcome line) and every
remote-tracker interface keep the plain string unchanged; only the two
consuming seams match on the typed variant instead of each re-deriving the
three-grammars-in-one-string ambiguity itself. Finding a `BranchRef` already
an ancestor of its Integration branch means the merge landed but the
post-merge upgrade never ran — Reconcile repairs it in place (upgrades the
recorded landing to `IntegrationRef`, closes the seam) instead of leaving it
stuck open silently forever; not yet an ancestor prints a loud stuck verdict
naming the branch.
_Avoid_: landing ref, landing string (fine for the untyped stored form;
ambiguous once the typed value is in scope).

**localloop**:
The `cmd/launcher/internal/localloop` package: CODE_FORGE=local's per-issue
Code Forge construction, outbox resolution, and parent resolution, plus the
reconcile/surface hookup, behind one `Wire` constructor (issue #1806,
campaign #1803 T1). The launcher's command path and the package's own
composed **local loop** test (seeded Accumulation repo → fixture commit →
bundle in the outbox → settle → reconcile → surface) drive the identical
composition, so a regression in any gate names itself in one place instead
of two independently maintained copies drifting apart. `Wire` resolves each
issue's parent exactly once, sealed as `local.SanitizedParent` — a struct
mintable only by the parent-resolution function — and hands that one value
to the forge constructor, the launcher's BASE_BRANCH resolver, and surface
grouping; `IntegrationBranch` and its siblings accept only the sealed type,
so an unsanitized string can't reach a branch name (issue #1810).
_Avoid_: local-loop glue, launcher wiring (both too vague — `localloop` is
the package name).

**Conformance contract**:
The executable contract for the forge seams: a shared `forgetest` suite that
every adapter of a seam interface — the shared test Fake included — must pass,
run hermetically via per-adapter scripted-backend harnesses (stubbed `gh`
exec, `httptest` Jira, tempdir local tracker) with seeding and fault-injection
hooks. Replaces fidelity-by-comment in the Fake ("mirroring the real
adapter's…") with a test failure on drift. Three contracts, landed in order:
Issue Tracker (#1544), Code Forge (#1545), PRForge (#1546). Decided
2026-07-18.
_Avoid_: parity test, integration suite (it is hermetic), mock verification.

**Backend matrix**:
Issue Tracker and Code Forge are two independent, freely-combinable axes
(`ISSUE_TRACKER` × `CODE_FORGE`). All cells are permitted — the harness does not
reject "incoherent" pairings (e.g. github-issues + no-code-forge); an operator
who selects one owns the consequences. `local × local` is the fully-specified
cell — the **local loop**, both planes host-mediated and offline (ADR 0033);
`CODE_FORGE=local`'s per-ticket Integration branch assumes a tracker that
supplies a `parent`/epic link, which `local` does today and other trackers do
as their sub-issue links land — an issue with no such link is simply its own
broad ticket rather than an unspecified case.
_Avoid_: preset, profile, mode.

**Launcher input**:
The nix-rendered JSON document that carries every nix-computed value from the
generated wrapper to the launcher through a single `--input` store path: a
`settings` section (the resolved knob values after the Consumer flake's
`settings` are applied) and an `artifacts` section (built references — image
archive, agent files, driver name, …). Knob precedence is document < explicit
CLI flag; ambient env no longer configures knobs (staged: warn, then error)
and remains only for secrets and launcher→Box plumbing. The document is the
Consumer flake's voice, flags are the operator's per-run voice, env is
secrets. Decided 2026-07-13 (ADR 0020), replacing the exported
`VAR="${VAR:-baked}"` run preamble whose env-wins fallback let ambient
variables silently override flake settings.
_Avoid_: config file (generated, never operator-edited), env preamble,
defaults preamble.

**Dispatch**:
The per-issue execution, from claim to verdict: every Box launched for one
issue — initial run, fix passes, conflict-resolve — plus its results and its
Driver-cache entry. The thing whose states the Dispatch lifecycle names;
distinct from the Driver's resumable conversation session, which the
Driver-cache entry preserves across a Dispatch's fix passes.
_Avoid_: session (collides with the Driver's conversation session), run, job.

**Dispatch kind**:
The axis naming what a Dispatch delivers: `work` (the original kind — lands
code through the Code Forge) or `research` (lands a verdict and enrichment
comments on the Issue Tracker; never touches the Code Forge). Kinds share the
canonical Dispatch lifecycle; on the `github` tracker each kind maps the
states to its own label family.
_Avoid_: mode, dispatch type, pipeline.

**Research dispatch**:
A Dispatch whose Agent (the "researcher") reviews a posted issue from inside
the Box — exploring the Target repo for real context — then posts an
enrichment comment and a verdict. Advise-only: it never promotes an issue to
dispatchable, never closes one; a human acts on the verdict, preserving the
rule that a human is the launch button. Verdicts are a closed set carried by
`Complete`: `recommend` (relevant, enriched, ready to promote), `reject`
(false positive, not worth doing, or duplicate — reason in the comment),
`unclear` (relevance needs answers only a human has; answer, then re-apply
the trigger label to re-research). A crashed or verdict-less Box is `Failed`,
never a verdict. On `github` the label family is `agent-research` (dual-role:
standing state and trigger) → `agent-research-in-progress` → terminals
`agent-research-recommend` / `-reject` / `-unclear` / `-failed`.
_Avoid_: triage (the human action on `Failed` issues), scout (the in-box
subagent role).

**Dispatch lifecycle**:
The canonical dispatch states the launcher reasons in, independent of how any
one Issue Tracker stores them: `Dispatchable` (a human marked the issue ready —
the launch button) → `InProgress` (a Box has been dispatched; re-runs skip it) →
`Complete` (the agent has nothing left to do — its landing path has settled:
a merged/handed-off PR on `github`, a landed branch when the Code Forge is
push-only/absent, or a posted verdict for a research dispatch) or `Failed`
(the Box crashed, never reached green past MAX_FIX_ATTEMPTS, or — on `github`
— a force-pushed head from rebase-retry or an agent conflict-resolve box never
re-confirmed green; human triage, re-transition to retry). Each Issue Tracker
adapter maps these states to its native mechanism:

- `github` — labels (`ready-for-agent` → `agent-in-progress` →
  `agent-complete`/`agent-failed`), swapped atomically. This is the original,
  unchanged mechanism.
- `jira` — a configurable status mapping (state → that project's workflow status
  name/id), since there is no universal Jira workflow to assume; when a state is
  unmapped, or the mapped transition isn't available on the issue's current
  workflow, falls back to swapping a Jira label for that state (mirroring
  `github`'s labels) so the lifecycle always makes progress.
- `local` — a `state:` field in the issue file (frontmatter), rewritten in
  place. Independently, a local issue also carries a native open/closed axis (a
  `closed:` field, absent = open), the same way a GitHub issue is open/closed
  independent of its dispatch labels; it is driven not by the launcher's
  lifecycle but by Reconcile (ADR 0029).

On `github`, `Complete` is swapped once the landing path settles, not at first
green: `immediate` mode can still do real agent work after green (rebase-retry,
an agent-dispatched conflict-resolve box, a post-force-push CI re-wait), and
the issue stays `InProgress` throughout that work so the label never claims
"nothing left to do" while a Box may still run. MERGE_MODE governs that
landing path: `immediate` merges automatically (locally, via a push that
updates a clean checked-out branch); `manual` (default) leaves the branch/PR
for a human; `auto` is native GitHub auto-merge and has no meaning off
`github`. A merge failure after green leaves the issue `Complete` with a
merge-blocked note — never `Failed` — once that landing attempt settles,
except when the post-force-push re-wait (after rebase or conflict-resolve)
ends red or times out, or the conflict-resolve dispatch itself fails: there
the force-pushed head never went green, so the issue ends `Failed` instead.
_Was_: "Label lifecycle" — labels were GitHub's storage mechanism, mistaken for
the states themselves.
_Avoid_: status, queue, state machine.

**Dispatch order**:
The order in which `ListIssues` hands work to the launcher — canonically
oldest-first. Identity is opaque to the launcher (`Number` is a string), so the
launcher never parses or compares IDs numerically; each Issue Tracker adapter
returns issues already in canonical order using its own order key: `github` =
issue number, `jira` = created time, `local` = a `created` frontmatter timestamp.
_Avoid_: issue number, sequence, priority.

**Wave**:
One batch of Dispatches launched concurrently. With no blocker edges the whole
ready-set is a single wave; declared edges split a run into dependency waves.
Edges come from each Issue Tracker adapter's `DepsOf`: `github` and `jira`
both prefer native dependency relationships (GitHub's issue-dependencies API,
Jira's "is blocked by" issue links) and use body/prose refs only as a
fallback where the adapter has one — native wins whenever it's non-empty,
never merged with body text. `DepsOf` tags each ref with the source it
resolved from (native vs body), carried alongside the edges as `Sources` and
surfaced in every operator-facing blocker rendering — `preview`'s blocker
annotations, blocked-skip notices, and the blocked-claim marker (and the
release comment posted from it) — so drift between a stale body section and
changed native links is visible instead of silent.
Every dispatch invocation runs at most one wave (ADR 0019): `MAX_PARALLEL`
caps the number of concurrent Boxes within a wave (default 3); `MAX_JOBS`
caps the wave size (default 0 = uncapped). Held issues stay on the dispatch
label and are picked up by the next invocation — a driving loop (dogfood.sh,
CI, or a human re-running) drains a dependency graph wave by fresh wave; no
in-process poll waits for later waves. No label-based gate serializes issues;
ordering is purely by dispatch order and blocker edges.
_Was_: "fan-out" — the launch act and the batch carried two names; unified on
the batch noun.
_Avoid_: fan-out, batch, round.

**Readiness**:
The query seam answering "may this issue dispatch now, and if not, why"
before anything launches: per-issue blocker status (ready / blocked-by /
check-failed) with the native-vs-body `Sources` tags carried through. The
pre-dispatch consumers — the Console's held picks (#650) and `preview`'s
blocker annotations — use it; the wave engine keeps its own internal gate,
disabled only by an explicit `PreResolved` contract when a caller has already
resolved readiness through this seam. Decided 2026-07-18 (#1547), replacing
the exported blocker primitives and the empty-edges construction.
_Avoid_: blocker check, gate (that is the engine's internal act), preflight
(collides with the stale-base preflight).

**Console**:
The interactive driving loop: a launcher session in which an operator composes
the running work by Picking issues (promoting them as needed), watches live
Dispatches, drills into each Dispatch's work, and ends them (Unpick,
Terminate). Discovery is picks-only — nothing launches that the operator did
not Pick; "pick all ready" is an explicit bulk gesture, not standing
discovery. The issue listing is advisory and the claim authoritative: a stale
listing can only produce a failed claim, never a wrong dispatch. The session
queue is in-memory; durable state lives on the Issue Tracker alone. A peer of
the headless driving loops (dogfood, CI), not a replacement for them.
_Avoid_: TUI (names the rendering, not the role), dashboard (it drives, not
merely displays), monitor.

**Section**:
A named slice of the session's issues the Console shows one at a time — Backlog,
Running, Held, Settled, or Failed. The Backlog section is the pick source; the
rest slice the work queue of already-Picked issues by their state.
_Avoid_: tab (names the widget, not the slice), view, filter (the Backlog's own
label filter is a separate thing).

**Backlog**:
The Console section listing queueable issues an operator can Pick — the pick
source, distinct from the work queue of issues already Picked.
_Avoid_: inbox, todo, queue (that is the picked work, not the source).

**Viewport**:
The Console-internal geometry module owning scroll state for every scrolling
pane — offset, cursor, cursor-follow, and clamp-on-shrink behind a small
interface, with height 0 meaning unbounded. Pure geometry: it returns
visible-slice bounds and hidden-above/below counts, and the view renders the
affordance strings ("… N more below"), so restyling never touches it. One
implementation serves the backlog/queue columns, the drill-in pane, and the
rebuild-output pane. Decided 2026-07-18 (#1540).
_Avoid_: scroll region, pager, window (that names its output value, not the
module).

**Quickstart**:
The pre-CLI interactive scaffolder — a nix app (`nix run
…#quickstart`, `apps.quickstart`), not a Harness subcommand — that takes an
operator from zero to a validated, buildable Consumer flake in one command. It
runs *before* the `spindrift` binary exists (which is why it cannot be a
subcommand: `runtime`/`driver` are baked into the wrapper the binary is built
from, and the fields it sets live in `flake.nix`, not env). A TTY-only wizard
that detects what it can (container runtime by `podman → docker → bwrap`, host
git identity, repoSlug from `git remote`, an ambient token) and asks only the
irreducible rest, then writes a minimal generated `flake.nix` + secrets-only
`harness.env` + `.gitignore` + `.envrc` (no `prompts/` — the Harness defaults
every prompt), and finishes through `spindrift doctor` and `spindrift build`
(ADR 0027). Refuses to clobber an existing flake without `--force`.
_Avoid_: init, wizard (names the UI, not the role), bootstrap (the launcher's
internal launch-context wiring).

**Answers**:
The Quickstart's collected operator decisions — one value (runtime, driver,
repo slug, git identity, tracker settings, …) produced by the wizard's
prompt/detect phase and consumed by a pure scaffold render that returns the
generated files as values, with the injection-guarding nix escaping applied
on that single seam. The wizard keeps the writes, the clobber/backup policy,
and the doctor/build finish line. Decided 2026-07-18 (#1548).
_Avoid_: config (that names the generated files), wizard state, form.

**Pick**:
The operator's act of selecting an issue into the running session's queue for
dispatch. Picking an issue that is not yet `Dispatchable` promotes it through
the normal lifecycle transition first — the pick *is* the human launch button,
recorded durably on the Issue Tracker. A picked issue waits in the queue until
a parallelism slot frees; queued-but-unlaunched picks hold at `Dispatchable`,
never `InProgress`.
_Avoid_: select, schedule, enqueue.

**Unpick**:
Retracting a queued-but-not-yet-launched Pick. Purely a session-queue edit —
no Issue Tracker interaction; the issue simply remains `Dispatchable`.
_Avoid_: cancel (ambiguous with Terminate), dequeue, remove.

**Terminate**:
The operator-initiated ending of a live Dispatch, valid anywhere from claim to
verdict: any running Box is reaped, the settle is abandoned wherever it stands
(CI watch, fix pass, merge gate), and the issue returns to `Dispatchable` —
never `Failed` (nothing to triage; the human just decided) and never a new
lifecycle state. Terminate abandons watching, never un-lands work: pushed
branches and open PRs stay put, and the terminate comment on the issue links
them so a later re-dispatch can adopt rather than collide. The ending is
recorded outside the state machine: a terminal line in the Box log and that
comment. Distinct from Unpick, which retracts a Pick that never launched.
_Avoid_: kill, cancel, abort.

**Reconcile**:
The `local`-tracker bookkeeping sweep that makes a local issue's native
open/closed axis match Code Forge reality — the sole authority that closes a
local issue (ADR 0029). Observational: it never lands code. Per open issue it
closes the issue when its recorded landing PR is merged, discovers a PR by
agent branch when no landing was recorded (a box that died before its outcome
line), flags one whose PR was closed unmerged, and — only behind a composite
death signal (no PR/branch, a stale Box log, and an absent container when the
runtime is reachable) — resets an orphaned `InProgress` to `Dispatchable`,
supplying the liveness signal #600 required before any such reset. Against a
push-only `local` Code Forge it parses the recorded string into a
[[Landing]] instead: an `IntegrationRef` closes on a confirmed merge exactly
like a PR; a `BranchRef` still an ancestor of its own Integration branch is
repaired in place (landing upgraded, seam closed) rather than left stuck
open, and one that isn't prints a stuck verdict naming the branch (issue
#1809). Auto-invoked at the end of a `dispatch` run and available standalone
(`spindrift reconcile`) for the between-runs cases: a runner that died, or a
PR in approval limbo. Does not itself merge — landing stays with `dispatch`
and the explicit `recover` gesture. _Avoid_: sync, sweep, cleanup, recover
(the operator's explicit per-issue adopt-and-merge gesture, a different
act).

**Transcript**:
The Driver-rendered record of a Dispatch's work across its pass logs — the full
content the live-tail sidebar shows on toggle, with a raw-JSONL toggle for the
byte-exact form. It is a *view* of logs the Dispatch already produced, not a
separately stored stream: the condensed Activity feed is a second view of the
same logs, not a distinct timeline behind them.
_Avoid_: narrative log, running log, log.

**Activity feed**:
The Console's condensed, timestamped view of a running Dispatch's work — one
line per Driver step, derived by replaying the Dispatch's pass log through the
heartbeat parser. It is the live-tail sidebar's default, following the newest
line until the operator scrolls back; a key toggles it to the full [[Transcript]].
Like the Transcript, a view of logs the Dispatch already produced, not a
separately stored stream.
_Avoid_: heartbeat log, status log, activity log, event stream.

**Outcome line**:
The machine-readable final line a Box writes to stdout, parsed by the Launcher
to learn where the deliverable landed and whether the Dispatch is ready for
settle, blocked, or failed. Grammar:
`SPINDRIFT_OUTCOME issue=<num> landing=<ref> status=<status> note=<text>`
where `note` may contain spaces and `=`. `landing` is the landing reference —
a PR URL (`github` Code Forge), a branch ref (push-only `git`), or a
verdict-comment URL (research dispatch); `status` values are scoped to the
Dispatch kind (`ready`/`blocked` for work, the verdicts plus `blocked` for
research). The line carries only what the Launcher cannot know without the
Box — never backend identity or other run config, which the Launcher already
holds authoritatively. The `cmd/launcher/internal/outcome` package is the
authoritative spec and implementation: `Parse` validates the grammar, `Line`
produces the canonical form, and `LastInLog` scans a Box log while gracefully
skipping lines too large for the scanner buffer.
_Was_: `pr=<url>` — renamed once the field carried branch refs and comment
URLs; PR-vs-issue is a GitHub-ism that confuses on split backends.
_Avoid_: result line, output line, status line.

**Guardrail prompt**:
The harness-owned prompt carried in the system slot of every harness-issued
Driver invocation, stating the trust boundaries to the model: issue bodies and
comments are untrusted data, credentials are never disclosed, work stays on the
dispatched issue, and edits to the guarded paths force a human merge.
Non-negotiable by the Agent and by issue/comment authors; overridable by the
Consumer flake, which is trusted by construction. A soft control: it hardens
harness-issued invocations but cannot bind a fresh Driver process the Agent
spawns itself — that containment belongs to the Box and token scope.
_Avoid_: system prompt (ambiguous with the Driver's own), jailbreak prompt,
safety prompt.

**Conditional fragment**:
An opt-in prompt step rendered into an Agent prompt only when its gate is on:
one registry row — gate variable, fragment file, substitution variable — in a
harness-owned nix registry, consumed by a single entrypoint loop that also
derives the substitution allowlist from the same rows. Gates are normalized
to env-nonempty on launcher-delivered Box plumbing; computed gates (skills
discovery, filer, caveman) are precomputed into variables before the loop.
Replaces the six hand-unrolled "conditional residue" blocks in
`phase_prompt_assembly`. Decided 2026-07-13; lands with the prompt-registry
work. _Avoid_: prompt toggle, feature flag, optional section.

**Settle**:
Driving a Dispatch from Box-exit to its terminal lifecycle state, whatever
that takes: interpreting the Outcome line, watching CI, self-heal fix passes,
the merge or push-only landing under `MERGE_MODE` and the Merge guard, and
merged-verification. "Merge gate" informally names the green→merge segment
within a settle.
_Avoid_: gate (a checkpoint — narrower than the whole settle), finalize, report.

**Merge guard**:
The launcher-side check, outside the Box and beyond the Agent's influence,
applied between green CI and merge: if the change touches a guarded path, the
merge downgrades to manual — regardless of `MERGE_MODE` — and a note on the PR
says why. It bounds injection-induced *drift* (an agent following its normal
flow), not a fully adversarial Agent, which holds a token that can merge (see
Two-actor separation). Downgrade, never block: the cost of a hit is one human
read.
_Avoid_: path filter, merge block, review gate.

**Instruction surface**:
The repo-carried files a Driver reads as trusted instructions on every fresh
clone — `CLAUDE.md`, `AGENTS.md`, `.claude/`, `.opencode/` — plus the CI config
under `.github/`. The cross-run persistence vector: a poisoned instruction file
merged once feeds every future Agent as trusted input. Guarded by the Merge
guard's default path set.
_Avoid_: config files, dotfiles, prompt files.

**Two-actor separation**:
The opt-in hard mode where the Box token's user cannot update the base branch
(repository ruleset) and only a second, launcher-held token can merge. The only
configuration in which the Merge guard is literally uninfluenceable by the
Agent; the single-token default trades that away for operator simplicity.
_Avoid_: dual token, split credentials, bot pair.

**Tripwire**:
The launcher's detection that a PR was merged by an actor other than itself
while the issue was `InProgress` — evidence the Agent (or someone else) merged
around the gate. Detection only, no prevention; surfaces for human triage.
_Avoid_: audit, alert, monitor.
