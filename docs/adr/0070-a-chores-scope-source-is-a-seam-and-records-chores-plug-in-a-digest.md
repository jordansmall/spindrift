# A Chore's scope source is a seam, and records Chores plug in a digest

Status: proposed

## Context

ADR 0062 added the tuning Chore as the first Chore scoped by Dispatch
Records rather than the tree. The catalog row says so with
`scopeSource = "records"`, but in the butler "records" means "tuning".

- Five `c.Records` branches in `butler.go` choose the due check, the
  next-due time, the input (file slice or digest) and a settle step. Every
  records branch calls the tuning package's own code by name: its renderer,
  its prior-findings query and its snapshot type.
- Outside the butler, about 100 `Tuning*` identifiers sit in 14 files.
- Doctor, `settle/ready.go` and `issue_intent.go` branch on tuning's finding
  label to enforce merge-by-hand.
- Four `BUTLER_TUNING_*` knobs are hand-written in the shared schema.

Adding a tree-scoped Chore is a catalog row and a prompt. A second
history-scanning Chore would mean rewriting each of those branches. That
Chore is planned, so the hook is built now rather than on a trigger.

## Decision

1. **The scope source is a seam with two adapters.** `tree` covers the
   commit cursor, the head-keyed due check and the tracked-file slice.
   `records` covers the Record ID cursor and the Records store. The butler
   calls `Due`, `NextDue` and `Prepare(claim)`, which returns the scope, the
   input and a settle hook. Between the due check and the claim it calls
   `Stage`, so a failure there (the `tree` file listing) leaves the Ledger
   unclaimed. It no longer branches on the scope.
2. **A records Chore plugs in a Records digest.** Each built-in records
   Chore registers a Digest Strategy in the Host bundle under its own name
   (see the amendment below). The Strategy has a `Render` function, which
   takes the Records, the prior cursor, the claim time, the parsed prior
   findings and a merge lookup, and returns the digest text and the latest
   Record ID it covers. `Anchors`, also required, lists what a finding may
   cite in a digest it rendered. Its other functions are optional, and a
   nil one means the Chore does without it:
   - `ParsePrior`, which reads a prior finding;
   - `Validate`, which applies the Chore's own settle rules, such as tuning's
     sample floor and its `evidence-gap` exemption;
   - `DedupTerms`, which supplies the Chore's dedup pattern terms.

   The `records` adapter owns everything else:
   - the check that a Chore is due once enough new settled Records exist;
   - the prior findings, as finding issues whose dedup marker carries the
     Chore's `chore=<name>` term (see the amendment below);
   - the host-private snapshot keyed by Record ID, failing closed when it
     cannot be stored;
   - dropping any finding with no cites or one citing an anchor absent
     from the snapshot (ADR 0062 decision 4);
   - the dedup policy, from the Digest's `DedupTerms` and the Chore's
     cooldown.
3. **Settings live on the catalog row.** A records row declares its minimum
   number of new Records, and its own settings are row-owned knobs (ADR
   0068). `BUTLER_TUNING_*` move onto the tuning row with their names
   unchanged.
4. **Merge-by-hand is a row field.** `mergeByHand = true` on tuning replaces
   the label branches. Doctor requires each enabled Chore's `findingLabel`
   from its row.
5. **Records Chores are built in.** A Digest is Go, so a Consumer-declared
   Chore stays tree-scoped. A catalog row with `scopeSource = "records"` and
   no registered Digest fails at startup.
6. **Tests.** Each records Chore has a Digest corpus of Record fixtures and
   a golden of the digest they render, replayed by a small kit. The
   `records` adapter is tested once against a synthetic Digest.

## Considered Options

- **Two adapters, with the `records` adapter being tuning itself.** This
  was the first recommendation: a general digest hook with one user is a
  hypothetical seam. Rejected once a second history Chore was known to be
  planned.
- **Leave the branches.** They sit in one file, which is decent locality,
  but each records branch names tuning, and the settle and doctor branches
  sit outside the butler.

## Consequences

- ADR 0062 stands. Its digest becomes the first Records digest, and its
  host-side checks split between the `records` adapter (cites in the
  snapshot) and tuning's `Validate` (sample floor, `evidence-gap`).
- A second records Chore is a catalog row, a prompt, a Digest Strategy and
  its corpus.
- This ships incrementally with no release gate. The knob names do not
  change.

## Amendment (issue #5696): the Digest registers in a Host bundle

Decision 2 first said the Digest Strategy registers in the Bundle. The
Box and `driver-exec` import that Bundle for their Drivers, so a Digest
there would link settle, tuning and the Records store into both. Built-in
Digests register instead from a separate Host bundle
(`internal/hostbundle`), which only the binaries that run the butler sweep
import. The bundle guard pins that list the way it pins the Bundle's.

## Amendment (issue #5697): prior findings match the Chore's marker

Decision 2 first said the adapter finds prior findings as issues wearing
the Chore's `findingLabel`. It matches the `chore=<name>` term in a
finding issue's dedup marker instead. Every finding of a Chore with a
`findingLabel` carries that term, and the marker also works on trackers
with no label registry (`local`, `jira`), where a label query cannot.
The not-planned cooldown is keyed on the same term, so it applies to
the settling Chore's own findings rather than to tuning by name.

The cite check needs the Digest's anchor syntax, which only the Chore
knows, so the Strategy gains a required `Anchors` function. The adapter
then drops an uncited or unknown-anchor finding for every records Chore,
whether or not its Digest has a `Validate`.
