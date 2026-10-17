# An out-of-tree Driver crosses a process seam, once a trigger fires

Status: proposed

## Context

The question behind the October 2026 architecture review was whether
spindrift's adapters, Drivers first, could live outside the tree, so that
adding one does not add to everyone's test burden and a Driver spindrift
would not bundle can still exist.

After ADR 0065 and ADR 0066 a Driver's host half is a Strategy literal: a
decoder, three small functions and data, with a non-test import closure of
five small packages. Its in-box half is a Nix row of data plus a column in
the Bash hook table. The core already compiles with no Driver (issue #5179),
and ADR 0066's second stage makes a Driver edit rerun only that Driver's
derivations. What a separate repository would add is an independent release
cadence and a contributor boundary. What it would cost is a versioned
extension contract where today there is none: VERSIONING.md covers only the
Consumer surface, `cmd/launcher/internal/*` is explicitly unversioned, and
the reference guide's Driver-author section is in-tree documentation.

The Nix registry is closed (ADR 0009): mkHarness looks a Driver name up in a
fixed attrset, and the Go registry refuses an unknown name. Opening the Nix
side alone would let a Consumer define a row only if it reused a bundled
host half, which buys variants of a bundled Driver and nothing else. Package
pinning is already an overlay, and the knobs a Consumer varies are already
per-Driver options (ADR 0064).

## Decision

The shape is decided now; the split is not built until a trigger fires.

1. **The shape is a process seam.** An out-of-tree Driver is a Nix-built
   executable: Pass-log lines in, canonical events (ADR 0065) as JSON lines
   out, plus subcommands for exit resolution, the result event and session
   flags. The four binaries exec it. The in-box half stays a Nix row. The
   contract is the canonical event schema and the subcommand argv, which
   VERSIONING.md covers from the day the first out-of-tree Driver exists.
2. **Nix-time Go composition is rejected.** A public driverkit Go module
   with out-of-tree Strategy packages composed by mkHarness into the four
   binaries would need every Go binary's fixed-output vendor hash to stay
   valid for every Consumer's composition, which holds only if no
   out-of-tree Driver ever brings a new external dependency. That is a rule
   spindrift could state but not enforce on another repository.
3. **The Nix registry stays closed** until one of the triggers below fires.
   ADR 0066's credential rules are plain data so that, when it opens, a
   Consumer row's facts travel in the launcher input document (ADR 0020)
   with the generated Descriptor as the no-document fallback.
4. **Two triggers**, either of which reopens this decision for building:
   a Driver someone wants that spindrift will not bundle; or `pi`'s dogfood
   (issue #5487) showing that in-tree scoping still leaves a third Driver's
   cost on every contributor. A variant that overlays and the per-Driver
   options cannot express opens the Nix registry alone, without the Go
   seam.
5. **All or none.** When the split is built, every bundled Driver crosses
   the same seam, so the in-tree path is not a privileged second contract.
   spindrift's own tests keep a fake Driver behind the seam, which ADR
   0066's seamtest Strategy already is.

## Considered Options

- **Build the process seam after #5487.** Commits the contract before any
  out-of-tree Driver asks for it. Rejected: a versioned contract with no
  second party is a cost with no reader.
- **Go composition.** See (2). The vendor-hash constraint is the deciding
  reason; the public-module surface it would require is the second.
- **Reject the split outright.** In-tree scoping is most of the benefit, so
  this was close. Rejected because the question will be asked again at the
  next Driver, and the answer is better recorded than re-derived.
- **Open the Nix registry now, variants only.** Rejected for lack of a
  variant today that overlays and the per-Driver options cannot express.

## Consequences

- ADR 0009's closed registry stands, with the triggers above as its
  conditions for change.
- No contract is versioned until a trigger fires. The reference guide's
  Driver-author section remains in-tree documentation, which ADR 0066's
  Strategy makes shorter.
- The heartbeat would gain a subprocess per Pass under the seam. That cost
  is accepted in principle and measured when the seam is built.
- Canonical events becoming a test-only wire format under ADR 0066 is the
  only step taken toward the seam now.
