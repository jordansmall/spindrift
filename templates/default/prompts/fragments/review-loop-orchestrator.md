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
finding spawns more issues than the work ever closes; the default is to
resolve them here:

1. Fix inline, on this branch, every finding whose fix is cheap and in scope
   for this change — most nits, smells, dead code, misleading names, and doc
   updates for a surface this diff already touches. Re-run checks, then commit
   them the same way — amended into the commit each logically belongs to
   unless it is a reasonably separate scope, which earns its own commit. They
   never become issues.
2. Escalate — to the filer if present, else the PR body — only a finding that
   genuinely needs a human: a real design trade-off, work outside this issue's
   scope, or a change too large to fold in without derailing the slice. When
   unsure whether a finding clears that bar, fix it rather than file it.
