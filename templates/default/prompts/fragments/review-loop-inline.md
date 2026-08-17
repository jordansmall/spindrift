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

Then triage the Non-blocking findings — do NOT reflexively file them. Filing
every finding spawns more issues than the work ever closes, so resolving a
cheap, in-scope finding here stays the default regardless of round (item
1 below, unchanged by round). Only the tiebreak for an ambiguous finding
is round-aware (item 2 below): this is the first review round if you've
invoked the reviewer exactly once this turn; it is the second review round
or later once you've already been through at least one BLOCK-fix-reinvoke
cycle above. This round-awareness applies only to the non-blocking triage
below, not the BLOCK loop above, which always repeats until clear:

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
