# The driving loop is a shipped app above the invocation boundary

Issue #1633 asked for `spindrift dispatch --daemon`: a flag that makes dispatch
sleep on a drained queue instead of exiting, so work continues unattended. The
flag is the wrong home, and the reason is already written down twice.

[ADR 0019](0019-dispatch-exits-at-the-wave-boundary.md) makes the launcher
*invocation* the image-freshness boundary and deletes an in-process poll to get
there — `DEPS_POLL_SECS` and `DEPS_WAIT_SECS` went with it, because "waiting was
only meaningful while one invocation was expected to finish the whole graph."
Held issues stay on the dispatch label and are picked up by the next invocation,
"which re-evaluates the flake — freshness is automatic, never orchestrated."
A `--daemon` flag reintroduces exactly the wait that ADR removed.

[ADR 0043](0043-box-artifact-freshness-hot-swaps-the-launcher-restarts.md) then
named the cost of a long-lived launcher directly: "launcher-half staleness stops
being a latency problem and becomes a correctness one … a long-lived process
orchestrating fresh Boxes with stale code, with no incidental restart to rescue
it." Today that incidental restart *is* the drained-queue exit. The same ADR
records that `launcher build` is the only snapshot-reclaim point, so "a launcher
that hot-swaps for hours without restarting holds every generation it ever
swapped to" — a cost accepted precisely because the process was short-lived.
`--daemon` inverts that assumption and inherits the leak.

**Decision: the unattended driving loop is `apps.daemon`, a separate program
generated per-Consumer by `mkHarness` beside `apps.default`, which re-invokes
the launcher as a fresh process on every iteration.** Each wake is a new
invocation, so the freshness boundary, the incidental restart, and the
`build`-time generation reclaim all keep working unchanged.

The governing principle: **a stateless supervisor may re-invoke freely; a
stateful launcher must drain and exit.** The launcher holds in-flight Boxes and
cannot swap its own code beneath them. The daemon holds nothing, so re-invoking
costs it nothing.

Shape follows the two existing precedents. Like
[ADR 0027](0027-quickstart-is-a-pre-cli-nix-app.md)'s quickstart, it is a nix
app and a Go program under `cmd/launcher/` rather than a launcher subcommand —
quickstart sits *before* the CLI exists, the daemon sits *above* its lifetime,
and neither belongs inside it. Like `apps.default`, it is a thin
`writeShellApplication` that sources `harness.env` from `$PWD` and passes knobs
through an `--input` document ([ADR 0020](0020-knobs-flow-through-the-launcher-input-document.md)),
so its poll interval and awake window come from the same schema the launcher
reads instead of being hand-copied.

It is the only spindrift component that must invoke `nix` at runtime. It cannot
exec the launcher store path it was built against — that path is the stale one a
rebuild exists to replace — so it shells out to `nix run <attr>` with the attr
baked in and carries `nix` in `runtimeInputs`. That, independently, is why it
cannot be a launcher subcommand.



## The daemon owns concurrency; invocations are pinned to a rev

