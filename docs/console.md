# The interactive Console

`spindrift console` opens the interactive Console (ADR 0023): an in-terminal
loop that lists every open issue from the Issue Tracker — number, title, labels
— oldest-first per dispatch order, and lets you Pick issues to launch as
Dispatches.

```sh
spindrift console
```

Type a command and press enter:

| command | effect |
|---------|--------|
| `r` / `refresh` | re-query the Issue Tracker and re-render the backlog |
| `f <text>` / `filter <text>` | narrow the list to issues with a label containing `<text>` |
| `f` / `filter` (no text) | clear the filter, restoring the full list |
| `p <num>` / `pick <num>` | Pick issue `<num>` — the launch button |
| `pa` / `pick-all-ready` | Pick every issue currently `Dispatchable` — the bulk launch button |
| `u <num>` / `unpick <num>` | Unpick a queued-but-unlaunched pick |
| `enter` (queue focus) | Drill in: open the highlighted pick's rendered transcript |
| `t` / `toggle` | toggle the open transcript between rendered and raw |
| `x` / `close` | close the transcript view, back to the backlog/queue |
| `k <num>` / `kill <num>` / `terminate <num>` | ask to Terminate `<num>`'s live Dispatch — prompts `y`/`N` to confirm, `q`/`ctrl+c` decline and quit |
| `+` | raise the session's live parallelism cap by one |
| `-` | lower the session's live parallelism cap by one |
| `b` / `build` / `rebuild` | rebuild the image in-session when stale — no confirm needed |
| `q` / `quit` | quit — immediately with nothing live, otherwise offers drain/terminate-all/stay (see Quit below) |

If a `.dogfood.pid` file is present at startup — a headless loop
(`dogfood.sh`) already draining the same queue — the Console prints an
informational notice and keeps going; it never blocks or refuses to start,
and the two are safe to run side by side (claims are atomic label swaps).

## Pick

**Pick** is the launch button. An unlabeled issue is promoted through the
normal `Dispatchable` transition first — recorded durably on the tracker —
then queued; an already-`Dispatchable` issue queues directly. The pick
launches through the same continuous engine the headless loops use, up to
the session's live parallelism cap at once (starting at `MAX_PARALLEL`, the
same knob `run`'s wave dispatch honors, and resizable in-session with `+`/`-`):
its queue row tracks `queued` → `claiming` → `running` → `settled`, and as
each running pick settles, the next queued pick fills the slot it freed — the
session's queue drains continuously without re-invocation. Queued-but-
unlaunched picks hold at `Dispatchable` on the tracker, never `InProgress` —
the claim to `InProgress` only happens when the pick's turn to launch
actually arrives. If that claim races (another loop, the issue closed, a
relabel), the pick dissolves and its row shows why, instead of launching a
Box for a stale listing.

**Held picks**: picking an issue whose blockers are still open does not
dissolve it — the row goes `held` with a "held by #N" badge naming the
unmet blockers, and stays `Dispatchable` on the tracker the whole time it
sits held. Blocker resolution reuses the same edge machinery the headless
waves use (no second dependency parser); a held row re-evaluates on every
refill and launches with no operator action the moment every blocker reaches
`Complete`. If a blocker instead lands `Failed`, the row surfaces it
(`blocker #N failed`) but stays held — the Console never auto-unpicks;
`u <num>` still works on a held row exactly as it does on a queued one, so the
operator decides whether to wait or give up on it.

**Pick all ready** (`pa`) picks exactly the issues currently `Dispatchable`
on the tracker, in one snapshot query — an explicit action, never standing
discovery: an issue that becomes `Dispatchable` after `pa` returns is not
picked until the operator asks again. Each issue queues through the same
Pick path a single `p <num>` uses.

**Unpick** removes a queued-but-unlaunched pick — including a held one — from
the session with zero Issue Tracker calls — it only ever un-does the
in-session queue entry, never the durable promotion a pick already recorded.

Every pick defaults to a `work` Dispatch; the record carries a kind field so
research picks can arrive later as a UI gesture rather than a remodel — only
`work` is exposed today.

## Live parallelism cap

`+`/`-` raise or lower the session's parallelism cap by one, and the current
`cap: <live>/<cap>` is always visible above the queue. Raising takes effect
immediately — a held or queued pick launches into the freed slot right away,
without waiting for a running Dispatch to settle or for the background poll.
Lowering never terminates anything: it only gates new launches until the live
count sinks under the new cap on its own, as running Dispatches settle —
`k`/`kill`/`terminate` remains the only way a running Dispatch dies by hand.
`MAX_JOBS` gets no Console control — it caps headless wave size, and in a
picks-only session the operator is already the cap.

## Backlog freshness

The Console keeps the backlog fresh without spending the shared rate-limit
window: `r` re-queries on demand, the backlog auto-refreshes whenever the
session itself writes to the tracker — a claim, a settle, or a promotion —
and a slow background poll re-queries on a fixed cadence (60–120s) even on an
otherwise idle session. Nothing refreshes faster than that poll; only the
session's own writes and the operator's own `r` trigger a refresh in between.

A running pick's queue row also shows its latest heartbeat — phase, turn
count, last tool — reusing the same heartbeat parser the live dispatch's own
terminal output already uses, replayed against the pick's on-disk log. It
updates on every render, since it is a local log read with no Issue Tracker
call behind it.

