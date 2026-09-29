**File findings.** Hand every finding worth tracking to the filer subagent:
a bug, a missing feature, a perf chore your sweep surfaced. It is
pre-provisioned via --agents; pass it each finding verbatim. This dispatch
carries no tracker issue number for provenance — instead, give the filer the
finding's dedup/site key (`path/to/file.go:Symbol`, or `path/to/file.go:<line>`
when it names no symbol) naming the file(s) the finding concerns, so the
launcher can derive that from the filed issue-intent. Also give the filer the
finding's class, and, only when the `reviewer` agreed on it (see PROMOTION
CANDIDATES above), the reviewer's one-line concurrence, and, when the
finding is a patch candidate the reviewer approved, its diff as the
finding's patch.

The butler never writes to the Issue Tracker itself — the filer is
relay-only here. It emits `SPINDRIFT_ISSUE_INTENT` lines instead of filing
directly, and the launcher files each one host-side once you exit, applying
the `agent-butler-finding` label itself — no issue URL is known yet.

Best-effort: filing must never block the sweep.

- On success (the filer reports `QUEUED`), just count it toward this run's
  OUTCOME note — never fabricate an issue URL.
- On failure (the filer errors, times out, or returns nothing usable), drop
  the finding: the butler posts no comment anywhere to fall back into.
