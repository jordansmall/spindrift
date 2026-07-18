# The Console is a section-switched list with a live-tail sidebar

ADR 0025 rendered the Console body as two side-by-side columns (queueable
backlog left, picks queue right) with a docked Transcript third column, chosen
so a driving loop running many Dispatches keeps backlog and live queue both
visible. But when many Dispatches run at once, the operator's real need is not
to see every queue row simultaneously — it is to select any single running
Dispatch and watch *its own* stream, isolated and reviewable, free of the
interleaving that a shared terminal (`dispatch`, dogfood) makes unreadable. The
two-column body spends horizontal space keeping both lists visible while giving
each running Dispatch only a one-line heartbeat and a frozen, one-shot
Transcript on drill-in.

**Decision: the Console body becomes a section-switched single list with a
live-tail sidebar.** One Section is shown at a time — Backlog, Running, Held,
Settled, Failed — as an aligned table under the persistent status header;
selecting a running row opens its isolated, live-tailing Activity feed in a
right-hand sidebar. This supersedes the two-column body of ADR 0025 while
keeping its status-header intent and its liveness guarantee: the header still
carries the aggregate at-a-glance (`running L/Cap · waiting · held · settled ·
failed`), so switching to the Backlog section never blinds the operator to how
many Dispatches run.

Concretely:

- **Sections** replace the side-by-side columns. The Backlog section is the
  pick source and keeps its label substring filter; Running / Held / Settled /
  Failed slice the work queue by `PickState`. Switched with `H`/`L` (previous /
  next) or `1`–`5` (direct jump).
- **The live-tail sidebar** replaces the docked / floating / fullscreen
  Transcript pane. Selecting a running row (`Enter`) attaches its Activity feed
  — a condensed, timestamped feed, one line per Driver step, derived by
  replaying the Dispatch's pass log through the heartbeat parser (keeping the
  whole sequence, not just the last line the queue-row heartbeat kept). It
  follows the newest line by default; scrolling up detaches follow for review;
  `G`/`End` re-attaches; per-issue scroll and follow state are retained across
  selections, so hopping between running Dispatches never loses your place. `t`
  toggles the sidebar to the full rendered Transcript, and a fullscreen "zoom"
  survives for deep reading. Floating mode is dropped.
- **Vim keymap**: `j`/`k` (and `↓`/`↑`) move within the focused pane, `h`/`l`
  (and `←`/`→`) move focus between the list and the sidebar, `H`/`L` switch
  Sections, `1`–`5` jump. Terminate moves off `k` (now "up") to `X`; the
  existing pick / unpick / filter / cap / rebuild / quit / help keys carry over.
- **Rendering foundation**: lipgloss — already in the module graph, promoted to
  a direct dependency — styles color, borders, the section tabs, the table, and
  the column joins; the existing hand-rolled windowing / scroll logic is kept
  and extended for the live-tail's follow mode. No new component dependency (no
  `bubbles`), consistent with the module's no-figlet minimalism.

## Considered Options

- **Keep the two-column body, only make the docked Transcript live-tail.**
  Rejected: it under-delivers the actual need — a scannable, section-organized
  way to move among many running Dispatches — and keeps spending width on two
  lists when the operator inspects one at a time.
- **Pure tabbed sections with no persistent liveness.** Rejected: it
  reintroduces exactly what ADR 0025 rejected, losing sight of running work
  while on another tab; the persistent status header is what buys the liveness
  back.
- **The full rendered Transcript as the live-tail default.** Rejected: a
  running Transcript is a firehose — the very noise that makes many concurrent
  Dispatches unreadable. The condensed Activity feed is the scannable default,
  with the Transcript one key (`t`) away.
- **Adopt `bubbles` (viewport / table).** Rejected: a new module and discarding
  mature, tested scroll logic for behavior the existing windowing already nearly
  provides.

## Consequences

- Supersedes the two-column body and the docked / floating / fullscreen
  pane-mode cycle of ADR 0025. Layout state simplifies to the active Section,
  the focused pane, and per-issue live-tail position.
- The Activity feed is a new view derived from existing logs — no new stored
  stream and no live in-memory capture. It is a pure function of the on-disk
  pass logs, so a crashed and reopened session reconstructs it by re-parsing.
- The drill-in stops being a one-shot: the Console now polls the selected
  running Dispatch's log to advance its feed (piggybacking the existing per-Msg
  sync tick), scoped to the selected Dispatch to bound I/O.
- lipgloss becomes a direct dependency; view tests keep their substring
  assertions and gain golden snapshots for layout and style.
- The strict one-way dependency of ADR 0023 holds: the layout lives entirely in
  `internal/console`; engine packages never import it.
- Glossary terms Section, Backlog, and Activity feed land in CONTEXT.md;
  Transcript is amended to name the Activity feed as its condensed peer view.
