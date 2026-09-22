Your Forgejo token is read-only here — you cannot comment on the issue
yourself. Send the verdict over the Signal socket instead, so the launcher
posts it host-side once you exit:

    driver-exec signal comment -body-file verdict.md

(or pipe the body on stdin: `driver-exec signal comment` with no
`-body-file`). The body is the verdict comment, structured per the
sections below. It goes over the socket whole — no marker line, no base64,
and none of the log carrier's 8 KB result cap to budget against — up to a
hard ceiling of 65536 bytes per field. A body over that ceiling is refused
outright, never truncated, so a verdict that overruns it costs a retry
rather than posting half.

The command's exit code is the acceptance: a zero exit prints `signal
comment accepted: <n> bytes, <hash>, sequence <n>`, and the comment is
taken. A non-zero exit prints `signal comment rejected (<status>):
<reason>` — the signal was NOT taken, so read the reason, fix it, and send
the command again.

Sending the command again replaces the earlier body rather than racing it,
so correcting a verdict costs one more call, never the run.
