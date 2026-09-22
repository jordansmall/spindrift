2. Your token is read-only — do NOT `gh pr create`. Send your intended draft
   PR's title and body over the Signal socket instead, so the launcher opens
   the draft PR host-side once you exit:

   driver-exec signal pr-intent -title "<conventional title>" -body-file pr-body.md

   (or pipe the body on stdin: `driver-exec signal pr-intent -title "..."`
   with no `-body-file`). The title is its own flag, so the launcher never
   has to guess where the title ends.

   The command's exit code is the acceptance: a zero exit prints `signal
   pr-intent accepted: <n> bytes, <hash>, sequence <n>`, and the intent is
   taken. A non-zero exit prints `signal pr-intent rejected (<status>):
   <reason>` — the intent was NOT taken, so read the reason, fix it, and
   send the command again.

   Sending the command again replaces the earlier intent rather than racing
   it, so correcting a title or body costs one more call, never the run.
