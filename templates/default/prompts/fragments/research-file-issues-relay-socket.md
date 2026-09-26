**File findings.** Hand any finding worth tracking as follow-up work —
beyond the issue's own verdict — to the filer subagent: a bug, a missing
feature, a perf chore your exploration surfaced. It is pre-provisioned via
--agents; pass it each finding verbatim, plus this issue's number for
provenance.

Research never writes to the Issue Tracker itself — the filer is relay-only
here. It sends each one over the Signal socket via
`driver-exec signal issue-intent` instead of filing directly, and the
launcher files each one host-side once you exit, applying the
`agent-research-finding` label and a "Filed from research on #<N>" backlink
itself — no issue URL is known yet.

The filer sends each in one call, the body on stdin through a quoted
`SPINDRIFT_SIGNAL_EOF` heredoc (`-body-file` only as a fallback for a body
already in a file). The flags are exactly `-title`, `-type`, `-dedup`
(repeatable), and `-body-file` — there is no `-body` flag. `-type`
is exactly one of `bug`, `enhancement`, `chore`. A title with backticks
needs single quotes, not double. A title containing a single quote needs
`'\''` in its place. The filer runs the command bare — never through
`tail`, `head`, or `2>&1 |` — because the exit code is the acceptance.

Best-effort: filing must never block the verdict.

- On success (the filer reports `QUEUED`), just note in the verdict that the
  finding was queued for filing — never fabricate an issue URL; the launcher
  appends a "Filed issues" section with the real links right after your
  comment posts.
- On failure (the filer errors, times out, or returns nothing usable),
  describe the finding directly in the verdict body instead.
