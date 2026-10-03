# Coverage-severity calibration (issue #2696)

## Before (baseline, recorded in the issue)

Across 115 verdicts in a 24h window, prior to this change and prior to
#2550/#2551 (disposition file + reviewer memory, delta review):

- Blocking findings per verdict did not decay across review rounds: 4.2,
  3.3, 3.9, 3.6 at rounds 1 through 4.
- Non-blocking findings rose from 8.9 to 11.1 per verdict over the same
  rounds.
- 17 of 22 BLOCK-ending runs hit the round cap rather than converging on
  agreement.
- 96 BLOCK verdicts were issued against 19 APPROVE.
- 17% of all blocking findings were test-coverage complaints.

The 96-vs-19 figure is an aggregate across all rounds. The issue's own
source data does not break out a first-round-only approval rate. A query
surface does exist for this: `.github/workflows/agent-dispatch.yml` uploads
every run's `logs/` as an `agent-logs-<issue>` artifact. As of 2026-08-17,
`gh run list --workflow=agent-dispatch.yml --status success` returns 83
successful runs total across this repo's whole history, spread across
weeks — not clustered into a dense 24h window the way the original 115-verdict
baseline was. Reconstructing a first-round approval rate from that scattered
history would mean downloading and parsing each run's artifact individually,
and the result still wouldn't be *comparable* to the original window's
density. The after-measurement below should capture the first-round rate
directly, from a purpose-run comparable window, instead.

This measurement predates #2550 and #2551, so it does not isolate how much
of the non-convergence they alone resolve; this ticket lands after both so
this change's own marginal effect is measurable in isolation, per the
issue's own sequencing note. Whether #2550/#2551 alone already restored
convergence (the issue's AC5 "close unbuilt" condition) is also not
determinable from this diff — it requires the same after-window
measurement below, read before this change's own effect is layered on top.

## What changed

`review-prompt.md`'s CORRECTNESS dimension, Severity bullets, and
`fragments/code-review-default.md`'s skill-deferral coverage clause add one
narrow exemption to the existing "untested new logic blocks on its own"
rule: missing or inadequate tests for a pure relocation, refactor, or
comment/doc change whose behaviour is already covered under test move from
`## Blocking` to `## Non-blocking` — still surfaced, never dropped, just no
longer gating the merge. Nothing else changes — an uncovered refactor still
blocks exactly as before, and every other blocking category and the
adversarial default-BLOCK stance are unchanged.

## After — not measured

The after arm is abandoned, not pending: issue #3840 closed the open
loop by recording why it is not worth running, rather than running it.
Both of "Before"'s forward references, the "after-measurement below"
and the AC5 "same after-window measurement below", point to this
section; the method it would have used is kept below as planned.

### Why

- **No window isolates this change.** The method assumes a comparable
  window in which this change's exemption is the only rubric variable.
  Every window since it merged on 2026-08-17 also carries these changes
  to the same review surface:
  - `b27ed6eb` (2026-08-24): the review prompt defaults to caveman
    (terse, compressed output).
  - `b83edc12` (2026-08-28): comment-to-code disproportion becomes
    blockable. A new blocking category inflates blocking findings per
    round and dilutes the test-coverage share.
  - `0846145f`, `3498bb43`, `4a329f78`, `f63b52e0` (2026-09-04): the
    review hunt is reshaped depth-first, with every dimension always
    rendered.
  - `87716fc1` (2026-09-12): the review fan-out moves onto the
    `review-axis` agent. [Issue #3611's
    record](3611-round-2-escalation-flip.md) has filed review findings
    per merged agent PR rising from 0.54 to 1.51 across it.
  - `dbca42a0` and `1a8c26a0` (2026-09-23): the round-2 escalation
    flip narrows to diff growth, and `drop` becomes a third triage
    outcome.
  - `0d29c6f1` (2026-09-28) and `b6e64772` (2026-09-30): review-loop
    fixes for an advisory reviewer steering the loop and for
    re-escalating an already-filed finding.
- **The baseline was never isolated either.** As "Before" says, it
  predates #2550/#2551, and no window separated their effect from this
  change's, so the issue's AC5 "close unbuilt" question stays
  unanswered too.
- **The per-round findings are not kept in queryable form.** Blocking
  findings per round, their test-coverage share, and which defect
  categories they caught all come from finding text. The orchestrator's
  per-run findings log (`orchestrator-findings-log-*.md`,
  `cmd/launcher/orchestrator/run.go`) collects it, but that log is a temp
  file inside the Box, gone when the Box exits. The text does survive in
  the Driver's raw stream, which a CI run uploads as the
  `agent-logs-<issue>` artifact (daemon and local runs leave it in the
  host's `.spindrift/logs`), so recovering it means fetching each run's
  log while it is retained and re-parsing its stream the way
  `scanReviewLog` does.

The one metric only this change targets is the test-coverage share of
blocking findings, 17% at baseline.

### Method as planned (never run)

Kept verbatim as written before this change merged on 2026-08-17.

This diff cannot produce the after side of the measurement: it requires
observing real review rounds over a comparable run set after this change is
live, the same way the baseline above was gathered, and that run set does
not exist until this merges and runs for a comparable window. Once it has,
re-run the same verdict/finding query this baseline used, over a comparable
24h (or otherwise comparable) window of dispatched runs on the target repo,
and record:

- first-round approval rate,
- blocking findings per round (rounds 1-4, or however many the loop caps
  at),
- the fraction of blocking findings that were test-coverage complaints,
- whether genuine blocking-defect detection (spec/correctness/security/
  standards) dropped relative to the baseline above. This diff only
  reclassifies non-behavioural coverage gaps and touches no other blocking
  category, so a drop would indicate an implementation defect in the rubric
  wording rather than an intended effect — but that is an expectation to
  verify against real data, not something this doc can confirm on its own.

Per the issue's acceptance criteria, if #2550 and #2551 alone are found to
already restore convergence, this change's own marginal contribution may
turn out to be small — that is itself a valid outcome to record here, not a
failure of the change.
