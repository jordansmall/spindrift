# The interactive Console

`spindrift console` opens the interactive Console (ADR 0023, 0025, 0030): an
in-terminal loop that lists every open issue from the Issue Tracker under
columns headed `issue`/`title`/`labels` (the Backlog) or
`issue`/`title`/`state`/`age` (a work Section's queue) — oldest-first per
dispatch order, grouped into Sections (Backlog, Running, Held, Settled,
Failed) — and lets you Pick issues to launch as Dispatches.

```sh
spindrift console
```

There is no command line to type into. Every action is a single keypress —
vim-style — dispatched against whichever of three views has focus: the main
list, a ticket's detail modal, or a live-tail sidebar. `?` (from the main
list) toggles a help overlay listing every binding across every view.

## Main list

| key | effect |
|-----|--------|
| `j`/`k`, `down`/`up` | move cursor within the active Section |
| `G` | jump to the active Section's last row, scrolling it into view |
| `gg` | jump to the active Section's first row (`g` arms a pending leader, awaiting a trailing `g`) |
| `H`/`L` | switch to the previous/next Section (from the sidebar, also closes it) |
| `1`-`5` | jump straight to a Section (Backlog, Running, Held, Settled, Failed) |
| `ctrl+f`/`ctrl+b`, `pgup`/`pgdown` | jump a full page of the active Section's live rendered rows without moving the cursor; the page size tracks terminal resizes |
| `ctrl+d`/`ctrl+u` | jump a half page of the active Section's live rendered rows without moving the cursor — half of the `ctrl+f`/`ctrl+b` page above |
| `/` | filter the Backlog by label substring |
| `enter` | apply filter (while filter-editing); otherwise: open the highlighted row's ticket detail (Backlog Section), or open the highlighted pick's live-tail sidebar (a work Section, only when it has run) |
| `h`/`l`, `left`/`right` | move focus between the list and the sidebar (while a sidebar is open) |
| `x`/`esc` | close a docked sidebar, if one is open |
| `p` | pick the highlighted Backlog row (launch button) |
| `P` | pick all ready (bulk pick-all-ready gesture) |
| `r` | research the highlighted Backlog row (advise-only pick) |
| `R` | refresh the backlog |
| `u` | unpick the highlighted queued pick |
| `X` | terminate the highlighted live Dispatch (confirm `y`/`N`, `q`/`ctrl+c` decline and quit) |
| `A` | adopt the highlighted orphan-flagged Backlog row (a running sandbox this session didn't launch); reports why and changes nothing without a non-draft open PR |
| `+`/`-` | raise/lower the live parallelism cap |
| `b` | rebuild the stale image in-session |
| `o` | open the rebuild output pane (once a rebuild has run); scrolls with the same keys as the sidebar below |
| `q`/`ctrl+c` | quit |
| `?` | toggle the help overlay |

If a `.dogfood.pid` file is present at startup — a headless loop
(`dogfood.sh`) already draining the same queue — the Console prints an
informational notice and keeps going; it never blocks or refuses to start,
and the two are safe to run side by side (claims are atomic label swaps).

## Ticket detail modal

`enter` on a highlighted Backlog row opens its ticket detail modal: the
issue's full body plus its Blocked-by and Blocks lists, each resolved
directly from the issue's own dependency edge. On a `local` tracker the
Blocks list is always empty — only GitHub and Jira expose a native,
bidirectional blocked/blocking relationship (`forge.BlockersLister`); a
`local` issue's only blocker concept is one-directional body-text parsing,
with no reverse edge to query.

| key | effect |
|-----|--------|
| `esc` | close the ticket detail modal |
| `j`/`k`, `up`/`down` | scroll the modal's body |
| `ctrl+f`/`ctrl+b`, `pgdown`/`pgup` | page the modal's body |
| `ctrl+d`/`ctrl+u` | scroll the modal's body a half page |
| `G` | jump to the modal's last page |
| `gg` | jump to the modal's first page |
| `p` | pick the displayed issue as a work-kind dispatch (same launch button as the Backlog's `p`), then close the modal |
| `r` | pick the displayed issue as a research dispatch (advise-only: posts one verdict comment, never opens a branch/PR), then close the modal |
| `u` | unpick the displayed issue's queued pick, if any |

## Live-tail sidebar

`enter` on a highlighted work-Section row that has actually run opens its
live-tail sidebar: an activity feed by default, with the Dispatch's rendered
transcript and raw JSONL log one keystroke away. `enter` on an
orphan-flagged Backlog row also opens the sidebar (instead of the ticket
detail modal), showing a "no local logs for this dispatch" notice until logs
appear on disk.

| key | effect |
|-----|--------|
| `t` | cycle the sidebar's activity feed -> transcript -> raw JSONL -> activity feed (while the sidebar has focus) |
| `z` | toggle the sidebar's fullscreen zoom (while it has focus) |
| `h`/`left` | move focus back to the list (while the sidebar has focus and isn't zoomed — a zoomed sidebar has no list to return to) |
| `l`/`right` | move focus to the sidebar (from the list, while a sidebar is open) |
| `x`/`esc` | close the sidebar (while it has focus) |
| `j`/`k`, `ctrl+f`/`ctrl+b`, `pgup`/`pgdown` | scroll the sidebar (while it has focus); its page jump is fixed, unlike the ticket detail modal's live-viewport-derived one; scrolling up detaches the running Activity feed's live follow |
| `ctrl+d`/`ctrl+u` | scroll the sidebar a half page (while it has focus) |
| `G`/`end` | re-attach follow and jump to the sidebar's bottom |
| `gg` | detach follow and jump to the sidebar's top |
| `H`/`L` | switch to the previous/next Section (also closes the sidebar) |

The activity feed and rendered transcript strip ANSI/control sequences
before they ever reach the terminal, since the underlying log is untrusted
model/tool output. The raw JSONL view intentionally does not, so a Dispatch
log carrying crafted escape sequences can still move the cursor, clear the
screen, or rewrite the terminal title once `t` cycles to it — treat the raw
view as a trusted-log-only debugging tool, not something to point at an
unreviewed transcript.

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
`u` still works on a held row exactly as it does on a queued one, so the
operator decides whether to wait or give up on it.

**Pick all ready** (`P`) picks exactly the issues currently `Dispatchable`
on the tracker, in one snapshot query — an explicit action, never standing
discovery: an issue that becomes `Dispatchable` after `P` returns is not
picked until the operator asks again. Each issue queues through the same
Pick path a single `p` uses.

**Unpick** removes a queued-but-unlaunched pick — including a held one — from
the session with zero Issue Tracker calls — it only ever un-does the
in-session queue entry, never the durable promotion a pick already recorded.
`u` works the same way from the main list (on the highlighted row) and from
the ticket detail modal (on the displayed issue).

`p`/`P` queue a `work` Dispatch; `r` queues a `research` Dispatch instead —
advise-only, posting a single verdict comment rather than opening a
branch/PR — for the highlighted Backlog row. Both kinds ride the same Pick
record and queue machinery, distinguished only by the kind field.

## Live parallelism cap

`+`/`-` raise or lower the session's parallelism cap by one, and the current
`cap: <live>/<cap>` is always visible above the queue. Raising takes effect
immediately — a held or queued pick launches into the freed slot right away,
without waiting for a running Dispatch to settle or for the background poll.
Lowering never terminates anything: it only gates new launches until the live
count sinks under the new cap on its own, as running Dispatches settle — `X`
remains the only way a running Dispatch dies by hand. `MAX_JOBS` gets no
Console control — it caps headless wave size, and in a picks-only session
the operator is already the cap.

## Backlog freshness

The Console keeps the backlog fresh without spending the shared rate-limit
window: `R` re-queries on demand, the backlog auto-refreshes whenever the
session itself writes to the tracker — a claim, a settle, or a promotion —
and a slow background poll re-queries on a fixed cadence (60–120s) even on an
otherwise idle session. Nothing refreshes faster than that poll; only the
session's own writes and the operator's own `R` trigger a refresh in between.

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

## Terminate

**Terminate** (`X`, on the main list's highlighted live Dispatch) ends a
live Dispatch by hand (ADR 0024) — valid anywhere from claim to verdict: a
running Box, the CI watch, a fix pass, or the merge gate. It always requires
an explicit `y`/`N` confirm before acting; anything but `y`/`yes` cancels
with no effect, and `q`/`ctrl+c` declines and arms the quit confirm instead.
Once confirmed, Terminate reaps any running Box, abandons the settle
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

`b` fires the rebuild without leaving the session or needing a confirm: it
checks out the base branch, pulls it, and re-realizes the image in the
background while the session stays responsive, with `==> rebuilding
image...` shown until it finishes. That checkout runs on the operator's own
working directory — it refuses to run when the directory is on some other
branch with a dirty working tree (uncommitted changes or untracked files),
since a plain `git checkout` only blocks on a *conflicting* file and would
otherwise carry a non-conflicting uncommitted change or untracked file onto
the base branch in silence. Outside that case (already on the base branch,
or any branch with a clean tree) the checkout is a safe no-op or a plain
branch switch, so it proceeds. A successful rebuild clears the banner and
resumes every held pick exactly where it queued — no re-pick needed. A
failed rebuild — including a refused checkout — prints `!! rebuild failed:
<reason>` and leaves launches held, so the operator can retry `b` once the
underlying problem (a dirty working tree on the wrong branch, a merge
conflict on pull, a broken derivation) is fixed. `o` opens a pane showing the
rebuild's own output once one has run.

## Quit

**Quit** (`q`/`ctrl+c`): with no live Dispatches, quit exits immediately —
no dialog. With one or more live Dispatches, it instead offers a choice:
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
and flags its Backlog row as an orphan; nothing is asked at startup, and the
Console never blocks on it. `A` on a highlighted orphan-flagged row adopts it
through the existing recover path (the same adoption `spindrift recover <n>`
and a re-pick after Terminate both use), reporting why and changing nothing
if the orphan has no non-draft open PR to adopt. `enter` on an orphan row
opens its live-tail sidebar instead of the ticket detail modal, so an
ungraceful end is a speed bump, not a cleanup chore.
