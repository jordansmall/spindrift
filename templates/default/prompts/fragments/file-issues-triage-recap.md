REVIEW's triage has three outcomes, and two of them are already settled by
the time the filer runs: what was cheap and in scope was fixed inline, and
what was trivial and out of scope was dropped without filing. Escalation is
the third, and it is this step. Delegate to the filer subagent only the
findings that survived that triage — the ones that genuinely need a human
(a design trade-off, out-of-scope work, or a change too large to fold in),
plus, from the second review round on and only when fixing it would widen
the diff, an ambiguous finding REVIEW's own round-aware tiebreak deferred
rather than fixed — one whose fix is small and stays inside what the branch
already touches is still fixed inline, at every round. An uncertain finding
on a surface the branch never touched arrives here at every round instead:
fixing it inline was never an available outcome. If none survived, skip this
step; do not re-file what you just fixed or dropped.
