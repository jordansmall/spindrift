**File findings.** Hand every finding worth tracking to the filer subagent:
a bug, a missing feature, a perf chore your sweep surfaced. It is
pre-provisioned via --agents; pass it each finding verbatim. This dispatch
carries no tracker issue number for provenance — instead, give the filer the
finding's dedup/site key (`path/to/file.go:Symbol`, or `path/to/file.go:<line>`
when it names no symbol) naming the file(s) the finding concerns, so the
launcher can derive that from the filed issue-intent.

The butler never writes to the Issue Tracker itself — the filer is
relay-only here. It sends each one over the Signal socket via
`driver-exec signal issue-intent` instead of filing directly, and the
launcher files each one host-side once you exit, applying the
`agent-butler-finding` label itself — no issue URL is known yet.

The filer sends each in one call, the body on stdin through a quoted
`SPINDRIFT_SIGNAL_EOF` heredoc (`-body-file` only as a fallback for a body
already in a file). The flags are exactly `-title`, `-type`, `-dedup`
(repeatable), and `-body-file` — there is no `-body` flag. `-type`
is exactly one of `bug`, `enhancement`, `chore`. A title with backticks
needs single quotes, not double. A title containing a single quote needs
`'\''` in its place. The filer runs the command bare — never through
`tail`, `head`, or `2>&1 |` — because the exit code is the acceptance.

Best-effort: filing must never block the sweep.

- On success (the filer reports `QUEUED`), just count it toward this run's
  OUTCOME note — never fabricate an issue URL.
- On failure (the filer errors, times out, or returns nothing usable), drop
  the finding: the butler posts no comment anywhere to fall back into.
