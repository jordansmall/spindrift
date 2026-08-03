# The run's terminal outcome is host-decided from evidence, not reconstructed per-site

ADR 0035 moved the box's review loop into the `cmd/launcher/orchestrator` Go
binary, and ADR 0036 moved the host/box hand-off's branching decisions into
`driver-exec` verbs, each upholding ADR 0007's tier boundary one level deeper.
Both landed the loop and the hand-off in Go. Neither named what those two
changes did to a third thing they both touch: the answer to *"is this run
done, and with what outcome?"*.

Since `ORCHESTRATOR_ENABLED`, that answer is reconstructed independently at
four sites, over two shared artifacts — a temp log the loop truncates every
pass, and a single process exit code — with no site owning the fact:

- `orchestrator.scanPassLog` renders a pass log and calls
  `outcome.ParseAnywhere` — **no nonce gate**, token-anywhere, a boolean loop
  gate.
- `entrypoint.sh`'s `_driver_extract_outcome "$stream_log"` re-extracts the
  **last pass only** (`driver-exec` truncates `--log-path` each pass) and
  re-emits a bare line.
- `outcomebackstop.Run` holds the commit count and forge but discards them to
  emit an **always-`status=blocked`** synthetic line, and only when
  `[ "$claude_rc" -eq 0 ]`.
- The launcher's `outcome.LastInLog` scans raw, **nonce-gated**, last-wins,
  and is the authority.

When these disagree, a run whose work is committed and whose bundle is already
in the outbox settles wrong. Two windows are load-bearing:

- **Window 3 (false `blocked`).** `runWithReviewPass` stops at *"land pass
  reached no terminal outcome after APPROVE"*; the last pass's truncated log
  carries no outcome; the synthetic backstop stamps `blocked`; and under
  `CODE_FORGE=local` the entrypoint `exit 0`s **before** `bundle-out` ever
  runs. Work committed, no bundle relayed, settled `blocked`.
- **Window 2 (Failed, outbox ignored).** A bundle written pre-agent by
  `publish_rebased_branch`, then the box dies with no parseable outcome. The
  launcher's settle keys on `OutcomeFound`, not the exit code, so it parks the
  issue `Failed` via `settleUnresolved` and **never inspects the outbox**. The
  `tryAdoptRelayedBranch` self-report rescue (#2224) covers only a read-only
  PR-shaped `github` forge (`s.pr != nil`), never `CODE_FORGE=local`
  push-only.

The result is more failed and mis-settled runs since orchestrator mode: work
that is genuinely done gets `blocked`, or dies `Failed` with the deliverable
stranded in the outbox, and the fix is a manual restart.

## Decision

The run's terminal outcome has **one owner: the launcher (host authority)**.
The in-box side's contract shrinks to *producing honest evidence*, and the
host *decides the disposition* from that evidence. The Box advises; the
launcher decides — the same trust posture `outcome.SelfReport` already
documents (advisory, unauthenticated) and #2223–#2225 already spend host-side.

Concretely:

- **In-box: always produce evidence, on every exit path.** The entrypoint
  hand-off is reordered so the nonce-bearing `SPINDRIFT_OUTCOME` line (already
  unconditional) and the `bundle-out` step (when `base..branch` carries
  commits) both run **before** the hand-off exits — ahead of both the
  synthetic-backstop `exit 0` and the `exit "$claude_rc"` short-circuit — and
  the box then exits with the driver's **real** code. The exit code stays
  load-bearing: a non-zero exit remains the host's transient/OOM/#2075 signal,
  so forcing `exit 0` is rejected. This removes the last rc-gated *branch* from
  the hand-off, completing ADR 0036's rule (the entrypoint becomes linear exec
  glue there too). Research still short-circuits — it cuts no branch (ADR
  0022), so it emits no bundle.

- **No outcome-line grammar change.** The host re-derives the evidence it
  already holds rather than reading new line fields: outbox bundle presence
  (and `bundle-out` writes only when `commits > 0`, so a bundle *is* the
  commit-count signal), `LastSelfReportInLog` (advisory), the possibly-synthetic
  outcome line, and the exit classification. Widening the single-source
  `outcome` grammar to duplicate host-observable state is rejected — the
  outcome line stays a small interface.

