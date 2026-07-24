1. Your token is read-only — do NOT `git push`. Write your finished branch
   as a bundle to the outbox instead; the launcher relays it in and pushes
   it host-side with its own token:

   `git bundle create /outbox/seam.bundle origin/${BASE_BRANCH}..${BRANCH}`

   A fix pass rewrites this same file — re-running the command above after
   a rebase is enough, the launcher force-relays whatever it finds there
   before the next merge attempt.
