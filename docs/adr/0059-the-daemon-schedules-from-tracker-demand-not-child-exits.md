# The Daemon schedules from tracker Demand, not child exits

Status: accepted

## Context

[ADR 0051](0051-the-driving-loop-is-a-shipped-app-above-the-invocation-boundary.md)
made the Daemon one pool over every [Dispatch kind](../../CONTEXT.md) and
chose to learn about queued work only from a child's exit code: "Backoff is
per kind … the daemon never probes a queue it already knows is empty, and no
new query surface is needed to decide what to run." A check is therefore a
whole child — `nix run <rev> -- <verb>`: evaluation, bootstrap, a full issue
list walk, one DepsOf call per listed issue — and an empty answer (exit 2)
backs that kind off from `DAEMON_IDLE_FLOOR` (5m) doubling to
`DAEMON_IDLE_CAP` (30m).

Running it unattended exposed three gaps in that choice:

- **"Already knows is empty" goes stale.** Nothing resets a queue-empty
  kind's backoff except that kind's own successful child; a moved tip
  deliberately resets only jammed kinds. An issue labelled for research
  while research sits at the cap waits up to 30 minutes with slots free.
- **Idle slots sleep blind.** A slot that finds every kind gated makes one
  `Clock.Sleep` to the earliest deadline. A sibling whose child finds work
  resets that kind, but slots already asleep don't notice, so one slot
  drains a fresh backlog while the rest sleep.
- **Losers poison the gate.** With a kind runnable, every free slot starts a
  child; with one queued issue, one claims it and the rest exit 3, gating
  the kind for at least `DAEMON_IDLE_FLOOR`.

The cost argument behind "no new query surface" has also moved. An empty
check today spends at least three forge calls plus a nix evaluation, every
5–30 minutes per kind. A dedicated probe is far cheaper, and on GitHub
nearly free: an authorized conditional request answered `304 Not Modified`
does not count against the primary rate limit. Verified 2026-10-05 against
each tracker the launcher supports:

| Tracker | Cheapest probe | Change token |
|---------|----------------|--------------|
| github | `issues?labels=…&sort=updated&per_page=100` | ETag → 304 |
| forgejo | `issues?labels=…&limit=1`, read `x-total-count` | none — Codeberg sends no ETag or Last-Modified on issue lists |
| local | `LocalTracker.ListIssues` over `LOCAL_ISSUES_DIR` | none needed — a directory scan, no network |
| jira | JQL search, `maxResults=0`, read `total` | none |

## Decision

### Demand is a third outside-world question

The Daemon's `Runner` gains `Demand(ctx, kind)` beside `ResolveTip` and
`RunChild`. **Demand** is a kind's cheap answer to "is there work": a count of
labelled candidates (`Ready`) for a tracker kind, or the instant the next
[Chore](../../CONTEXT.md) is due for the butler. The Daemon process answers it
itself, building the tracker adapters from its own environment — it already
holds every tracker credential, since a child's environment is the Daemon's
minus its knobs. A child would only add a process start to every probe.

Demand is advisory. The child still discovers and claims, so ADR 0022's
claim rules and ADR 0051's cross-family discovery exclusion are untouched,
and a wrong count costs at most one spawn or one probe interval, never a
wrong claim. `Ready` counts labelled candidates, not dispatchable ones:
blockers and overlap are still the child's to find, and still surface as
exit 3.

Which source a kind's Demand comes from is a descriptor row,
`DemandSource: TrackerProbe | ChildReported`; the Daemon never branches on a
kind name to pick an adapter. The same pass replaces `gateButlerKind`'s
by-name check with a descriptor enablement row.

### One adapter per tracker; ETag is the GitHub adapter's detail

- **github** probes `sort=updated&per_page=100` with `If-None-Match`. Sorting
  by update matters: labelling an old issue bumps its `updated_at`, while the
  default created-order first page would not change, so a 304 would hide it.
  When Demand said `Ready > 0` but the child exits 2, the next probe drops
  `If-None-Match` and reads fresh.