Continuous dispatch (#527) and the daemon are two implementations of one job.
`RunContinuous` maintains a pool of Boxes inside a single invocation, refilling
each freed slot — which is precisely what a driving loop does, running inside
the process whose exit ADR 0019 made the freshness boundary. The whole
multi-Box freshness apparatus (the probe's image dimension, ADR 0043's
hot-swap, `ErrImageStale`, exit 4, the snapshot generations that accumulate
because `build` is the only reclaim point) exists to keep that long-lived pool
fresh.

The shared state the in-process pool appears to need is already external. The
overlap gate builds its snapshot from `it.ListIssues(forge.InProgress)` plus
declared `## Touches` and open-PR changed files (`waves/touches.go`), so it is
tracker-derived and process-independent. `Claim` is documented as tolerating a
concurrent claimant by design: "a failed claim … means skip, never an error."
Independent launcher processes therefore coordinate correctly through the
tracker without sharing memory.

**Decision: the daemon supervises N concurrent single-Box invocations, and
continuous dispatch is deprecated in favour of it.** `MAX_PARALLEL` becomes the
daemon's pool size. One invocation, one Box, extending ADR 0019's "one
invocation, one wave" to its conclusion — freshness stops being orchestrated
because each Box is born from its own evaluation.

Continuous mode is **deprecated, not removed**. It remains for operators who do
not want a daemon, it is a documented CLI surface under ADR 0010's semver
contract, and the Console drives the same engine regardless.

**Children are pinned to a rev, not to the working tree.** The daemon fetches
without pulling and launches each child against an explicit revision (a
rev-pinned flake reference into the repo), never mutating the checkout.
Running children were never at risk from a rebuild — artifacts are
content-addressed, so a new rev yields a new image tag and a new closure. The
exposure is the child being *born*: a flake evaluation reads the tree over
seconds, and a checkout landing inside that window can fail confusingly or,
worse, evaluate a mixed tree and build an image from a state that never
existed as a commit. With a pool, something is being born often enough that
this stops being theoretical.

Pinning also buys two things unrelated to the race: every Box's rev is exactly
known, and the dirty-tree refusal disappears — git objects are read, not the
worktree, so an operator may keep working in the checkout while the daemon
runs.


## One pool, both kinds

The daemon drives work and research concurrently rather than one kind per run
as `DOGFOOD_KIND` does. Kind is already a verb — `spindrift dispatch` and
`spindrift research` are separate subcommands and `applyDispatchKind` swaps the
label family behind them — so the daemon spawns children of either kind rather
than being configured into one.

**One shared pool.** `MAX_PARALLEL` caps total concurrent Boxes across both
kinds. Two pools would make `doctor`'s RAM arithmetic
(`MEMORY_LIMIT × MAX_PARALLEL`) wrong by exactly the amount that took down the
VM in #712, and ADR 0022 is explicit that research "gets no lighter path" — it
runs through the full Box, so it costs what work costs.

**Backoff is per kind.** Each kind carries its own timer, so an empty work queue
backs work off while research keeps filling slots at full speed. The daemon
idles only when both kinds have backed off into an empty result. This is what
makes mixed mode cheap: the daemon never probes a queue it already knows is
empty, and no new query surface is needed to decide what to run.

**Research capacity is a floor, not a ceiling.** `RESEARCH_SLOTS` guarantees
research at least that many slots *while research has queued work*; work may
occupy at most `MAX_PARALLEL − RESEARCH_SLOTS` during that time. When either
kind has nothing queued the other bursts into the whole pool. The reservation
binds only while both kinds have work, which is the only moment a policy is
needed. The knob spans the spectrum rather than being a special case: `0` is
work-first with research taking leftovers, `MAX_PARALLEL` is research-first.

Research starving is the failure worth guarding against, because research is
the pipeline stage *ahead* of work: starve it and workers receive exactly the
thin issues ADR 0022 exists to prevent. A research slot also turns over faster,
since research settle is one-shot — no fix passes, no session resume — so the
throughput cost to work is smaller than the slot ratio suggests.

**Discovery skips the other family's in-flight work.** An issue may legitimately
wear a label from each family at once, so mixed mode could otherwise run a
researcher and a worker on the same issue simultaneously — the researcher
writing "context for a worker" while that worker is already running. Each
kind's discovery therefore excludes issues in progress in the other family.

ADR 0022's invariant is that label families "never interact at claim time," and
that survives: this is discovery, a different seam, and it is a scheduling
preference rather than a claim rule. A daemon-side check would not do, since it
would be blind to work started by a Console session, by CI, or by a human
running `dispatch` directly — the tracker is the only place that knows.

## The wait policy

The daemon holds `MAX_PARALLEL` slots and fills each with one single-Box
invocation pinned to the fetched tip. Because it knows how many siblings are
running, it can read a child's exit against context the launcher does not
have — the same code means different things to a busy pool and an idle one:

| child exit | siblings running | pool otherwise idle |
|------------|------------------|---------------------|
| 0 dispatched | refill the slot at once; reset backoff | refill at once; reset backoff |
| 2 queue empty | leave the slot idle, back off quietly | back off — the idle case the daemon exists for |
| 3 none dispatchable | routine: claimed or overlap-deferred against a sibling; back off quietly | the genuine jam; back off loudly |
| 1 unclassified | slot backs off and refills; the circuit breaker counts it | same |
| 5 host-tainted | halt the pool | halt the pool |
| 6 config invalid | halt the pool | halt the pool |

Exit 3 is the row the pool changes most. Sequentially it meant the queue was
jammed and a human was needed. With a pool it is the ordinary steady state —
a slot opening while every ready issue is claimed by, or overlaps with, a
sibling. Only an otherwise-idle pool makes it a jam, and only the daemon can
tell the difference.

Exit 4 is vestigial here. It reports that a long-lived invocation's image drifted
from the base tip, and a rev-pinned single-Box child is fresh by construction.

Halting means the same thing everywhere: stop filling slots, let running
children drain, exit. It never kills work already paid for.

A single child's failure must not stop the others — one malformed issue should
not end the night — but an unclassified failure repeating across the pool is a
systemic fault (an expired token, a forge outage) that no amount of retrying
will clear. So a failing child's slot backs off and refills, while a circuit
breaker counts failures across the pool and halts it once they exceed a
threshold in a window.

The backoff counts only "nothing changed", and resets on any successful
dispatch. Outside the awake window the daemon starts no new children and sleeps
to the next window start; children already running finish, because killing work
already paid for saves nothing.
## Shutdown belongs to the launcher; the daemon only forwards

The daemon must never kill the launcher. The launcher has no signal handling
today — the only `signal.Notify` in the tree restores terminal echo in
quickstart — and `registryproxy.go`'s teardown comment records that a SIGTERM or
SIGKILL arriving before `Proxy.Close`'s defer leaks the proxy socket. Killing it
mid-run also orphans containers and strands issues in `InProgress`, which only
`spindrift recover` clears.

dogfood.sh works around this by trapping the signal in the script and letting
the child finish. That is safe but it is not a spindown: under continuous
dispatch one invocation drains the *entire* queue, so "finish the current
invocation" can mean hours. A shipped daemon needs standard process semantics
instead.

**Decision: the launcher owns its own graceful shutdown, and the daemon
forwards signals rather than implementing shutdown on its behalf.**

- `SIGTERM` to the launcher sets the same stop-refilling flag the stale-image
  path already sets (`waves/continuous.go`): no new Boxes launch, in-flight ones
  finish, normal teardown runs, and it returns a distinct exit code meaning
  "signalled drain" so the daemon exits rather than treating it as failure. The
  bound is one Box's runtime, not the queue's.
- A second `SIGTERM`, or `SIGINT`, escalates to abort: `Runner.Reap` the
  in-flight Boxes and release their issues off `InProgress` — the work the
  Console's Terminate gesture (ADR 0024) already does for a single Dispatch —
  then exit promptly.
- `SIGKILL` is uncatchable and remains abrupt, as everywhere.

This is the same principle that put the daemon above the invocation boundary,
applied downward: the component holding the state owns the draining of it. The
launcher holds in-flight Boxes, so the launcher drains them. The daemon holds
nothing, so it has nothing to drain and no business reaching into what does.

It also removes a landmine that would otherwise need documenting forever: with
the daemon-only trap, `systemctl stop` would `SIGKILL` after its 90-second
default `TimeoutStopSec` while the daemon politely waited out a multi-hour
drain, killing the launcher in exactly the abrupt way this avoids.

## Considered Options

- **`dispatch --daemon`, as filed** — rejected above: it fights ADR 0019's
  invocation boundary and inherits ADR 0043's unreclaimed snapshot generations
  and launcher-half staleness. Recorded so nobody re-proposes the flag.
- **An external scheduler (systemd timer, launchd, `cron`)** — genuinely
  attractive, since a calendar spec expresses the awake window natively and
  spindrift grows no surface at all. Rejected because it leaves every Consumer
  to reinvent the exit-code state machine, needs a lockfile to prevent
  overlapping runs, and has nowhere to put the in-flight-across-the-boundary
  semantics. A Consumer may still run `apps.daemon` *under* a timer; the two
  compose.
- **A generic top-level app, like `apps.quickstart`** — rejected: the daemon
  re-invokes a specific Consumer's `apps.default`, so it must be told which attr
  to drive, and it would re-derive the schema's defaults in Go — the same drift
  dogfood.sh has today, in a new language.
- **Nix-generated bash** — the shortest path from dogfood.sh, and shellchecked
  at build time. Rejected because ADR 0005 scopes bash to config and the
  in-container entrypoint, and because the daemon's real content — wall-clock
  window arithmetic across midnight and DST, and a state machine over the
  documented exit codes — is decidable logic that wants table tests. The repo
  carries 429 Go test files and confines bats to the entrypoint.

- **A daemon supervising one `dispatch --continuous` child** — the obvious
  first shape, and what this ADR was originally drafted as: the launcher keeps
  its in-process pool, the daemon only decides whether to run again. Rejected
  because it leaves two implementations of the same pool in the tree and keeps
  the entire multi-Box freshness apparatus alive to serve one of them. It is
  also strictly more machinery for less: the refill ticker polls the tracker
  every three minutes for the whole time Boxes are running, where a process
  pool queries only when a slot actually frees.

## Consequences

The dogfood loop stops being a bespoke script and becomes `nix run .#daemon`,
which is what makes spindrift's own loop a first-class surface rather than a
prototype. `DOGFOOD_RUNTIME`'s `NIX_APP` switch dissolves — spindrift's flake
exposes two harnesses, so each gets its own daemon app — and `DOGFOOD_KIND`
becomes an ordinary knob. `dogfood.sh` is nevertheless *kept* until the daemon
has driven real runs green, and deleted in a follow-up: the daemon can only be
proven by dogfooding with it, and dogfooding is how spindrift is built, so
landing a new concurrency model with no fallback stacks two risks. That
follow-up must actually be filed, or the script rots in the tree.

The launcher grows signal handling it has never had, and a new exit code for a
signalled drain. That is scope inside the launcher, entered deliberately: the
component holding in-flight Boxes is the only one that can drain them.

Three things trapped in `package main` or in dogfood.sh must be lifted to be
shared rather than re-typed: `exitCodeFor`'s integers, which `docs/reference.md`
documents as a product contract; the podman-machine RAM preflight (#580, #712),
which today protects the dogfood loop and no Consumer at all, and which moves
into `doctor` where a Consumer would look for it; and `doctor`'s
advisory-versus-blocking tiering, since a daemon that shells out to `doctor` at
startup needs "runtime not ready" to be fatal where an interactive run does not.

`spindrift doctor` publishes a *different* exit-code table from `dispatch` —
exit 2 means "configuration invalid" there and "queue empty" here. A daemon
that runs both must never conflate them.

Deprecating continuous dispatch is an ADR 0010 semver event, not a deletion. It
stays for operators who do not want a daemon and remains the Console's engine,
so the wave engine keeps both entry points for the foreseeable future.

The circuit breaker introduces threshold knobs (failures, window) whose defaults
are guesses until a real unattended run produces evidence. So does the backoff
(floor, cap). These should be revisited against measurement rather than tuned by
argument.

Unattended operation accumulates work only a human clears: an orphaned
`InProgress` issue after a halt, and the `Recoverable` state, which `Reconcile`
deliberately skips and only `spindrift recover` lands. The daemon makes that
backlog grow unattended where dogfood.sh had an operator watching. The daemon
surfaces the count and does not clear it — `recover` *lands* work, and
auto-landing unattended is a policy step `MERGE_MODE` governs deliberately.

A second app enters the Consumer CLI surface and its semver contract
([ADR 0010](0010-consumer-cli-surface-and-semver.md)).
