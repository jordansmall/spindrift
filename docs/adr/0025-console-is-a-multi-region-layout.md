# The Console is a multi-region layout with a docked transcript pane

The Console (ADR 0023) rendered its whole state as one flat text stream —
banner-less, the backlog and the picks queue stacked under bare labels, the
drill-in transcript replacing the entire view. Everything the operator needs is
present, but nothing is signposted: the running-vs-waiting split, the aggregate
status, and the path from "a Box is running" to "show me its work" are all
there in the model yet invisible on screen.

**Decision: the Console renders as three signposted regions — a full-width
banner+status header over a two-column body (queueable backlog left, work queue
right) — and drilling into a running row opens the Transcript as a docked third
column, not a full-screen takeover.** This is a view/interaction change only:
the engine, the lifecycle, and the `Model` semantics are unchanged; the sole
model addition is terminal `Width/Height` (from `WindowSizeMsg`) to drive column
splitting, and every status count already lives in `Picks`/`Cap`/`Live`.

Concretely:

- **Header**: a persistent hardcoded "spindrift" wordmark banner and a one-line status —
  `running L/Cap · waiting N · held N · settled N`, all derived from existing
  state. Stale-image and competing-dogfood warnings render here as alert lines.
  On a terminal too short to afford the banner rows, the header degrades to the
  status line alone.
- **Two-column body**: the backlog (queueable issues) on the left keeps its
  filter and cursor; the work queue on the right is one pick-ordered list, each
  row state-tagged (`[running]`/`[held]`/`[queued]`/`[settled]`/…), held rows
  naming their blocker and running rows carrying their heartbeat. `Tab` moves
  focus between the columns.
- **Context-sensitive `Enter`**: Pick on a focused backlog row, drill-in on a
  focused work-queue row. Drill-in is enabled only where a Transcript exists
  (running / settled / terminated) and is a no-op on queued/held rows, which
  have produced no logs yet.
- **Docked Transcript pane**: drilling in compresses the two columns leftward
  and opens the Transcript (rendered by default, raw-JSONL on toggle) as a
  right-hand column, so the operator keeps watching the queue while reading one
  Dispatch. A key cycles the pane docked → floating → fullscreen; when the
  terminal is too narrow for three columns it falls back to fullscreen
  automatically.

This **supersedes the drill-in consequence of ADR 0023**, which stated the
transcript "replaces the backlog/queue rendering entirely." Keeping the queue
visible while inspecting one Dispatch is the whole point of a driving loop that
runs many in parallel; the earlier "replace entirely" was an artifact of the
flat-stream renderer, not a considered choice.

## Considered Options

- **Stacked full-width sections** (header, then backlog, then queue, each a
  block) — simplest and closest to today, but wastes the terminal's horizontal
  space and keeps the backlog and live queue scrolling past each other rather
  than side by side.
- **Fullscreen-only transcript** (keep 0023's behavior) — maximum reading
  space, but the operator loses sight of every other running Dispatch the moment
  they inspect one, which is exactly the parallel-run visibility the Console
  exists to provide. Retained only as the narrow-terminal fallback and one of
  the three pane modes.
- **Floating-only transcript overlay** — works at any width and keeps the queue
  dimly visible, but a modal box over a greyed body reads as "stop and look at
  this" rather than "keep driving while you read." Kept as a selectable pane
  mode, not the default.
- **Single cursor spanning both columns** (j/k walks backlog then continues into
  the queue as one list) — fewer keys, but a long combined list blurs "which
  side am I on" and couples two lists with different actions. Rejected for the
  clearer `Tab`-switched focus.

## Consequences

- The transcript content and its raw/rendered toggle are unchanged — only its
  container moves — so `Transcript` stays the canonical glossary term
  (CONTEXT.md), with "narrative log" / "running log" / "log" under `_Avoid_`.
- The Console gains a small piece of layout state: focused column, and the
  transcript pane mode (docked/floating/fullscreen). Keybindings for the
  pane-mode cycle land alongside the existing raw/rendered toggle and close.
- The strict one-way dependency of ADR 0023 holds: the layout lives entirely in
  `internal/console`; engine packages still never import it.
- Nothing here adds a new derived stream, a `MAX_JOBS` control, or a
  daemon/detach split — those remain out of scope, as in 0023.
