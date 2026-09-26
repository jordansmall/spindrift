Your GitHub token is read-only here — you cannot comment on the issue
yourself. Send the verdict over the Signal socket instead, so the launcher
posts it host-side once you exit. Send it in one call, the body on stdin —
never write the body to a file in one call and send it in the next:

```sh
driver-exec signal comment <<'SPINDRIFT_SIGNAL_EOF'
<verdict body>
SPINDRIFT_SIGNAL_EOF
```

The closing `SPINDRIFT_SIGNAL_EOF` must sit alone at column one — an
indented copy never ends the heredoc.

The only flag is `-body-file` (optional): `-body-file <file>` is the
fallback for a body that already sits in a file; without it the body is
read from stdin. The quotes around the delimiter keep `$` and backticks in
the body literal, and the distinctive delimiter stops a bare `EOF` line
in quoted issue or comment text from ending the body early. The body is
the verdict comment, structured per the sections below. It goes over the
socket whole — no marker line, no base64, and none of the log carrier's
8 KB result cap to budget against — up to a hard ceiling of 65536 bytes
per field. A body over that ceiling is refused outright, never truncated,
so a verdict that overruns it costs a retry rather than posting half.

Run the command bare — never through `tail`, `head`, or `2>&1 |`. The
command's exit code is the acceptance, and a pipe replaces it with the
last piped command's own. A zero exit prints `signal comment accepted: <n>
bytes, <hash>, sequence <n>`, and the comment is taken. A non-zero exit
prints `signal comment rejected (<status>): <reason>` — the signal was NOT
taken, so read the reason, fix it, and send the command again.

Sending the command again replaces the earlier body rather than racing it,
so correcting a verdict costs one more call, never the run.
