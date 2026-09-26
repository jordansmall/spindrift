2. Your token is read-only — do NOT `gh pr view` or `gh pr create`. Send
   your intended draft PR's title and body over the Signal socket instead,
   so the launcher opens the draft PR host-side once you exit. Send it
   in one call, the body on stdin — never write the body to a file in
   one call and send it in the next:

```sh
driver-exec signal pr-intent -title '<conventional title>' <<'SPINDRIFT_SIGNAL_EOF'
<PR body>
SPINDRIFT_SIGNAL_EOF
```

   The closing `SPINDRIFT_SIGNAL_EOF` must sit alone at column one — an
   indented copy never ends the heredoc.

   The flags are exactly `-title` (required) and `-body-file`
   (optional). There is no `-nonce` flag, and the run's nonce goes
   neither on this command nor on the outcome line. `-body-file <file>`
   is the fallback for a body that already sits in a file; without it the
   body is read from stdin. The title is its own flag, so the launcher
   never has to guess where the title ends; quote a title containing
   backticks in single quotes, so the shell does not run them as a command
   substitution. A title containing a single quote needs `'\''` in its
   place (closing the quote, an escaped quote, reopening it). The quotes
   around the delimiter keep `$` and backticks in the body literal, and
   the distinctive delimiter stops a bare `EOF` line in quoted issue or
   comment text from ending the body early.

   Run the command bare — never through `tail`, `head`, or `2>&1 |`. The
   command's exit code is the acceptance, and a pipe replaces it with the
   last piped command's own. A zero exit prints `signal pr-intent accepted:
   <n> bytes, <hash>, sequence <n>`, and the intent is taken. A non-zero exit
   prints `signal pr-intent rejected (<status>): <reason>` — the intent was
   NOT taken, so read the reason, fix it, and send the command again.

   Sending the command again replaces the earlier intent rather than racing
   it, so correcting a title or body costs one more call, never the run.
