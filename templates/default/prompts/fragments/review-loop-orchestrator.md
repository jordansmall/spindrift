Review is handled by the orchestrator as a separate, code-owned pass (issue
#2037) — do not spawn a `reviewer` subagent yourself, and do not loop on
blocking findings in this turn. Look for a "## Run-state handoff" section
below, seeded from a prior pass in this run.

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

When a check surfaces a failure you want to set aside as pre-existing
rather than fix, prove it against a genuinely clean checkout of
`origin/${BASE_BRANCH}` first — a cleaning step that reports nothing to
clean is an unmet precondition, not proof, so do not go on to claim
pre-existence from that run. `git stash` alone does not establish a
clean base: once a slice's own edits are committed, the branch tip is
the dirty state stash cannot touch, so it saves nothing, exits zero, and
any check you then run is still against your own commits, not the base.
Fetch first (`git fetch origin`, so the ref isn't stale), then check out
`origin/${BASE_BRANCH}` itself somewhere outside this working tree — a
sibling worktree (`git worktree add`) or a fresh clone — and re-run the
same check there; that is what actually proves the failure predates this
branch. Prove it wherever the check actually ran, not only on the host —
a failure that reproduces solely in the Box is still real. Report which
revision you verified against and how you reached the clean tree,
directly in your own output this turn, so the claim is auditable from
this log. A failure you cannot prove this way is a failure this branch
caused: fix it, do not wave it off.

Non-blocking triage — do NOT reflexively file every finding. There are
three outcomes: fix inline, drop, or escalate. Filing every finding spawns
more issues than the work ever closes, so resolving a cheap, in-scope
finding here stays the default regardless of round (item 1 below,
unchanged by round), and dropping a correct-but-trivial, out-of-scope
finding (item 2 below) is also unchanged by
round. Only the tiebreak for an ambiguous finding headed for escalation is
round-aware (item 3 below): read the highest N among the top-level
"## Round N (verdict: ...)" section headers in the Findings log file the
handoff's own Findings log bullet points to — not the Decisions record's own
"## Round N" headers shown separately above (those number by pass, not by
review round), and not similar-looking text quoted inside a finding's own
description. N == 1 means this is the first review round; N > 1 means the
second review round or later. If the Findings log bullet is absent, or you
otherwise cannot tell, treat it as the second round or later — under
item 3 below that default is not a blanket deferral but the narrower
diff-growth test, so an ambiguous small in-scope finding is still fixed
inline either way. This round-awareness applies only to the non-blocking
triage below, not the blocking-verdict handling above:

1. Fix inline, on this branch, every finding whose fix is cheap and in
   scope for the slice as originally authored and the issue's own
   acceptance criteria — most nits, smells, dead code, misleading names,
   and doc updates for a surface the *original* slice touches. Lines this
   branch itself touched in an earlier round's own absorbed fix count as
   that surface: they are this branch's own work, so a finding about them
   is in scope at every round. What stays out of scope is surface this
   branch never touched at all — that pin, not the round number, is what
   bounds where the in-scope surface can grow: it widens with this
   branch's own fixes, never onto code this branch never wrote. Re-run
   checks, then commit them the same way — amended into the commit each
   logically belongs to unless it is a reasonably separate scope, which
   earns its own commit. They never become issues.
2. Drop a finding that is correct but trivial and out of scope for
   this slice — the fix would be cheap, but the surface is one this
   slice never touched, and nobody would ever prioritise it as a tracked
   issue. Two examples: swapping a bare commit SHA for an issue reference
   in a test comment, and preferring a numeric sort over a lexical one
   in one line of list output. Dropping is a legitimate third outcome,
   not a failure to act — a tracked issue nobody will ever pick up costs
   more than the nit does. The floor is worth, not certainty: a finding
   you are merely unsure about is not dropped, it goes to item 3 below.
3. Escalate — to the filer if present, else the PR body — only a finding
   that genuinely needs a human: a real design trade-off, work outside
   this issue's scope, or a change too large to fold in without derailing
   the slice. When unsure whether a finding clears that bar: on the first
   review round, fix it rather than file it. From the second review round
   on, escalate it only when fixing it would widen the diff — new
   behaviour, a new surface, or an edit large enough to need its own
   review; an ambiguous finding whose fix is small and stays inside what
   this branch already touches is still fixed inline, at every round.
   Deferring is the safer failure mode precisely where a silently widened
   diff is the alternative, and only there: elsewhere filing is the
   costlier failure — a backlog issue nobody picks up, and a duplicate a
   later run rediscovers and files again. Narrowing the round-2 flip to
   diff growth is this tiebreak's calibration, not a weakening of it.

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
