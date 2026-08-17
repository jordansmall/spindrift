Review is handled by the orchestrator as a separate, code-owned pass (issue
#2037) — do not spawn a `reviewer` subagent yourself, and do not loop on
blocking findings in this turn. Look for a "## Run-state handoff" section
above, seeded from a prior pass in this run.

- **No handoff yet, or its `Last reviewer verdict:` line is not `APPROVE`
  (including a `BLOCK` line, with a `Reviewer findings:` block to fix):**
  Stop your turn now, right after COMMIT. Do not run FILE ISSUES, LAND THE
  CHANGE, OPEN A PULL REQUEST, or OUTCOME below — a fresh review pass runs
  next automatically, and the orchestrator invokes you again afterward,
  seeded with its verdict (and findings, if it blocked).
- **`Last reviewer verdict:` is `APPROVE`:** The review pass already
  reviewed this branch and found nothing blocking. Triage any findings the
  handoff's `Reviewer findings:` block lists under `## Non-blocking` exactly
  as the loop below describes, then continue straight into FILE ISSUES,
  LAND THE CHANGE, OPEN A PULL REQUEST, and OUTCOME.

Non-blocking triage — do NOT reflexively file every finding. Filing every
finding spawns more issues than the work ever closes, so resolving a cheap,
in-scope finding here stays the default regardless of round (item 1 below,
unchanged by round). Only the tiebreak for an ambiguous finding is
round-aware (item 2 below): read the highest N among the top-level
"## Round N (verdict: ...)" section headers in the Findings log file the
handoff's own Findings log bullet points to — not the Decisions record's own
"## Round N" headers shown separately above (those number by pass, not by
review round), and not similar-looking text quoted inside a finding's own
description. N == 1 means this is the first review round; N > 1 means the
second review round or later. If the Findings log bullet is absent, or you
otherwise cannot tell, treat it as the second round or later — deferring
only costs an extra filed issue, not a silently widened diff, so it's the
safer failure mode here. This round-awareness applies only to the
non-blocking triage below, not the blocking-verdict handling above:

1. Fix inline, on this branch, every finding whose fix is cheap and in scope
   for the slice as originally authored and the issue's own acceptance
   criteria — most nits, smells, dead code, misleading names, and doc
   updates for a surface the *original* slice touches, not whatever surface
   the diff has since grown to touch across earlier rounds' own absorbed
   non-blocking fixes. Re-run checks, then commit them the same way —
   amended into the commit each logically belongs to unless it is a
   reasonably separate scope, which earns its own commit. They never
   become issues.
2. Escalate — to the filer if present, else the PR body — only a finding
   that genuinely needs a human: a real design trade-off, work outside
   this issue's scope, or a change too large to fold in without derailing
   the slice. When unsure whether a finding clears that bar: on the first
   review round, fix it rather than file it; from the second review round on,
   escalate it instead. Escalating more from round 2 on is the intended
   effect of this round-awareness, not a regression.

Before stopping this turn, either right after COMMIT or after OUTCOME per the
two cases above, write to `/tmp/pass-summary.md` (outside the repo, never
commit) a free-form summary of what you did this pass and what remains — the
next pass, if any, is seeded with this path.

Also write a second, separate file, `/tmp/dispositions.md` (outside the
repo, never commit) — do not fold this into the pass-summary file above, and
do not put pass-summary prose into this one. It holds nothing but one terse
line per Blocking or Non-blocking finding this pass addressed: `<finding> ->
fixed in commit <SHA>` for one you actually fixed, or `<finding> ->
won't-fix: <reason>` for one you deliberately left alone (an escalated
finding, per the non-blocking triage above, is a won't-fix here). Reference
existing artifacts — the commit SHA, a `file:line`, an issue number — rather
than restating their content: no narrative, no headers, no reasoning beyond
that one-line reason, and no pasted diff hunks, file contents, or transcript
excerpts. A later review round reads this file verbatim and treats every
line as an unverified claim to check against the diff, not settled fact, so
write it precisely enough to survive that check, and terse enough to stay a
reference rather than a restatement — a compact, well-formed entry never
needs more than a short clause after the arrow.

Also write a third, separate file, `/tmp/decisions.md` (outside the repo,
never commit) — never fold this into the pass-summary or dispositions file
above. It holds nothing but one terse line per notable implementation
decision this pass made: what you chose, what you rejected (if a real
alternative existed), and the constraint that drove the choice, e.g.
`<what/where> -> chose <X>, rejected <Y> -- <constraint, with a reference>`.
Not every trivial choice belongs here — only one a later pass, re-deriving
intent from the diff alone, could plausibly get wrong or relocate. Reference
existing artifacts — the commit SHA, a `file:line`, an issue number —
rather than restating their content: no narrative, no pasted diff hunks,
file contents, or transcript excerpts. Write only this pass's own new
decisions; a rejected alternative stays worth keeping even after a later
pass's own choice looks different, and the run's own decisions record
already accumulates every pass's entries across rounds without you doing
anything extra.
