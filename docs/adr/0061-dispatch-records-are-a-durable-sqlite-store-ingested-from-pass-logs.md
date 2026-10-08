# Dispatch Records are a durable SQLite store ingested from Pass logs

Status: proposed

## Context

Improving spindrift from its own history (where the cost goes, which
passes earn their keep, whether a prompt or knob change helped) needs
per-Dispatch facts that can be joined and grouped. The facts exist, but
nothing joins them:

- **Pass logs are complete.** Every pass's `result` event carries the
  Driver's notional cost, per-model tokens, duration and turns; every
  `spindrift_op` line is there; review verdict text and fix dispositions
  appear in the transcript; earlier attempts survive as `.prior-run.N`.
- **Nothing identifies a Dispatch.** Log paths are reused per Dispatch key,
  host-side fix and conflict-resolve passes are separate files, and a
  research Dispatch and the work Dispatch after it share one chain.
- **The host's decision is not in the Pass log.** ADR 0039's settled state
  reaches the Events file (Daemon only, and only since 2026-10-07), Child
  log status lines, and tracker labels, which keep only the latest state
  and hide failures a relabel retried.
- **The pass manifest is not history.** It is reset every Box and written
  only on some forge modes; on the dogfood Daemon it held 3 of one
  Dispatch's 6 passes.
- **Nothing says what ran.** The harness revision moves about 85 times a
  day on the dogfood repo, the model per role is not recorded, and no
  prompt or knob identity exists to compare cohorts by.

Measured over the two dogfood roots on 2026-10-07: 4.2 GB of Pass logs
(1.2 GB on disk), 4,847 files, about 3,600 passes and $2,700 notional
since 2026-07-11, growing about 2.2 GB a month on the Daemon root alone. A
full scan of the lines that matter takes about 10 s, nearly all I/O.

## Decision

Add **Dispatch Records**: one durable row per Dispatch in a per-root SQLite
database, `.spindrift/dispatch-records.db`, ingested from Pass logs and
read by a new `spindrift stats` subcommand.

1. **A Record ID names a Dispatch.** It is
   `<kind>:<Dispatch key>@<claim time, UTC>`, e.g.
   `work:4762@2026-10-07T20:13:23.104Z`: readable, greppable, and
   deterministic, so re-ingesting the same logs yields the same IDs. The
   per-key claim lock is what makes it unique.
2. **The host stamps the Dispatch's boundaries into its Pass logs.** A
   `dispatch_start` op heads every Pass log the host creates for the
   Dispatch (each attempt, each host fix pass, conflict-resolve), all
   carrying the same Record ID, since a retry rotates the previous
   attempt's log aside and any surviving file must still name its
   Dispatch. It carries the Record ID, kind, Dispatch key, harness
   revision, the model for each role, the Driver and its CLI version, and
   the value of every env-schema knob not marked `secret`. A
   `dispatch_settled` op, appended to the primary Pass log after the last
   Box, carries the settled state, a reason class from settle's existing
   status vocabulary, the note and the PR URL. Every Dispatch, Daemon or
   manual, then has a host-written start and end in its own logs. The Events file's
   `box` and `settled` events gain `record_id` (an added field, so `v` stays
   1).
3. **The Box reports what prompt ran as a template hash.** A
   `prompt_hashes` op names, per role, the SHA-256 of that role's prompt
   assembled with every per-Dispatch variable (issue number, title and
   text, Chore section, findings, CI detail) replaced by a fixed
   placeholder. Two Dispatches with the same harness setup hash equal
   whatever the issue, so cohorts survive unrelated commits.
4. **The outcome is the host's, only.** It comes from `dispatch_settled`,
   cross-checked against the Events `settled` event. The Box's
   `SPINDRIFT_OUTCOME` self-report is kept as a separate `box_status`
   field and never stands in for the outcome.
