Before the PR, spawn a fresh `reviewer` subagent on the branch diff vs
`${BASE_BRANCH}`. Do NOT review inline — an inline review ends your turn at the
halfway gate; delegating returns a result to act on. The `reviewer` is
pre-provisioned via `--agents`; pass only the issue number.

Its final message starts `VERDICT: APPROVE` or `VERDICT: BLOCK`. This exact
wording is load-bearing (ADR 0035): the in-box orchestrator's scanPassLog
greps for it verbatim, and rewording it silently collapses the multi-pass
loop to single-pass on ORCHESTRATOR_ENABLED runs. On BLOCK:

1. Fix on this branch, run checks, then commit. Unless the fix is a
   reasonably different change, fold it into the existing commit where it
   logically belongs — `git commit --amend` or an autosquash fixup — rather
   than tacking on a follow-up. Add a *new* commit only when the fix is
   truly a separate file or scope. The branch force-pushes, so rewriting
   your own unmerged history here is expected.
2. Re-invoke a fresh `reviewer` (not the same instance).
3. Repeat until no blocking findings remain.
4. Re-scout only if the finding shows the change is in the wrong place.

Never open the PR with a blocking finding open.

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

Also triage the Non-blocking findings — do NOT reflexively file
them. There are three outcomes: fix inline, drop, or escalate. Filing
every finding spawns more issues than the work ever closes, so resolving
a cheap, in-scope finding here stays the default regardless of round
(item 1 below, unchanged by round), and dropping a correct-but-trivial,
out-of-scope finding (item 2 below) is also unchanged by round. Only the
tiebreak for an ambiguous finding
headed for escalation is round-aware (item 3 below): this is the first
review round if you've invoked the reviewer exactly once this turn; it
is the second review round or later once you've already been through at
least one BLOCK-fix-reinvoke cycle above. This round-awareness applies
only to the non-blocking triage below, not the BLOCK loop above, which
always repeats until clear:

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
   Scope is never the uncertain part — whether this branch touched a
   surface is a fact the diff answers — so what reaches item 3 this way
   is still out of scope, and item 3 files it rather than fixing it.
3. Escalate — to the filer if present, else the PR body — only a finding
   that genuinely needs a human: a real design trade-off, work outside
   this issue's scope, or a change too large to fold in without derailing
   the slice. Item 1's scope pin is absolute here, so the round-aware
   tiebreak below only ever runs where fixing is available at all —
   inside the surface this branch already touched. On a surface this
   branch never touched there is no inline fix to weigh, so an uncertain
   finding item 2 hands up is filed, at every round. When unsure whether
   a finding clears that bar: on the first review round, fix it rather
   than file it. From the second review round on,
   escalate it only when fixing it would widen the diff — new behaviour,
   a new surface, or an edit large enough to need its own review; an
   ambiguous finding whose fix is small and stays inside what this branch
   already touches is still fixed inline, at every round.
   Deferring is the safer failure mode precisely where a silently widened
   diff is the alternative, and only there: elsewhere filing is the
   costlier failure — a backlog issue nobody picks up, and a duplicate a
   later run rediscovers and files again. Narrowing the round-2 flip to
   diff growth is this tiebreak's calibration, not a weakening of it.
