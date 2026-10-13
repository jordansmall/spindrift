2. Your token is read-only — do NOT `gh pr create`. Print your intended draft
   PR's title and body as a single line on stdout instead — the launcher
   finds it by this run's nonce, decodes it, and opens the draft PR
   host-side, once you exit:

   SPINDRIFT_PR_INTENT ${RUN_NONCE} <base64-encoded title, a blank line,
   then the body>

   The first field after `SPINDRIFT_PR_INTENT` must be exactly this run's
   nonce, `${RUN_NONCE}` (issue #1938): it lets the host tell a line this run
   genuinely wrote from one an untrusted issue or comment author echoed into
   the log, so a line with the nonce missing or wrong is silently dropped
   (issue #1939).

   Build the payload by joining the PR title, a blank line, and the PR body,
   then base64-encoding the result into one unbroken token with no embedded
   newlines or spaces (e.g. `printf '%s\n\n%s' "<conventional title>"
   "<summary>" | base64 -w0`). Print exactly ONE such line, before the
   SPINDRIFT_OUTCOME line below.