A queue row's field order is conditional, not fixed: title sits right after
the state tag — the operator's primary identifier for the row — whenever
that natural order fits the terminal's available width. Only when the
natural-order row would actually be clipped does it fall back to
blocker/reason/heartbeat before title, so the operator-critical blocker
signal survives truncation instead of the title eating the row's budget
first (issue #1256, following up on issue #858).

## Drill-in

**Drill-in** (Enter) opens the highlighted pick's rendered transcript:
assistant turns and tool calls, readable, spanning the whole Dispatch — initial
run, every fix pass, and conflict-resolve — concatenated in order with a
`=== pass: ... ===` boundary between them, since the Dispatch (claim to verdict)
is the domain object and per-pass logs are storage detail. The pane is a
one-shot load: it renders once on open and no keystroke refreshes it in place —
a running Dispatch's growing log is not live-tailed. Close (`x`/Esc) and reopen
(Enter) to reload with fresh content. `t`/`toggle` switches to the raw
byte-exact log for debugging the harness itself, and back; `x`/`close` returns
to the backlog/queue. Rendering is a per-Driver strategy — a Driver with no
configured strategy, or an issue with no Dispatch logs on disk yet, surfaces an
error in place of the transcript rather than a blank pane.

The rendered view strips ANSI/control sequences before it ever reaches the
terminal, since the underlying log is untrusted model/tool output; the raw view
intentionally does not, so a Dispatch log carrying crafted escape sequences can
still move the cursor, clear the screen, or rewrite the terminal title when
toggled to — treat `t`/`toggle` as a trusted-log-only debugging tool, not
something to point at an unreviewed transcript.

## Terminate

**Terminate** (`k <num>` / `kill <num>` / `terminate <num>`) ends a live
Dispatch by hand (ADR 0024) — valid anywhere from claim to verdict: a running
Box, the CI watch, a fix pass, or the merge gate. It always requires an
explicit `y`/`N` confirm before acting; anything but `y`/`yes` cancels with no
effect. Once confirmed, Terminate reaps any running Box, abandons the settle
wherever it stands, and returns the issue to `Dispatchable` — never `Failed`,
since the operator decided and there is nothing to triage, and never a new
tracker state. It never un-lands work: no branch deletion, no PR close, no
force-push. The ending is recorded outside the state machine — a terminal
line appended to the Box log, and a comment on the issue naming the terminate
and linking any dangling branch/PR — so a terminated Dispatch with an open PR
is never silently orphaned. Re-picking a terminated issue later dispatches a
fresh Box and, through the existing settle adoption path, picks up the
dangling PR instead of duplicating it — terminate-then-repick is a clean
reclaim loop, not a collision.

## Stale image

When the freshness probe finds the loaded image would be rebuilt against the
current base branch tip, the Console prints
`!! image stale: <reason> — new launches held; press [b] to rebuild` and
holds every new launch — a queued pick stays at `queued` instead of claiming.
A Box already running rides out the stale window on its original image
untouched; staleness only gates a slot *refill*, never an in-flight Dispatch.

`b`/`build`/`rebuild` fires the rebuild without leaving the session or needing
a confirm: it checks out the base branch, pulls it, and re-realizes the image
in the background while the session stays responsive, with
`==> rebuilding image...` shown until it finishes. That checkout runs on the
operator's own working directory — it refuses to run when the directory is on
some other branch with a dirty working tree (uncommitted changes or untracked
files), since a plain `git checkout` only blocks on a *conflicting* file and
would otherwise carry a non-conflicting uncommitted change or untracked file
onto the base branch in silence. Outside that case (already on the base
branch, or any branch with a clean tree) the checkout is a safe no-op or a
plain branch switch, so it proceeds. A successful rebuild clears the
banner and resumes every held pick exactly where it queued — no re-pick needed.
A failed rebuild — including a refused checkout — prints
`!! rebuild failed: <reason>` and leaves launches held, so the operator can
retry `b` once the underlying problem (a dirty working tree on the wrong
branch, a merge conflict on pull, a broken derivation) is fixed.

## Quit

**Quit** (`q`/`quit`): with no live Dispatches, quit exits immediately — no
dialog. With one or more live Dispatches, it instead offers a choice:
`drain (d, default) / terminate-all (t) / stay (s)`. Drain launches nothing
new — every queued-but-unlaunched pick is dropped (it was already
`Dispatchable` on the tracker, so dropping is a pure session-queue edit with
no tracker call, exactly like Unpick) — and Run doesn't return until every
still-running Dispatch settles on its own. Terminate-all additionally applies
Terminate (above) to every live Dispatch before exiting. Anything else,
including a bare "stay", cancels the pending quit and keeps the session running.

## Orphan recovery

A hard death — a crash, a dropped SSH session — leaves its containers running
with nothing left to track them. On its next start, the Console detects any
sandbox still running under the deterministic `agent-issue-<N>` naming scheme
and offers each one to the operator by number (`[y/N]`); `y`/`yes` adopts it
through the existing recover path (the same adoption `spindrift recover <n>`
and a re-pick after Terminate both use), so an ungraceful end is a speed bump,
not a cleanup chore. Declining leaves that orphan, and every one after it,
untouched.