- **Host consume-gate.** A genuine nonce-gated `ready`/`blocked`, and the
  transient path, settle exactly as today (#2075 included). When the line is
  synthetic-`blocked` **or** absent (`!OutcomeFound`) but a bundle is present
  and the driver's non-synthetic `SelfReport` reports success:
  - a **PR-shaped forge** auto-adopts a **reviewable draft PR** on the relayed
    branch — extending #2224's automatic arm to also fire on the
    `!OutcomeFound` path, not only synthetic-`blocked`;
  - a **`CODE_FORGE=local` push-only** run transitions to a new first-class
    **`Recoverable`** state, **not** `Failed`.

  With no bundle, or no success self-report, the run parks `Failed`: partial
  work never auto-lands.

- **`Recoverable` is a first-class lifecycle state.** Reconcile **skips** it
  (never resets it toward `Dispatchable`, so it is never silently
  re-dispatched); `spindrift recover` (`SettleRelayedBranch`, #2225) is what
  lands it; `spindrift doctor` and the Console surface a count. The operator's
  action becomes *recover*, not *restart*. A local push-only run is **not**
  auto-fast-forwarded on an unauthenticated `SelfReport`: unlike a github
  draft PR, a local fast-forward has no review catch, and a genuine `ready`
  landing is nonce-authenticated where this signal is not — so the review-gate
  asymmetry is resolved by surfacing local for `recover` rather than
  auto-landing it.

## Considered Options

- **An in-box finalizer that decides `ready` authoritatively and emits it,
  host trusting the line as today.** Rejected: it lets the Box self-promote
  its work to `ready`, shifting trust into the container against the
  Merge-guard / two-actor posture, and `SelfReport` is advisory by explicit
  design. The authority stays host-side; the Box only advises.
- **Explicit evidence fields on the outcome line
  (`commits=`, `bundle=`).** Rejected: it widens the single-source-of-truth
  grammar and its parity checks to carry facts the host can already observe by
  statting the outbox and scanning the self-report. Keep the interface small.
- **Force `exit 0` once evidence exists, so the run always settles.**
  Rejected: it destroys the only signal that the driver did not finish — a
  429/transient mid-work would read as done, breaking transient resume, the
  #2075 hold path, and OOM-137 retry.
- **Auto-adopt `CODE_FORGE=local` push-only uniformly, like github.**
  Rejected: local landing fast-forwards the Integration branch with no draft-PR
  review catch, and would do so on an unauthenticated `SelfReport`. Local
  surfaces to `recover` instead.
- **Reuse `Failed` plus a "recoverable" note rather than a new state.**
  Rejected: `Failed` reads as *restart* to an operator, defeating the point,
  and Reconcile's death-signal reset of an orphaned `InProgress` local run (no
  PR, stale log, no container) could re-dispatch it — the exact restart being
  designed out. A first-class state Reconcile skips is the clean seam.

## Consequences

- `entrypoint.sh`'s hand-off loses its last exit-code branch: `bundle-out`
  runs unconditionally (when commits exist) before the box exits. ADR 0036's
  "the entrypoint is linear exec glue" now holds with no residue. The
  `entrypoint-outcome-backstop`/`entrypoint-bundle-out` bats cases that pin
  "no bundle on a non-zero exit" are deliberately updated — that behavior is
  what changes.
- A new `Recoverable` state joins the `local` issue lifecycle (ADR 0029): a
  Reconcile skip arm, a `recover` landing arm (built on #2225's
  `SettleRelayedBranch`), and a doctor/Console surface. ADR 0017's
  "lifecycle stays call-site transitions" extends to it — the transition is
  minted at the settle call site, not inferred.
- #2224's auto-adopt arm broadens from synthetic-`blocked`-only to include the
  `!OutcomeFound` path, so a PR-shaped forge recovers a stranded success
  without an operator gesture.
- The composed local-loop test (`localloop_test.go`), which today hardcodes a
  success `Result`, gains the missing case: `OutcomeFound: false` with a bundle
  in the outbox drives settle to `Recoverable`, not `Failed`, and `recover`
  lands it — closing the coverage gap that let this class of bug ship.
- `outcomebackstop.Run` is unchanged: its synthetic `blocked` stays the honest
  "the driver did not self-report" default. What changes is that the host now
  promotes it from evidence instead of taking it at face value.

This amends ADR 0036 and builds on ADR 0035; it references ADR 0017, 0029, and
0033 for the lifecycle and local-loop seams it extends, and issues #2075 and
#2223–#2225 for the self-report / adopt-relayed machinery it generalizes. The
implementation is sliced into vertical tracer-bullet issues: the in-box
reorder, the github auto-adopt-on-`!OutcomeFound` arm, and the local
`Recoverable` state and its settle/Reconcile/recover/doctor wiring.
