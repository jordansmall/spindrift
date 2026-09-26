# Coverage-severity calibration (issue #2696)

## Before (baseline, recorded in the issue)

Across 115 verdicts in a 24h window, before this change and before
#2550/#2551 (disposition file + reviewer memory, delta review):

- Blocking findings per verdict did not decay across review rounds: 4.2,
  3.3, 3.9, 3.6 at rounds 1 through 4.
- Non-blocking findings rose from 8.9 to 11.1 per verdict over the same
  rounds.
- 17 of 22 BLOCK-ending runs hit the round cap rather than converging on
  agreement.
- Reviewers issued 96 BLOCK verdicts against 19 APPROVE.
- 17% of all blocking findings were test-coverage complaints.

The 96-vs-19 figure is an aggregate across all rounds. The issue's own
source data does not break out a first-round-only approval rate. A data
source for this does exist: `.github/workflows/agent-dispatch.yml` uploads
every run's `logs/` as an `agent-logs-<issue>` artifact. As of 2026-08-17,
`gh run list --workflow=agent-dispatch.yml --status success` returns 83
successful runs total across this repo's whole history. They are spread
across weeks, not clustered into a dense 24h window the way the original
115-verdict baseline was. Reconstructing a first-round approval rate from
that scattered history would mean downloading and parsing each run's
artifact individually, and the result still wouldn't be *comparable* to
the original window's density. The after-measurement below should capture
the first-round rate directly, from a purpose-run comparable window,
instead.

This measurement predates #2550 and #2551, so it does not isolate how much
of the non-convergence they alone resolve. This ticket lands after both so
that this change's own marginal effect is measurable in isolation, per the
issue's own sequencing note. This diff also cannot determine whether
#2550/#2551 alone already restored convergence (the issue's AC5 "close
unbuilt" condition). That needs the same after-window measurement below,
read before this change's own effect is layered on top.

## What changed

`review-prompt.md`'s CORRECTNESS dimension, Severity bullets, and
`fragments/code-review-default.md`'s skill-deferral coverage clause add one
narrow exemption to the existing "untested new logic blocks on its own"
rule. Missing or inadequate tests for a pure relocation, refactor, or
comment/doc change whose behaviour is already covered under test move from
`## Blocking` to `## Non-blocking`. Reviewers still report them and never
drop them, but they no longer gate the merge. Nothing else changes. An
uncovered refactor still blocks exactly as before, and every other blocking
category and the adversarial default-BLOCK stance are unchanged.

## After — method only, no data yet

This diff cannot produce the after side of the measurement. That requires
observing real review rounds over a comparable run set after this change is
live, gathered the same way as the baseline above, and that run set does
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
  wording rather than an intended effect. That is an expectation to verify
  against real data, not something this doc can confirm on its own.

Per the issue's acceptance criteria, if #2550 and #2551 alone turn out to
already restore convergence, this change's own marginal contribution may
be small. That is itself a valid outcome to record here, not a failure of
the change.
