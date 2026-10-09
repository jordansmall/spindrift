# Drivers decode Pass logs into canonical events

Status: proposed

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
  The window closes when its old readers are deleted.
