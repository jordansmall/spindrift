1. Your token is read-only — do NOT `git push`. Write what you have as a
   bundle to the outbox instead (or note if even that is impossible); the
   launcher relays it in and pushes it host-side with its own token:

   `git bundle create /outbox/seam.bundle origin/${BASE_BRANCH}..${BRANCH}`
