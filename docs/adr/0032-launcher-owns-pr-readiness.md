# The launcher owns PR readiness; the Driver never flips its own PR ready

Issue #1614 first made a PR's draft bit the readiness signal: the Driver
opened its PR as a draft and flipped it ready itself, in the same breath as
printing `SPINDRIFT_OUTCOME ... status=ready`; a no-outcome Box exit fell
back to trusting an open non-draft PR as proof the Driver had reached that
point, so a mangled or missing outcome line (the #1582 dogfood incident) no
longer stranded a finished PR. That was progress over trusting the outcome
line alone, but it kept the flip itself inside the same prompt-obeying text
it was meant to backstop: the Driver — an untrusted, throwaway Box process —
still held sole authority over the one bit that told every downstream reader
"CI will confirm this is done." A skipped, misordered, or hallucinated flip
inside the Driver's own turn left forge state no more trustworthy than the
text line it replaced.

**Decision: invert ownership. The Driver never flips its own PR ready — it
opens the PR as a draft and leaves it there, whether it ends its turn with
`status=ready` or `status=blocked`. The launcher (the trusted, host-side Go
binary) becomes the only writer of the draft→ready transition, firing it
exactly once, at the moment CI confirms green, immediately before it applies
`MERGE_MODE` (`cmd/launcher/internal/settle/ready.go`'s `MarkReady` call in
the `gateGreen` case).** Symmetrically, draft-ness stops meaning anything
about Driver intent: a same-run Box exit with no parseable outcome line no
longer adopts an open non-draft PR as inferred proof of a lost `status=ready`
line (issue #1654) — both the entrypoint backstop and the launcher's `Settle`
now synthesize `status=blocked` unconditionally on a no-outcome exit,
regardless of the PR's draft state. The only surviving adoption path is the
operator's explicit `agent-recover` label (`SettleAdopted` /
`recoverByNumber`), which was never gated on the same-run inference to begin
with and still requires a non-draft PR — now trustworthy precisely because
only the launcher's own `MarkReady` at green ever produces one (issue #1651).
Because an adopted PR's head was not necessarily pushed by this launcher
process, its first CI poll additionally requires evidence this run's own
checks registered before it trusts an immediately-green rollup (issue
#1652), closing a race the old draft-based trust never had to consider.

This supersedes the draft-until-ready description issue #1614 added to
`docs/reference.md`'s runtime-flow and label-lifecycle prose; that prose has
since been updated in place (issues #1651–#1654) to describe the inverted
flow this ADR records the rationale for.

## Considered Options

- **Harden the outcome-line parsing instead** (stricter grammar, more
  tolerant extraction of a wrapped or malformed `status=ready` line).
  Rejected: it treats the symptom, not the structural problem — the Driver
  would still be the sole authority over its own "done" signal, and any
  future prompt drift or model deviation could reproduce the same failure
  class the #1582 incident exposed.
- **Keep the Driver-owned flip, but have the launcher double-check it
  independently before trusting adoption.** Rejected: a second reader
  re-deriving the same untrusted write does not make the write trustworthy;
  it only adds a second place that can disagree with it.
- **Drop the draft/ready distinction entirely and gate everything on the
  outcome line plus a fresh CI poll.** Rejected: draft-ness is a cheap,
  forge-native signal that lets a human glance at the PR list and see which
  ones the launcher itself has vouched for as green — discarding it loses
  that for no gain, since the launcher already needs to poll CI regardless.

## Consequences

- `MarkReady` is idempotent and called unconditionally at `gateGreen`
  (`ready.go`), so it never blocks the merge path on its own failure — a
  failure only reaches the console log, never a public issue comment,
  matching `EnqueueAutoMerge`'s existing best-effort precedent.
- A same-run no-outcome exit always reports `status=blocked` and demotes the
  issue to the failed label; the operator recovers explicitly via
  `agent-recover` rather than the launcher guessing from draft-ness. This
  removed a fail-open path: the old backstop would happily adopt (and merge)
  a WIP or explicitly-blocked PR that merely happened to be non-draft for
  unrelated reasons.
- `recoverByNumber` still requires a non-draft PR, but that requirement now
  means something it didn't before: the only way a PR becomes non-draft is
  this launcher's own `MarkReady` at confirmed green, so a non-draft PR is
  now *more* likely to be recoverable, not less.
- `docs/reference.md`'s runtime-flow diagram, label lifecycle, and killed-
  launcher recovery caveat describe the current (post-inversion) mechanics
  directly; this ADR is the pointer for readers who want the "why" behind
  that flow rather than the "what."
