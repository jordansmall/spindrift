# Round-2 escalation flip recalibration (issue #3611)

## Before (baseline, recorded in the issue)

Commit `87716fc1` moved the `/code-review` two-axis fan-out from the
unrostered `general-purpose` agent onto the rostered `review-axis`
entry. The sharper axes took review-findings per merged PR from
0.47 over 2026-09-01 to 09-06 up to 1.47 over 2026-09-19 to 09-20,
a 3.1x rise, at a reproduction rate of 1.71. Seven runs working a
review-finding issue spawned twelve more, so the backlog grew even
when dispatch worked nothing else. This change's own before arm is
the **1.47-per-PR rate over 2026-09-19 to 09-20**, the window right
after `87716fc1` landed and right before this recalibration. 0.47 is
the *pre*-`87716fc1` rate, a different arm entirely and not this
change's baseline (see the re-check below).

The findings themselves stayed genuine across that window: `fix`-tier
filings rose to 45% of the total, up from a 35% baseline. That is why
the flip was narrowed rather than switched off. The target is
duplicate and low-worth filings, never real ones, and a narrowing
that suppressed genuine findings would fail the issue's own
requirement to keep them.

Both the 0.47 and the 1.47 figures above are tracker-scraped, not
drawn from the per-run `filed=ok:N,failed:N,skipped:N` tally (issue
#3608, issue #3609). That tally landed in this same campaign, after
both measured windows, so no run in either window emitted one.

## What changed

`review-loop-orchestrator.md` and `review-loop-inline.md`'s
non-blocking triage narrow the round-2 escalation tiebreak. Previously,
from the second review round on, every ambiguous finding escalated to
the filer regardless of its own size. Now it escalates only when
fixing it would widen the diff with new behaviour, a new surface, or an
edit large enough to need its own review. The triage keeps fixing inline
an ambiguous finding whose fix is small and stays inside what the branch
already touches. Deferring is still the safer failure mode, but only
where a silently widened diff is the actual alternative. Elsewhere,
filing costs more than the nit does.

The in-scope surface item 1 fixes into also widens to count the
branch's own earlier-round absorbed fixes. Lines this branch itself
touched in an earlier round are this branch's own work, so a finding
about them is in scope at every round, not just the round that first
touched them. What stays out of scope is surface this branch never
touched at all. That pin, not the round number, bounds where the
in-scope surface can grow. It widens with this branch's own fixes,
never onto code this branch never wrote, so round 2 does not
file more and fix less at the same time.

## After — method only, no data yet

This diff cannot produce the after side of the measurement. That
requires observing real runs after this change is live, over a
comparable window to the before arm above, and that run set does not
exist until this merges and dispatches for a comparable stretch.

Once a batch of runs has dispatched under this narrowed wording, the
re-check that closes the loop is to read the
`filed=ok:N,failed:N,skipped:N` counts each run reports on its own
status output over that batch, rather than scraping the tracker after
the fact. Scraping the tracker is how the original blowup went
unnoticed for a week. Compare the resulting per-run rate against the
**1.47-per-PR rate over 2026-09-19 to 09-20** recorded above, this
change's own before arm, not against the 0.47 pre-`87716fc1`
baseline. 0.47 is not the target. Reaching it would mean suppressing
the genuine `fix`-tier findings the issue requires be kept, the same
findings the "What changed" section above deliberately still escalates
or fixes rather than drops. The re-tuning has failed only if the
absolute per-run count of genuine (`fix`-tier) findings drops, not if
the 45% figure above does. The `ok:N` figure in the
`filed=ok:N,failed:N,skipped:N` tally above already supplies that
count. 45% is a *share* of filings, not a count, and is the wrong guard
here. This change's own success mode is fewer duplicate and low-worth
filings, which mechanically raises that share even when genuine
findings hold steady or fall. A drop in the raw filing rate, driven by
fewer duplicate and low-worth escalations, is exactly what this change
is meant to produce, not a failure.
