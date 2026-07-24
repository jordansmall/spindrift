2. Your token is read-only — do NOT `gh pr create`. Print your intended draft
   PR's title and body as a single delimited block on stdout instead; the
   launcher opens the draft PR host-side, once you exit:

   SPINDRIFT_PR_INTENT_BEGIN
   <conventional title>

   <summary>
   SPINDRIFT_PR_INTENT_END

   The first line is the PR title; everything after the blank line is the PR
   body. Print exactly ONE such block, before the SPINDRIFT_OUTCOME line
   below. Never write a line reading exactly `SPINDRIFT_PR_INTENT_END` inside
   the body itself — it would close the block early and truncate your PR.
