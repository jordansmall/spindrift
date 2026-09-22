2. Your token is read-only — do NOT `gh pr view` or `gh pr create`. Send
   your intended draft PR's title and body over the Signal socket instead,
   the same way the OPEN A PULL REQUEST section above describes:

   driver-exec signal pr-intent -title "<conventional title>" -body-file pr-body.md
