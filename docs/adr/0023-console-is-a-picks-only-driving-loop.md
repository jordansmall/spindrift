# The Console is a picks-only interactive driving loop

Composing a parallel run today takes two tools: pre-label issues in the GitHub
UI, then start a headless loop and watch it through filesystem archaeology —
`logs/issue-N.log` tails, `blocked.txt`, label queries. The human launch
decision and the observation surface live nowhere near each other. We want one
interactive surface where the operator picks the issues to work in parallel
and drills into each Dispatch's work.

**Decision: the Console is a launcher subcommand that is itself a driving
loop — the driver's seat, not a spectator — and its discovery is picks-only.**
Nothing launches that the operator did not Pick: the session's queue is the
operator's picked list, consumed through the continuous engine's existing
`Discoverer` seam in place of the label poll. Already-`Dispatchable` issues
appear flagged in the picker but launch only when picked; "pick all ready" is
an explicit bulk gesture, not standing discovery. Picking an unlabeled issue
promotes it through the normal `Dispatchable` transition first — the Pick is
the human launch button, recorded durably on the Issue Tracker, and a
queued-but-unlaunched Pick holds at `Dispatchable`, never `InProgress`.

The session is a living queue, not a one-shot wave composer: picks enter while
Boxes run, slots refill as Dispatches settle, blocked picks hold visibly until
their edges clear (a failed blocker is surfaced, never auto-unpicked), and the
parallelism cap is live — raising launches a held pick immediately, lowering
only gates new launches. The issue listing is advisory and the claim
authoritative: a pick whose atomic claim fails (raced, closed, relabeled)
dissolves with the reason shown, so a stale picker can only produce a failed
claim, never a wrong dispatch — the same guarantee the label trust boundary
gives every other driving loop. The queue itself is in-memory; the Issue
Tracker remains the only durable truth, and a crashed session is re-picked in
seconds rather than reconciled against a stale intent file.

The Console is a peer of the headless loops, not their replacement:
dogfood.sh and CI keep draining the label queue AFK, `dispatch <nums>` stays
the scriptable one-shot, and coexistence is already safe because claims are
atomic label swaps — the Console just surfaces a live `.dogfood.pid` at
startup so a competing drain loop is visible. Quit drains by default
(terminate-all is an explicit escalation); a hard death leaves orphans to the
existing recover path, offered on next start.

## Considered Options

- **A spectator TUI over the dogfood loop** — attach to whatever is running,
  read labels and tail logs, "pick" by mutating labels for the next headless
  invocation. Rejected: it would immediately grow launch buttons anyway, and
  the launcher already holds every seam in-process; a spectator duplicates
  them read-only.
- **Picks plus standing discovery** ("dogfood.sh with a face") — the session
  also drains the label queue into free slots. Rejected: a session that
  launches issues the operator never touched — because a workflow labeled
  something mid-session — undermines exactly the control the Console exists
  to provide.
- **Persisting the session queue** to disk with restore-on-start. Rejected:
  it is the first second source of dispatch-intent truth outside the tracker,
  needs revalidation against it on every restore, and buys seconds of
  re-picking.
- **Detach/daemon split** — Boxes and settles survive the UI process, a
  client reattaches. Rejected for v1: the only option that changes the
  architecture rather than the UI; orphan-recovery-on-restart buys most of
  its value.
- **Bypassing the `Dispatchable` state** for picked issues (open →
  `InProgress` directly). Rejected out of hand: it forks the lifecycle and
  breaks the rule that the dispatch state on the tracker is the trust
  boundary.

## Consequences

- The freshness contract changes shape in-session: what is exit 4 headless
  becomes a stale banner that holds new launches, with a one-key in-session
  rebuild (running Boxes ride out the old image).
- Transcript rendering becomes a Driver capability beside heartbeat and usage
  extraction — the drill-in's default view is the rendered transcript of the
  whole Dispatch across pass logs, with a raw-JSONL toggle; heartbeats move
  to the queue rows. (Superseded by ADR 0025: the transcript docks as a third
  column rather than replacing the backlog/queue rendering entirely.)
- The Pick record is kind-aware (`work` | `research`) from day one; the v1
  UI exposes only work picks until research dispatch ships.
- `MAX_PARALLEL` becomes a resizable limiter in the engine; `MAX_JOBS`
  deliberately gets no Console control — the operator is the wave-size cap.
- Bare `nix run .#` keeps printing help, now pointing at `console`; a
  TTY-gated Console default waits on a doctor-backed unconfigured welcome
  screen.
- The UI is Bubble Tea in `internal/console`, with a strict one-way
  dependency: engine packages never import console.
- Glossary terms Console, Pick, Unpick, and Terminate (ADR 0024) land in
  CONTEXT.md with this decision.