- **forgejo** reads `x-total-count` from a `limit=1` page. It keeps
  `definedLabels`' pre-check, cached across probes: Forgejo silently drops an
  unresolved `labels=` filter (#3952), so an undefined label would otherwise
  count every open issue. Amended by issue #4594: a missing label's verdict
  is cached for `undefinedLabelTTL`, so it is not re-walked every probe.
- **local** runs `ListIssues` in-process, resolving `LOCAL_ISSUES_DIR` (a
  git-ignored path in the operator's checkout, not the fetched revision)
  against the same directory children use.
- **jira** reads `total` from a `maxResults=0` search.
- **butler** is `ChildReported`. A new pure `chore.NextDue`, beside
  `chore.Check` and fed the same inputs, maps each not-due reason to when it
  lifts: `IntervalNotElapsed` → last Done + `Every`; `LiveClaim` → claim
  staleness; a spent budget → next midnight in the policy zone;
  `NothingToScan` → on the next tip move. The butler child reports that as
  `next_due` on its not-due record, and the adapter answers from the last
  record. The Daemon reads no Ledger itself; one spawn after start seeds the
  answer, and a rival run's Ledger change stays invisible until `next_due`,
  which costs a late butler, never a double sweep — the Ledger's
  compare-and-swap still decides.

### Probes pace at a flat, per-tracker interval

Each adapter declares a default interval from its cost — local ~20s, github
~60s, forgejo ~3m, jira ~5m — and one global `DAEMON_PROBE_INTERVAL`
overrides them all. The interval is flat: exponential growth existed to
ration expensive child spawns, and a 304 or a one-item page is not one.
`DAEMON_IDLE_FLOOR`/`DAEMON_IDLE_CAP` keep their names and defaults but now
govern only the jam gate.

Slots probe on demand rather than a prober ticking on its own: a slot about
to choose reads each kind's last Demand and probes inline when it is older
than the kind's interval, coalesced so concurrent askers share one call per
kind (the pattern `ResolveTip`'s shared fetch already uses). With every slot
busy nobody asks, and a shut Awake window parks slots before they choose, so
the Daemon still queries only when a slot can act — the property ADR 0051
rejected the continuous refill ticker for lacking.

### A start budget bounds discovery per kind

A kind may have at most `Ready − starting` children in discovery at once,
where `starting` counts slots that chose the kind but whose child has not yet
reported its claim. A claim releases the budget and makes the kind re-probe
rather than trust the old count. One queued issue now starts one child, not
one per free slot. The discovery baton (#3684) still serializes discovery
among those children, since concurrent children race for the oldest
candidate whatever the budget.

### Jams lift on rising Demand, too

A jam (exit 3) still backs off on the idle floor/cap and still lifts on a
moved tip. It records `readyAtJam`, and a later probe that sees `Ready` above
it also lifts the jam (`demand_rose`): someone labelled more work. A falling
count never lifts it.

### Parked slots wake when a kind becomes startable

A parked slot's deadline is the earliest of its kinds' next probe, butler
`next_due`, and jam end. It parks on a context derived from a pool-held wake
generation; opening a gate cancels the generation and installs a fresh one,
so the `Clock` seam is unchanged. The trigger is computed, not listed: a state
change wakes parked slots only if the set of kinds startable *now* grew or a
start budget rose. A phase change can never grow that set, so wakes cannot
ping-pong between slots.

### Scheduling decisions live in one module

The decisions above live in a `schedule` module: a plain value the pool holds
under its lock, with

```go
Decide(now time.Time, occ Occupancy) Decision   // Start{Kind} | Probe{Kinds} | Park{Until}
Observe(now time.Time, ev Event) (Schedule, bool /* woke */)
```

`Occupancy` (running and starting per kind) is passed in from the pool's slot
phases on every call rather than tracked twice. Tiers, the reservation floor,
Demand age, the start budget, the jam gate, and `next_due` sit behind
`Decide`; goroutines, `Runner` calls, the baton, breaker, halt, the Awake
window, and events stay in the pool. Its tests are tables of events to
decisions, with no goroutines or parked clocks.

### Rate limits pause a tracker, never trip the breaker

A probe answered with `forge.ErrRateLimit` pauses both probes and child
starts for every kind on that tracker until the reported reset (or four
probe intervals when none is reported), and never counts toward the circuit
breaker. A child started into an empty bucket would only fail bootstrap and
turn a polite wait into a breaker strike. Every other probe error backs off
and counts, as a `ResolveTip` failure does.

### Chore-keyed kinds skip the baton

Kinds keyed `ByChore` take no discovery baton: two butler children racing for
one Chore are already settled by the Ledger's compare-and-swap (`LostRace`).
Work and research keep sharing one baton, since the cross-family exclusion
reads the other family's in-progress labels during discovery.

### Observability

The status snapshot carries each kind's Demand (`ready`, `probed_at`,
`next_probe`), `jam_until` and `ready_at_jam`, the butler's `next_due`, and a
tracker's `rate_limited_until`. Events record transitions only —
`demand_appeared`, `demand_drained`, `demand_rose`, `probe_rate_limited`,
`probe_resumed`, `probe_failed` — never each probe.

## Considered alternatives

- **Answer Demand in a thin child verb** (`nix run <rev> -- demand <kind>`),
  keeping tracker code at the fetched tip. Rejected: it holds nothing but
  "build a tracker and count", and costs a process start and evaluation per
  probe for no API saving. The Daemon's own revision skew is bounded by the
  existing self-build check, and a wrong count is harmless by construction.
- **Exponential probe backoff.** Rejected: it reintroduces stale-empty in a
  smaller form, to save calls that were free (github) or one-item (forgejo).
- **Exact matching** — Demand returns issue numbers and the Daemon hands each
  child one. Rejected: it moves claiming into the Daemon, against ADR 0022 and
  0051's discovery-in-the-child rule.
- **A prober goroutine per kind.** Rejected: it needs its own busy and window
  gates, duplicating what slots know, and is the refill ticker ADR 0051 turned
  down.
- **The Daemon reads the Ledger for the butler's due time.** Rejected: it
  duplicates Ledger access the butler child already performs every run.
- **One baton per tracker**, so work and research on different trackers
  discover in parallel. Deferred until a mixed-tracker Consumer shows
  contention.
- **Extract `schedule` as its own refactor first.** Rejected as a horizontal
  slice: the module is born in the first vertical slice and grows one input
  per slice after.

## Consequences

ADR 0051's "Backoff is per kind" paragraph and the exit-2 row of its wait
policy are superseded: an empty queue is now Demand's answer, not a backoff
step, and exit 2 after positive Demand is a stale count. Its other rows — exit
3's jam semantics, halts, the breaker, the Awake window — stand.

The Daemon now makes forge calls of its own. Locally it draws from the same
`GH_TOKEN` bucket its children use, which is why a rate limit pauses child
starts rather than only probes.

Two knobs change meaning: `DAEMON_PROBE_INTERVAL` is new, and
`DAEMON_IDLE_FLOOR`/`DAEMON_IDLE_CAP` narrow to the jam gate. Their schema
docs are rewritten in the slice that makes the change.