5. **History is backfilled by inference.** Logs without a stamp are
   grouped into Dispatches by their `.prior-run.N` order and first
   timestamps, their kind classified by filename, research-only statuses,
   and first pass role, with `kind: unknown` for the rest. Inferred Records
   are tagged `attribution: inferred`, carry `outcome: unknown`, and still
   count toward cost and pass totals.
6. **The store is durable and the logs become prunable.** The host ingests
   its own Dispatch at settle; `stats` ingests anything not yet ingested.
   Both upsert by Record ID. Once a Dispatch is settled and ingested its
   logs may be deleted. A row outlives its logs, so the schema moves only
   by ordered forward migrations (SQLite `user_version`). The store keeps
   per-pass usage (notional USD, four token classes, API calls, turns,
   durations), per-agent tokens, verdicts, the knob snapshot, template
   hashes, and the review verdict text and fix dispositions, so a later
   reader can judge a block after its log is gone.
7. **`spindrift stats` reads it.** A summary line and one row per role by
   default (cost labelled API-equivalent), `--json` for one Record per
   line, a repeatable `--root` defaulting to the current checkout,
   `--since`, `--kind`, `--include-inferred`, `--by
   role|revision|model|prompt:<role>|knob:<NAME>`, and `--reingest`.

The driver is `modernc.org/sqlite`, the pure-Go one, since every build
runs with `CGO_ENABLED=0`. The Dashboard stays dependency-free (ADR 0060)
and reads `stats --json` if it shows stats at all.

Host-written fields (the stamps, the outcome) are trusted. Usage, template
hashes, verdict text and dispositions are Box-reported, as cost is today,
and verdict text and dispositions are untrusted prose written by a Box that
read the issue thread.

## Considered Options

- **No store; parse the logs on every query, with a disposable per-file
  cache.** No dependency and trivially rebuildable, and first chosen;
  rejected once retention came up: the logs would have to be kept forever
  or compacted in place, and a compactor is its own file-maintenance tool
  keyed to every op type.
- **A materialised JSONL index written at settle.** Readable by `jq`
  without the launcher, rejected: a second copy that drifts, and no answer
  to retention either.
- **OpenTelemetry tracing.** Standard trace context and viewers, rejected
  as the foundation: it starts at switch-on with no backfill, needs a
  collector and backend every Consumer must run, opens a Box egress
  channel the read-only bar has to reason about, and suits per-request
  latency rather than month-long group-bys. The Record ID maps to a trace
  ID and passes to spans, so an exporter can be added later.
- **A layered outcome** (settled event, then Child log, then tracker, then
  self-report). Rejected: Child logs cover only recent Daemon runs, the
  tracker only the latest Dispatch per key, and three extra parsers buy an
  outcome for history that the cost and verdict analysis does not need.
- **An opaque ULID or UUID Record ID.** Rejected: inferred Records would
  need a name-based hash of the readable key anyway, and the readable form
  greps.
- **Hash the prompt as sent.** Rejected: it embeds the issue text and is
  unique per Dispatch. **Hash the template files** instead: rejected, it
  misses gate- and probe-driven conditional sections.
- **The Box stamps everything.** Rejected: knobs and models would become
  Box-reported, and not every knob reaches the Box. **The host computes the
  hashes** too: rejected, it would re-run assembly without the Box's probe
  results and could hash a prompt that never ran.

## Consequences

- `dispatch_start`, `dispatch_settled`, `prompt_hashes`, the Events
  `record_id` field, and the database schema become documented surfaces in
  `docs/reference.md`.
- The launcher takes a sizeable pure-Go dependency, a slower cold compile,
  and a `launcherVendorHash` bump.
- A parser bug found after its logs are pruned cannot be corrected for that
  history; `--reingest` repairs only what remains on disk.
- Log pruning (ADR 0060's known gap) now has a rule but no tool: ingest
  before you delete.
- Deferred, each a later decision: the butler retro Chore and experiment
  tracking that read the store (which need a host-injected snapshot, the
  verdict text fenced as untrusted, and a post-merge quality signal), an
  OpenTelemetry exporter, a pruning tool, and a cross-root store.
