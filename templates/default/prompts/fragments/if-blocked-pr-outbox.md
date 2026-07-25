2. Your token is read-only — do NOT `gh pr view` or `gh pr create`. Print
   your intended draft PR's title and body as a single nonce-guarded line
   instead — the launcher finds it by this run's nonce, decodes it, and
   opens the draft PR host-side, once you exit:

   SPINDRIFT_PR_INTENT ${RUN_NONCE} <base64-encoded title, a blank line,
   then the body>

   Build the payload the same way the OPEN A PULL REQUEST section above
   describes.
