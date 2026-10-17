# Drivers decode Pass logs into canonical events

Status: accepted

## Context

ADR 0009 split each Driver into an in-box half and a host-side Go strategy,
and had each Driver "normalize its tool's misbehavior at its own boundary."
On the host side that boundary is the whole `Driver` interface. Each Driver
walks its own CLI's raw NDJSON in five separate readers:

- transient classification;
- the heartbeat writer, including model reporting;
- usage extraction, which feeds `pass_usage` and Dispatch Records;
- Transcript rendering for the Console;
- the final result text that carries `SPINDRIFT_OUTCOME`.

driverkit shares only low-level substrate: line framing, the log-scan degrade
helper, transient patterns, role constants and text trimming. It does not
share an event vocabulary. The cost shows:

- **Size.** The `claude` strategy is about 1,750 lines of source and the
  `opencode` one about 500, almost all of it parsing.
- **Drift.** The two disagree where they need not: `opencode` attributes no
  usage to roles, and each reports models differently.
- **Multiplication.** A third Driver (`pi`, issue #4945) would be a third full
  set of five readers.
- **Hidden coupling.** Dispatch Record ingestion (ADR 0061) reads the `claude`
  strategy's event types directly, so Records are implicitly `claude`-shaped
  and would not ingest another Driver's Pass logs correctly.

## Decision

Each Driver **decodes** its Pass log into spindrift-owned, Driver-neutral
**canonical events**, and the five readers are written once, in driverkit,
over those events (issue #4944).

1. **A canonical event vocabulary in driverkit.** The kinds the readers need:
   start, assistant text, tool call, tool result, usage (role, model, effort
   when known, input/output/cache-read/cache-write tokens, cost when the
   Driver reports one), model observed, effort observed, error (keeping the
   raw text for pattern matching), and final result text. The exact field
   set is settled against both existing grammars in the first slice.
2. **A Driver's parsing is one decoder.** One raw line in, zero or more
   canonical events out, with whatever role attribution the grammar allows:
   `claude` from the parent tool-use id, `opencode` the default top-level
   role. A decoder may carry minimal state where a grammar needs it, such as
   tool-call id to role.
3. **Shared readers.** Classification, heartbeat, usage, Transcript and
   result text move into driverkit over canonical events. The per-Driver
   inputs that remain are extra transient and terminal patterns, exit-code
   resolution, the synthetic result-event encoder, and session flags.
4. **The `Driver` interface is unchanged for callers.** The launcher,
   orchestrator, Box and Records keep calling the same methods. A Driver
   delegates them to the shared readers once it is switched, and no caller
   learns about canonical events directly.
5. **The raw Pass log stays the only file.** Decoding happens when the log
   is read; no second, canonical log is written.
6. **Records ingest through the Driver.** Dispatch Record ingestion stops
   importing the `claude` strategy's types and decodes each Pass log through
   the Driver named in its `dispatch_start` stamp. spindrift's own
   host-written `spindrift_op` lines stay readable on every path.

**Migration without breakage.** Current Consumers must see no change while
this lands:

- A **differential corpus** of real Pass logs, scrubbed of credentials and
  private content, covers each existing Driver: normal runs, fix passes,
  crashes, rate limits and outcome-less runs.
- While both paths exist, a test runs the old and new readers on every
  corpus log and requires identical output for all five.
- A Driver switches to the new path only when its differential is empty,
  and its old readers are deleted in a later slice.
- Order: `opencode` first, the smaller grammar, which proves the event
  model. The `pi` Driver is then built directly on the layer. `claude`, the
  largest and the default, goes last.

The old-versus-new differential was never kept as a standing test. The
committed per-Driver corpus goldens (each Driver package's `TestCorpus`, which
replays its corpus through `conformance.Run` and checks each Driver through
the interface alone; issue #5506) replaced it and stay as the
permanent gate. (Amended by issue #5187: the planned standing differential and
the later-slice deletion of old readers did not happen as written; see the
#5187 amendment below.)

ADR 0009's Driver boundary stands, but on the host side the per-Driver
boundary is now the decoder, not the five readers.

## Considered Options

- **A canonical log written at capture time.** `driver-exec` would tee a
  versioned canonical NDJSON beside the raw log, and every host reader,
  Records and the Dashboard would read only that. Rejected for now: two logs
  per pass, a format to version, and a fallback path for every log written
  before the switch, all to save a decode that is cheap at read time.
- **Build `pi` first, extract the layer afterwards.** Three concrete
  grammars before generalising. Rejected: it writes a third set of five
  readers only to delete most of it, and two grammars already show the
  shape.
- **Port `claude` before adding `pi`.** Rejected: it puts the riskiest step
  in front of the new Driver for no gain. The layer is proven on `opencode`,
  and `claude` follows independently.
- **Rely on the existing per-Driver tests.** Rejected as the only gate: they
  are hand-written fixtures, and the failure modes here (usage drifting,
  a missed outcome line, transient and terminal swapping) show up only on
  real logs. They stay as a second line of defence.
- **A runtime switch between old and new paths.** Rejected: a knob every
  Consumer can see, for a migration the corpus proves offline.

## Consequences

- A new Driver is one decoder plus a handful of quirks (patterns, exit
  resolution, result encoding, session flags), not five readers.
- Every Driver gets the same heartbeat, Transcript and usage behaviour,
  including role attribution wherever its grammar carries roles.
- Dispatch Records become Driver-neutral: a Pass log from any registered
  Driver ingests correctly.
- A scrubbed corpus of real Pass logs becomes committed test data, guarded
  by a check that rejects credential-shaped strings.
- Behaviour bugs found during the port are filed separately, not folded
  into a switch, so each differential stays exact.
- While a Driver is mid-migration the interface carries two implementations.
  The window closes when its old readers are deleted. It has closed for
  every Driver (issue #5187).

## Amendment (issue #5182): opencode's old readers are deleted in the switch

The migration plan above asks for an old-versus-new differential test while
both paths exist, and for the old readers to go in a later slice. For
`opencode`, issue #5182 deleted them in the switch commit instead. The
committed corpus goldens, unchanged by the switch, stand in as the
differential: the new path must reproduce them byte for byte.

Issue #5183 then removed the one-line wrappers that switch left in the
`opencode` package: its Driver registration calls the driverkit readers
directly, and the package keeps its decoder, transient and terminal
patterns, exit resolution, result-event encoder and session flags.

## Amendment (issue #5184): claude's old readers are deleted in the switch

`claude` follows `opencode` (issue #5182). Issue #5184 deleted its old
classification, usage, result-text and transcript readers in the switch
commits rather than in a later slice, and the unchanged corpus goldens stand
in as the differential. Its heartbeat still runs its own parse (no longer true;
see the #5187 amendment); it moves onto the shared writer in its own ticket.

Two divergences from the old `claude` readers are deliberate. Every
`spindrift_op` line is a host op the classifier ignores; the old classifier
skipped only `dispatch_start` and `dispatch_settled`, so text in any other
host op could match a marker. And the per-role cache-write total now comes
from the split `cache_creation` object when the CLI writes one, rather than
from the flat `cache_creation_input_tokens`; the two differ only if the CLI's
own fields disagree.

## Amendment (issue #5187): the migration window closes

The heartbeat note in the previous amendment is out of date: issue #5185
moved `claude`'s heartbeat onto the shared writer, and issue #5187 removed
the reader wrappers and the stream-json event types that were left over. The
`claude` package now holds what the `opencode` one does: its decoder, its
transient and terminal patterns, its result-event encoder and its session
flags. It also keeps three quirks the shared readers take as inputs: its
classify options, its heartbeat tracking, and the ordering of its per-model
usage. It has no exit resolution, since its CLI's exit code is already
trustworthy.

Dispatch Record ingestion reads Pass logs only through the stamped Driver
(issue #5186). The corpus goldens remain the permanent gate. Status moves to
accepted.

## Amendment (issue #5501): per-model ordering moves to the shared reader

The per-model usage reader now orders rows itself, by the Driver's
model-family hook (the one the heartbeat takes) and then by exact id, and
the transcript renderer ends every non-empty transcript with a newline.
The `claude` package therefore keeps two of the three quirks named above:
its classify options and its heartbeat tracking, whose family hook now
also carries the rank the usage order reads.

## Amendment (ADR 0066): registrations are Strategy literals

ADR 0066 supersedes the worked-example sentence in the Decision: a Driver's
registration no longer calls the shared readers directly from ten methods.
It is a `driverkit.Strategy` literal, and one shared implementation calls
the readers from it. The #5501 amendment above is that decision's first
slice; the default top-level role and the choice of result reader stay
Strategy fields. The corpus goldens remain the permanent gate, replayed by
the conformance kit from each Driver's own package (issue #5506).
