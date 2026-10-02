# Round-2 escalation flip recalibration (issue #3611)

## Before (baseline, recorded in the issue)

Commit `87716fc1` moved the `/code-review` two-axis fan-out from the
unrostered `general-purpose` agent onto the rostered `review-axis`
entry. The sharper axes took review-findings per merged PR from
0.47 over 2026-09-01 to 09-06 up to 1.47 over 2026-09-19 to 09-20 —
a 3.1x rise — at a reproduction rate of 1.71: seven runs working a
review-finding issue spawned twelve more, so the backlog grew even
when dispatch worked nothing else. This change's own before arm is
the **1.47-per-PR rate over 2026-09-19 to 09-20**, the window right
after `87716fc1` landed and right before this recalibration; 0.47 is
the *pre*-`87716fc1` rate, a different arm entirely, not this
change's baseline. The two figures use different denominators, so
`## After` recomputes this arm as 1.51 per merged agent PR and
compares against that, not against 1.47.

The findings themselves stayed genuine across that window: `fix`-tier
filings rose to 45% of the total, up from a 35% baseline. That is why
the flip was narrowed rather than switched off — the target is
duplicate and low-worth filings, never real ones, and a narrowing
that suppressed genuine findings would fail the issue's own
requirement to keep them.

Both the 0.47 and the 1.47 figures above are tracker-scraped, not
drawn from the per-run `filed=ok:N,failed:N,skipped:N` tally (issue
#3608, issue #3609): that tally landed in this same campaign, after
both measured windows, so no run in either window emitted one.

## What changed

`review-loop-orchestrator.md` and `review-loop-inline.md`'s
non-blocking triage narrow the round-2 escalation tiebreak. Previously,
from the second review round on, every ambiguous finding escalated to
the filer regardless of its own size. Now it escalates only when
fixing it would widen the diff — new behaviour, a new surface, or an
edit large enough to need its own review — and keeps fixing inline an
ambiguous finding whose fix is small and stays inside what the branch
already touches. Deferring is still the safer failure mode, but only
where a silently widened diff is the actual alternative; elsewhere
filing costs more than the nit does.

The in-scope surface item 1 fixes into is also widened to count the
branch's own earlier-round absorbed fixes: lines this branch itself
touched in an earlier round are this branch's own work, so a finding
about them is in scope at every round, not just the round that first
touched them. What stays out of scope is surface this branch never
touched at all — that pin, not the round number, is what bounds where
the in-scope surface can grow: it widens with this branch's own fixes,
never onto code this branch never wrote, so round 2 does not
simultaneously file more and fix less.

## After (2026-09-24 to 09-29)

The after arm is recorded from the tracker. It fails the guard this
document set: absolute `fix`-tier findings per agent PR fell. The
recorded figures above are not reused; every arm is recomputed below
on one definition and one denominator.

### Method

`fix`-tier means a non-`chore` finding: an `agent-review-finding`
issue carrying the `bug` or `enhancement` finding-type label (the
filer's `-type`, mapped host-side by `ensureTypeLabel` against
`doctor.FindingTypeLabels`). Of the definitions tried, it is the only
one that reproduces the recorded shares: `bug` plus `enhancement` gives 37% for
2026-09-01..09-06 (recorded 35%) and 44% for 2026-09-19..09-20
(recorded 45%), where `bug` alone gives 24% and 34%. Untyped findings
(no type label) count in the total, not in `fix`-tier.

Correction (issue #3839): an earlier version of this section said the
`ok:N` figure of the `filed=ok:N,failed:N,skipped:N` tally supplies
the `fix`-tier count. It does not. `ok:N` is total filing volume:
`tallyFiled` buckets only on skipped and failed, and
`filedTally.String` renders no finding type. The `fix`-tier subset is
obtainable only from the tracker labels above.

The denominator is merged agent PRs (head branch starts `agent/`) for
every arm. The per-run tally lives only in the host's gitignored
`.spindrift/logs/issue-*.log`, unavailable to this measurement, so a
per-run rate is not used. The recorded 0.47 and 1.47 do not share one
denominator: 0.47 is 75 findings over 161 merged PRs of all authors,
while 1.47 is closest to 62 over 41 agent PRs (1.51; 62 over 49
all-author PRs is 1.27). Windows are UTC creation and merge days. The
after window stops at 2026-09-29 because `b6e64772` (2026-09-30,
"fix(review-loop): never re-escalate a filed finding", refs #4108)
moves the same metric.

### Data

| Window | Agent PRs merged (all) | Review findings | bug | enhancement | chore | untyped | fix-tier | fix-tier share | findings / agent PR | fix-tier / agent PR |
|---|---|---|---|---|---|---|---|---|---|---|
| 2026-09-01..09-06 (pre-`87716fc1`) | 140 (161) | 75 | 18 | 10 | 42 | 5 | 28 | 37% | 0.54 | 0.20 |
| 2026-09-19..09-20 (before arm) | 41 (49) | 62 | 21 | 6 | 27 | 8 | 27 | 44% | 1.51 | 0.66 |
| 2026-09-24..09-29 (after arm) | 115 (131) | 74 | 19 | 24 | 31 | 0 | 43 | 58% | 0.64 | 0.37 |

Merge throughput is comparable, about 20.5 agent PRs a day before
against about 19 a day after, so the after window is larger in total
(6 days against 2), not thinner. To reproduce the after row, change
the dates for the others:

```sh
gh issue list --label agent-review-finding --state all --search 'created:2026-09-24..2026-09-29' --limit 400 --json labels \
  --jq '[.[]|[.labels[].name]|map(select(.=="bug" or .=="enhancement" or .=="chore"))|(.[0]//"none")]|group_by(.)|map("\(.[0])=\(length)")|join(" ")'
gh pr list --state merged --search 'merged:2026-09-24..2026-09-29' --limit 400 --json headRefName \
  --jq '"merged=\(length) agent=\([.[]|select(.headRefName|startswith("agent/"))]|length)"'
```

### Result

The guard failed. Total findings per agent PR fell 57% (1.51 to
0.64), the intended effect. But absolute `fix`-tier findings per agent
PR fell 43% (0.66 to 0.37), which is the failure this document's own
criterion names. The share rising from 44% to 58% is exactly the
mechanical rise this document warned is not a guard: fewer total
filings lift the share even as genuine ones fall.

The drop survives the untyped ambiguity. Counting all 8 before-arm
untyped findings as non-`fix`-tier gives 0.66; counting them as
`fix`-tier gives 35 over 41, or 0.85. The after arm's 0.37 is lower
either way. It is still above the pre-`87716fc1` 0.20.

### Confounders

The after arm measures the campaign's combined effect, not this
recalibration alone. Between the two arms, all on 2026-09-23, #3609
(dedup skip) closed, #3610 (drop trivial out-of-scope findings as a
third triage outcome) closed, #3808 (per-site dedup) closed, and
`6d25bdbe` ("file an uncertain out-of-scope finding") landed. Dedup
and drop can remove `fix`-tier filings too, so tracker data cannot
attribute the drop to the round-2 wording specifically. What would
separate them is host-side: the per-run `filed=` tally (issue #3608)
and the dedup `skipped:N` counts.

### Next step

The #3821 run queued a follow-up issue for filing to revisit the
round-2 wording, and
separating the campaign's contributions using the host-side per-run
tally. 0.47 is still not the target: reaching it would mean
suppressing the genuine `fix`-tier findings the issue requires be
kept.
