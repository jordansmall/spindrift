- **No handoff yet, or its `Last reviewer verdict:` line is not `APPROVE`
  (including a `BLOCK` line, with a `Reviewer findings:` block to fix):**
  Stop your turn now, right after COMMIT. Do not run LAND THE CHANGE, OPEN A
  PULL REQUEST, or OUTCOME below — a fresh review pass runs next
  automatically, and the orchestrator invokes you again afterward, seeded
  with its verdict (and findings, if it blocked).
- **`Last reviewer verdict:` is `APPROVE`:** The review pass already
  reviewed this branch and found nothing blocking. Triage any findings the
  handoff's `Reviewer findings:` block lists under `## Non-blocking` exactly
  as the loop below describes, then continue straight into LAND THE CHANGE,
  OPEN A PULL REQUEST, and OUTCOME.
