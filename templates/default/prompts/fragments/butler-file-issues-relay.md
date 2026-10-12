**File findings.** Hand every finding worth tracking to the filer subagent:
a bug, a missing feature, a perf chore your sweep surfaced. It is
pre-provisioned via --agents; pass it each finding verbatim. This dispatch
carries no tracker issue number for provenance — instead, give the filer the
finding's dedup/site key (`path/to/file.go:Symbol`, where Symbol is the
symbol it names or else the nearest enclosing one, or `path/to/file.go:<line>`
only when no symbol encloses the site) naming the file(s) the finding
concerns, so the launcher can derive that from the filed issue-intent. Also
give the filer the finding's class, and, only when the `reviewer` agreed on
it (see PROMOTION CANDIDATES above), the reviewer's one-line concurrence,
and, when the finding is a patch candidate the reviewer approved, its diff
as the finding's patch.

The butler never writes to the Issue Tracker itself — the filer is
relay-only here. It emits `SPINDRIFT_ISSUE_INTENT` lines instead of filing
directly, and the launcher files each one host-side once you exit, applying
the `agent-butler-finding` label itself — no issue URL is known yet.

Best-effort: filing must never block the sweep.

- On success (the filer reports `QUEUED`), just count it toward this run's
  OUTCOME note — never fabricate an issue URL.
- If the filer reports its class was rejected as off the class list,
  re-delegate the finding with a class from the valid ones the rejection
  names (see FINDING CLASSES above) — never drop it for that.
- On any other failure (the filer errors, times out, or returns nothing
  usable), drop the finding: the butler posts no comment anywhere to fall
  back into.
