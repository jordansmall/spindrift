# Published surfaces carry a schema integer and a producer-written fixture

Status: proposed

## Context

spindrift is becoming a platform (ADR 0073), and the people building on it
are not only Connector authors. An organization adopting it wants its own
tools beside the Daemon: a chat bot announcing settled Dispatches, a cost
report, an internal portal. Those tools read what spindrift writes; they
cannot import `cmd/launcher/internal/*`, and should not have to.

VERSIONING.md versions CLI verbs, flake options, `mkHarness`, env names and
label names. None of the data spindrift writes is on that list, so every
reader is reading an internal surface:

- **The status file and the Events file** are the one exception in
  practice. ADR 0060 gave the status file a top-level `schema` and each
  event line a `v`, bumped only on a rename, removal or changed meaning,
  and its amendment (issue #6634) pins both with a fixture the Daemon's own
  writers produce. Even so, VERSIONING.md does not list them, and the
  Dashboard checks `schema` but never reads `v`.
- **`spindrift stats --json`** prints one Dispatch Record per line with no
  version, though ADR 0061 already names it as the Dashboard's way in to
  Records.
- **`spindrift daemon status`** prints a wrapper object around the status
  file whose own fields carry no version.
- **The Dispatch Record store** has a real schema version, `PRAGMA
  user_version`, but it guards migrations: fourteen forward-only steps, one
  of which renamed a table. It was never meant to be read by anyone else.
- **Everything else** a tool might scrape — Pass log names and lines, the
  `spindrift_op` stamps inside them, Child log text, the Ledger's
  `state.json`, issue comment markers — has no version and no fixture.

The question is which of these to promise, and how, before an outside
reader depends on one by accident.

## Decision

1. **A named set of Published surfaces.** A Published surface is data
   spindrift writes for a reader outside its own process tree, under a
   version marker and a fixture. The set is closed and listed in
   VERSIONING.md; anything not on it is internal, however readable.

   | Surface | Shape | Marker |
   |---|---|---|
   | Status file | one JSON object | top-level `schema` (exists) |
   | Events file and the Daemon's stdout stream | JSON Lines, one event each | `v` on every line (exists) |
   | `spindrift daemon status` | one JSON object | top-level `schema` (new) |
   | `spindrift stats --json`, with or without `--by` | JSON Lines, one Record each | `v` on every line (new) |

   A log's location is published through these surfaces, never through a
   naming scheme: `child_start.child_log` and `box.pass_log` in the Events
   file, `passes[].log` in a Record. Log contents stay raw and internal.

2. **One fact, one published shape.** The Dispatch Record is published
   through `stats --json` only. The SQLite store keeps `user_version` as
   its own migration guard and stays free to migrate, rename and split
   tables; a tool that wants Records runs `stats --json`. The same holds
   for the `spindrift_op` lines Records are ingested from.

3. **A plain integer per surface, and one bump rule.** Each marker is an
   integer, independent of the release version and of every other
   surface's. It bumps only on a rename, a removal, a type change or a
   changed meaning of an existing field or event name. Adding a field or
   an event name never bumps it.

4. **Readers tolerate additions.** A reader must ignore fields and event
   names it does not know. A reader meeting a marker newer than any it was
   built for must not interpret the data: it says so and, where it can,
   shows the raw data, as the Dashboard already does for `schema`. The
   producer writes only the current version; there is no dual-write
   window.

5. **A producer-written fixture per surface.** ADR 0060's amendment
   becomes the rule for all of them. The producer's own test drives its
   real writer to emit one of every shape it can produce, into a committed
   fixture; a golden test fails when the writer and the fixture disagree.
   The fixtures move from `dashboard/testdata/contract/` to a top-level
   `contracts/`, one directory per surface, so they belong to no single
   reader and sit at every release tag for outside readers to test
   against. The Dashboard's contract test reads them from there.

6. **A bump is a breaking change.** It takes the `!` marker, a minor
   release while pre-1.0, and a MIGRATING.md entry naming the old and new
   shapes. A fixture diff in review is the signal that a Published surface
   changed; a diff without a bump must be additive.

7. **Internal, named so nobody guesses.** VERSIONING.md lists these as
   internal even though an outside tool can read them: the Dispatch Record
   store's schema; Pass log and Child log names and contents, including
   `spindrift_op` lines; the child-to-Daemon report pipe (ADR 0053); the
   Outcome line and its sibling markers, which are the Box's protocol with
   the host; the Launcher input document (ADR 0020), whose keys are the
   env names already versioned; the Ledger; the lock file, the outbox and
   the in-place restart handoff (ADR 0063); and the body and markers of
   any issue comment, whose versioned form is the label it accompanies.
   Canonical Driver events are not data spindrift publishes; they belong
   to the Connector protocol and its handshake (ADR 0073).

## Considered Options

- **Publish the Dispatch Record store's schema.** A SQLite file is easy to
  query from any language. Rejected: every one of its migrations would
  become a breaking change, a reader holding the database open contends
  with the WAL writer, and the store is a cache of the Pass logs that
  `stats --json` already projects.
- **Semver per surface, as the Connector protocol uses.** Rejected: a
  Connector negotiates its version in a handshake, but a file reader
  cannot answer back, and since additions never break a tolerant reader a
  minor number would carry nothing a reader can act on.
- **One version for all published data, or the release version itself.**
  Rejected: a break in one surface would mark every reader of every other
  surface stale, and the release version moves on every patch.
- **JSON Schema documents beside the fixtures.** Deferred: a hand-written
  schema is a second description of each Go struct that can drift from
  it, while a fixture the real writer produces cannot. A schema generated
  from the structs could join later without changing this decision.
- **A Go client module for the surfaces.** Rejected for the reason ADRs
  0067 and 0073 rejected Go composition: it would make internal types
  public API, and it would serve Go readers only. Files serve any
  language.
- **Dual-write across a bump** (old and new shapes for one release).
  Rejected for now: no surface has bumped yet, and a reader that checks
  its marker already fails safe. Revisit at the first bump if an outside
  reader asks for a window.
- **Move the Dashboard to its own repository now** as the proof that the
  surfaces suffice. Rejected for now, for ADR 0060's reasons and ADR
  0073's: the shared `contracts/` directory gives an outside reader the
  same footing, and the Dashboard stays the in-repo reference reader.

## Consequences

- Two additive changes land: `daemon status` gains `schema: 1` and each
  `stats --json` line gains `v: 1`. Neither is a bump.
- The Dashboard starts honouring `v`, treating an unknown one as it treats
  an unknown `schema`.
- Today's field names become the contract as they stand, including the
  status file's camelCase `checks[].nextCheck` beside snake_case
  everywhere else. Normalizing it later is a bump.
- `docs/reference.md` documents each surface's fields. The Events file
  table is already pinned to the emit sites by `event-stream-parity`; the
  other three gain tables, and the fixtures keep them honest.
- Recovering an outcome-less run from the `spindrift_op` lines in a Pass
  log stays an operator's tool, not a contract.
- Pass log naming, the Record store and the report pipe stay free to
  change, so a tool built on them breaks without notice, as an override
  prompt directory does today.
