// Run drives the console as a real full-screen Bubble Tea program (issue
// #784) — the sole entry point; the earlier bufio.Scanner line-command loop
// is retired. teaModel is a thin adapter: tea.Model.Update translates
// tea.KeyMsg and the two async signals (background poll, launch-refresh)
// into the same console Msg values Update already handles; tea.Model.View
// delegates straight to the pure View. Unpick/pick-all-ready/Terminate/
// Resize/Rebuild act on the cursor's highlighted row (issue #785); the
// live-tail sidebar is wired too (issue #786, replaced by #1501's docked
// sidebar). Enter is context-sensitive: Pick (via the PickIssue adapter) on a
// focused Backlog row, open the highlighted work row's sidebar when it has a
// Transcript (issue #845) — the old "d"/backlog-Enter drill binding is
// retired in favour of this split.
package console

import (
	"fmt"
	"io"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fsnotify/fsnotify"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/forge"
)

// fixedPaneScrollDelta is how many lines pgup/pgdown move the drill-in
// transcript's scroll offset — j/k and the arrows move one line at a time
// (issue #786). Fixed, unlike the body's own page jump (sectionPageSize),
// which derives from the live viewport height instead (issue #1037).
const fixedPaneScrollDelta = 10

// teaModel is the Bubble Tea adapter around the pure Model: it carries the
// I/O seams (tracker, pwd, launch) Update itself never touches, and
// translates tea.Msg values into console Msg values before calling Update.
type teaModel struct {
	m            Model
	tracker      forge.IssueTracker
	pwd          string
	launch       *Launcher
	pollInterval time.Duration
	// heartbeats caches each running pick's last-parsed heartbeat line so
	// refreshPickDecorations' per-Update refresh skips the ReadFile+reparse
	// when a pick's latest pass log is unchanged since the last call (issue
	// #731) — a pointer so it survives Update's value-receiver copies of
	// teaModel across the session's whole lifetime.
	heartbeats *HeartbeatCache
	// sidebarActivity caches the open sidebar's own last-refreshed Activity
	// feed, the same skip-when-unchanged optimization as heartbeats — every
	// tea.Msg re-syncs the selected running Dispatch's feed (issue #1502,
	// ADR 0030's "piggybacking the existing per-Msg sync tick"), scoped to
	// that one Dispatch so I/O stays bounded even with many running.
	sidebarActivity *SidebarActivityCache
	// sidebarTranscript caches the open sidebar's own last-refreshed
	// Transcript render, the Transcript's own analogue of sidebarActivity —
	// refreshPickDecorations re-derives it via the same per-Msg refresh path
	// while ShowTranscript is active, so the Transcript view live-tails a
	// running Dispatch instead of staying frozen at open time (issue #1736).
	sidebarTranscript *SidebarTranscriptCache
	// sidebarTickArmed tracks whether a sidebarActivityTick is currently
	// in flight, so Update arms at most one at a time instead of stacking a
	// second while the first is still pending (issue #1735).
	sidebarTickArmed bool
	// sidebarTickGen increments every time Update arms a sidebarActivityTick
	// — on the first open and on every one of the tick's own re-arms alike —
	// the value each armed tea.Tick's Msg carries, so a stale timer left
	// over from a close-then-reopen within one tick interval
	// (sidebarActivityTickMsg's own doc comment) is recognized by its
	// now-superseded generation and dropped instead of re-arming a second,
	// permanently duplicate tick chain.
	sidebarTickGen uint64
	// toastGen increments every time Update arms a fresh toastDismissTick —
	// on every pick-transition toast a QueueSnapshotMsg sets (issue #1830,
	// Model.Toast). Mirrors sidebarTickGen's own doc comment: a toast a newer
	// one already replaced leaves its own dismiss timer still in flight (a
	// tea.Tick can't be cancelled once scheduled), so the fired
	// toastDismissTickMsg carries the generation that armed it and Update
	// drops it as stale instead of clearing the newer toast.
	toastGen uint64
	// watcher fires a logWriteMsg on a write to any path in watchedPaths — a
	// running pick's current log file — so Update's post-switch
	// refreshPickDecorations call runs the incremental heartbeat refresh
	// within moments of new bytes landing, instead of waiting for the next
	// pollTickMsg (issue #1748). nil for a launch-less session, or when
	// fsnotify.NewWatcher failed to acquire a platform watch handle — either
	// way, refreshPickDecorations still runs on every Msg, so the console
	// stays correct, just back to the slower per-Msg/poll cadence.
	watcher *fsnotify.Watcher
	// watchedPaths mirrors watcher's own watch set (fsnotify exposes no
	// cheap "is this path watched" query cheaper than WatchList's full
	// slice) so reconcileWatches can diff against it in place — a map, a
	// reference type, so every value-receiver copy of teaModel still shares
	// the one instance newTeaModel created.
	watchedPaths map[string]struct{}
	// done is closed exactly once, at the Quitting choke point below, to
	// unblock waitRefreshSignal's goroutine — bubbletea can't cancel a Cmd
	// goroutine itself (issue #823), so the closure has to select on this
	// instead of blocking on Launcher.Refreshes() forever. A chan is a
	// reference type, so every value-receiver copy of teaModel still shares
	// the one instance newTeaModel created.
	done chan struct{}
}

// newTeaModel builds the tea layer's starting state: the dogfood-competition
// notice is checked synchronously (a cheap pid-file read plus signal-0
// probe, matching the pre-#784 Run's own startup check) — the initial
// backlog load, background poll, and launch-refresh listener all start as
// Cmds from Init instead.
func newTeaModel(tracker forge.IssueTracker, pwd string, launch *Launcher) teaModel {
	m := NewModel()
	m = Update(m, DogfoodNotice(pwd))
	interval := defaultPollInterval
	var watcher *fsnotify.Watcher
	if launch != nil {
		interval = launch.PollInterval()
		if w, err := fsnotify.NewWatcher(); err == nil {
			watcher = w
		}
	}
	return teaModel{m: m, tracker: tracker, pwd: pwd, launch: launch, pollInterval: interval, heartbeats: NewHeartbeatCache(), sidebarActivity: NewSidebarActivityCache(), sidebarTranscript: NewSidebarTranscriptCache(), watcher: watcher, watchedPaths: make(map[string]struct{}), done: make(chan struct{})}
}

// Run drives the console's full-screen Bubble Tea program to completion —
// the tea program is the only entry (issue #784). launch is nil for a
// launch-less session; production wires a real Launcher.
func Run(tracker forge.IssueTracker, pwd string, in io.Reader, out io.Writer, launch *Launcher) error {
	p := tea.NewProgram(newTeaModel(tracker, pwd, launch), tea.WithInput(in), tea.WithOutput(out), tea.WithAltScreen())
	_, err := p.Run()
	if launch != nil {
		launch.Wait()
	}
	return err
}

// pollTickMsg is the tea layer's background-poll tick (#647 AC5) — re-armed
// on every arrival so the poll continues for the program's whole lifetime.
type pollTickMsg struct{}

// refreshSignalMsg is the tea layer's translation of Launcher.Refreshes()
// firing — the session's own tracker write (a claim, a settle, a promotion)
// asking for an out-of-band refresh (#647 AC4). picks carries the queue
// snapshot Launcher.signalRefresh recorded at the moment it fired
// (TakePendingSnapshot), hasPicks distinguishing "nothing pending" from a
// genuinely empty queue — the tea side lands it without ever pulling Queue
// itself (issue #1542).
type refreshSignalMsg struct {
	picks    []Pick
	hasPicks bool
}

// toastDismissDelay is how long a pick-transition toast (Model.Toast, issue
// #1830) stays visible before it auto-dismisses — long enough to read a
// short "#NN started: title" line, short enough that it never lingers into
// the next transition's own toast.
const toastDismissDelay = 4 * time.Second

// toastDismissTickMsg is the tea layer's one-shot auto-dismiss signal for a
// pick-transition toast. gen pins it to the teaModel.toastGen that armed it —
// see toastGen's own doc comment for why a stale straggler must be dropped
// rather than clearing a newer toast.
type toastDismissTickMsg struct{ gen uint64 }

// toastDismissTick arms one toastDismissTickMsg carrying gen.
func toastDismissTick(gen uint64) tea.Cmd {
	return tea.Tick(toastDismissDelay, func(time.Time) tea.Msg { return toastDismissTickMsg{gen: gen} })
}

// sidebarActivityTickInterval is how often the docked sidebar's own live-tail
// tick fires — independent of both keypresses and the 90s pollTick backlog
// cadence, so a running Dispatch's open sidebar advances while the operator
// sits and watches, including while zoomed (issue #1735).
const sidebarActivityTickInterval = time.Second

// sidebarActivityTickMsg is the tea layer's dedicated live-tail signal.
// Landing it is enough to reach Update's post-switch refreshPickDecorations
// call, the same refresh path a keypress or the 90s pollTick already drives.
// gen pins it to the teaModel.sidebarTickGen that armed it: tea.Tick can't be
// cancelled once scheduled, so a close-then-reopen within one tick interval
// can leave a stale timer in flight alongside a freshly armed one — gen lets
// Update recognize and drop that stale straggler instead of it re-arming a
// second, permanently duplicate tick chain (review finding on issue #1735).
type sidebarActivityTickMsg struct{ gen uint64 }

// sidebarActivityTick arms one sidebarActivityTickMsg carrying gen.
func sidebarActivityTick(gen uint64) tea.Cmd {
	return tea.Tick(sidebarActivityTickInterval, func(time.Time) tea.Msg { return sidebarActivityTickMsg{gen: gen} })
}

// sidebarActivityLive reports whether the docked sidebar is open on a
// Dispatch whose Activity feed can still change — the same gate
// refreshPickDecorations applies before refreshing it (isRunningNumber or an
// orphan row, issue #1502/#1621) — so the live-tail tick arms and disarms in
// lockstep with the refresh it exists to drive.
func sidebarActivityLive(m Model) bool {
	return m.Sidebar != nil && (isRunningNumber(m.Picks, m.Sidebar.Number) || m.IsOrphan(m.Sidebar.Number))
}

// gChordTimeout is how long a lone "g" waits for a trailing "g" before the
// leader window cancels — long enough that a deliberate two-key "gg" always
// lands within it, short enough that a lone "g" still reads as responsive
// (issue #1628).
const gChordTimeout = 200 * time.Millisecond

// gChordTimeoutMsg is the tea layer's signal that "g"'s leader window
// elapsed with no trailing "g" — cancels the still-pending leader.
type gChordTimeoutMsg struct{}

// gChordTick arms the "gg" leader-window timeout.
func gChordTick() tea.Cmd {
	return tea.Tick(gChordTimeout, func(time.Time) tea.Msg { return gChordTimeoutMsg{} })
}

// resolvePendingG resolves an armed "gg" leader against msg, factored out of
// the four PendingG-checking key handlers (handleListKey,
// handleRebuildOutputKey, handleDetailModalKey, handleSidebarKey — issue
// #1802). A no-op when PendingG isn't set. Otherwise it clears the leader
// and, if msg is the second "g", applies onFirst — the pane's own
// jump-to-first transition — and reports the key as consumed. Any other key
// still clears the leader but is reported unconsumed: that key's own
// binding still applies, so the caller falls through rather than returning
// (issue #1628 AC).
func resolvePendingG(m Model, msg tea.KeyMsg, onFirst func(Model) Model) (Model, bool) {
	if !m.PendingG {
		return m, false
	}
	m = Update(m, GResolvedMsg{})
	if msg.String() == "g" {
		return onFirst(m), true
	}
	return m, false
}

// armPendingG arms the "gg" leader window on m, factored out of the four
// key handlers' identical "g" case (issue #1802).
func armPendingG(m Model) (Model, tea.Cmd) {
	return Update(m, GPendingMsg{}), gChordTick()
}

// Init starts the initial backlog load and both async signal sources
// (background poll, launch-refresh) as Cmds — none of them block the
// program's own startup.
func (t teaModel) Init() tea.Cmd {
	cmds := []tea.Cmd{refreshCmd(t.tracker), pollTick(t.pollInterval)}
	if t.launch != nil {
		cmds = append(cmds, initialQueueSyncCmd(t.launch), waitRefreshSignal(t.launch, t.done), orphanDetectCmd(t.launch))
		if t.watcher != nil {
			cmds = append(cmds, waitLogWrite(t.watcher, t.done))
		}
	}
	return tea.Batch(cmds...)
}

// initialQueueSyncCmd bootstraps Model.Picks from launch's queue once, at
// startup — the only outside read of the private queue's full contents past
// construction, through Launcher's own exported Snapshot accessor rather
// than the queue directly (issue #1542). Every later transition reaches the
// Model synchronously through Pick/Unpick/TerminateAsync's own return value
// or asynchronously through a pushed refreshSignalMsg — there is no
// per-message pull to keep Model.Picks caught up with a queue that started
// non-empty otherwise.
func initialQueueSyncCmd(launch *Launcher) tea.Cmd {
	return func() tea.Msg {
		return QueueSnapshotMsg{Picks: launch.Snapshot()}
	}
}

// Update is the tea layer's whole adapter surface: it translates every
// Bubble Tea message (key presses, resizes) and internal signal into
// console Msg values already handled by the pure Update, then re-syncs the
// launcher's live Queue/stale state onto the Model exactly as the pre-#784
// Run loop did on every render. "Internal signal" spans two shapes: results
// of a completed async load (a backlog refresh, a drill-in fetch), which
// carry a payload, and reactive notifications, which carry none (poll
// ticks, refresh signals, "gg" leader timeouts).
func (t teaModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	prevToast := t.m.Toast
	switch msg := msg.(type) {
	case tea.KeyMsg:
		t, cmd = t.handleKey(msg)
	case tea.WindowSizeMsg:
		t.m = Update(t.m, SizeChangedMsg{Width: msg.Width, Height: msg.Height})
	case IssuesLoadedMsg: // async-load result, not a reactive signal
		t.m = Update(t.m, msg)
	case SidebarLoadedMsg: // async-load result, not a reactive signal
		t.m = Update(t.m, msg)
	case DetailModalLoadedMsg: // async-load result, not a reactive signal
		t.m = Update(t.m, msg)
	case OrphanRecoveryMsg:
		t.m = Update(t.m, msg)
	case OrphanDetectedMsg:
		t.m = Update(t.m, msg)
	case OrphanAdoptedMsg:
		t.m = Update(t.m, msg)
	case QueueSnapshotMsg: // startup bootstrap, or a launcher-pushed transition
		t.m = Update(t.m, msg)
	case pollTickMsg:
		if t.launch != nil {
			t.launch.tryLaunch(t.tracker, t.pwd)
		}
		cmd = tea.Batch(refreshCmd(t.tracker), pollTick(t.pollInterval))
	case logWriteMsg:
		cmd = waitLogWrite(t.watcher, t.done)
	case refreshSignalMsg:
		if msg.hasPicks {
			t.m = Update(t.m, QueueSnapshotMsg{Picks: msg.picks})
		}
		cmd = tea.Batch(refreshCmd(t.tracker), waitRefreshSignal(t.launch, t.done))
	case gChordTimeoutMsg:
		if t.m.PendingG {
			t.m = Update(t.m, GResolvedMsg{})
		}
	case sidebarActivityTickMsg:
		if msg.gen != t.sidebarTickGen {
			// A stale straggler from a close-then-reopen within one tick
			// interval (sidebarActivityTickMsg's own doc comment) — a fresh
			// tick already carries the current generation, so this one is
			// dropped rather than re-armed into a second, permanently
			// duplicate tick chain.
			return t, nil
		}
		// This fire already consumed the Cmd that scheduled it — clear the
		// flag so the generic re-arm check below issues a fresh one instead
		// of reading it as "still in flight" and skipping re-arm entirely.
		t.sidebarTickArmed = false
	case toastDismissTickMsg:
		if msg.gen != t.toastGen {
			// A stale straggler from a toast a newer one already replaced
			// (toastGen's own doc comment) — dropped rather than clearing
			// whatever toast is current now.
			return t, nil
		}
		t.m = Update(t.m, ToastDismissedMsg{})
	}

	t.m = refreshPickDecorations(t.m, t.launch, t.pwd, t.heartbeats, t.sidebarActivity, t.sidebarTranscript)
	t = t.reconcileWatches()
	t.m = syncStale(t.m, t.launch)
	if sidebarActivityLive(t.m) {
		if !t.sidebarTickArmed {
			t.sidebarTickGen++
			cmd = tea.Batch(cmd, sidebarActivityTick(t.sidebarTickGen))
			t.sidebarTickArmed = true
		}
	} else {
		t.sidebarTickArmed = false
	}
	if t.m.Toast != "" && t.m.Toast != prevToast {
		// A fresh or replaced toast — arm its own dismiss timer under a new
		// generation so a still-in-flight timer from whatever toast this one
		// replaced is recognized as stale and dropped (toastGen's own doc
		// comment) instead of clearing this one early.
		t.toastGen++
		cmd = tea.Batch(cmd, toastDismissTick(t.toastGen))
	}
	if t.m.Quitting {
		select {
		case <-t.done:
			// Already closed by an earlier Update call on this program.
			// This check-then-close is race-free only because bubbletea
			// invokes Update serially from its single event-loop goroutine
			// (Program.eventLoop in charmbracelet/bubbletea) — never
			// concurrently.
		default:
			close(t.done)
			if t.watcher != nil {
				_ = t.watcher.Close()
			}
		}
		return t, tea.Quit
	}
	return t, cmd
}

// View delegates straight to the pure View — the tea layer adds no
// rendering of its own; Bubble Tea's alt-screen renderer paints whatever
// string comes back across the whole terminal.
func (t teaModel) View() string {
	return View(t.m)
}

// pendingGJump returns the mode-specific "jump to first" transition a
// trailing "g" resolves a pending leader to, or nil for a mode with no "gg"
// leader of its own — the shape resolvePendingG's onFirst parameter expects,
// one per pane since each keeps its own notion of "first" (issue #1802,
// #1790).
func pendingGJump(mode Mode) func(Model) Model {
	switch mode {
	case ModeList:
		return func(m Model) Model { return Update(m, CursorJumpToFirstMsg{}) }
	case ModeRebuildOutput:
		return func(m Model) Model { return Update(m, RebuildOutputJumpToFirstMsg{}) }
	case ModeDetailModal:
		return func(m Model) Model { return Update(m, DetailModalJumpToFirstMsg{}) }
	case ModeSidebar:
		return func(m Model) Model { return Update(m, SidebarJumpToBeginningMsg{}) }
	}
	return nil
}

// dispatchDefault applies whatever a mode's retired handler did when no key
// matched any of its switch cases. Most modes simply did nothing (the
// switch's implicit fallthrough); two had a real default of their own:
// ModeTerminateConfirm declines the terminate; ModeQuitConfirm declines the
// quit (issue #1790).
func (t teaModel) dispatchDefault(mode Mode) (teaModel, tea.Cmd) {
	switch mode {
	case ModeTerminateConfirm:
		t.m = Update(t.m, TerminateCancelledMsg{})
	case ModeQuitConfirm:
		t.m = Update(t.m, QuitCancelledMsg{})
	}
	return t, nil
}

// dispatchKey resolves one keypress against mode: mode-specific pre-dispatch
// state (a pending "gg" leader, ModeList's queued-enter notice) runs first,
// exactly as it did inline at the top of each retired handler, then the
// keymap entry naming (mode, key) is looked up and its Action invoked — or,
// when no entry matches, dispatchDefault's mode-specific fallback. Every
// handleXKey method below is a thin wrapper around this one function,
// pinning mode to its own Mode rather than re-deriving it from
// t.m.ActiveMode() — handleKey's own routing switch already made that choice
// (issue #1790).
func (t teaModel) dispatchKey(mode Mode, msg tea.KeyMsg) (teaModel, tea.Cmd) {
	if onFirst := pendingGJump(mode); onFirst != nil {
		m, consumed := resolvePendingG(t.m, msg, onFirst)
		t.m = m
		if consumed {
			return t, nil
		}
		// Any other key cancels the leader without consuming it — that key's
		// own meaning still applies below (issue #1628 AC).
	}
	if mode == ModeList && t.m.QueueEnterNotice != "" {
		t.m = Update(t.m, QueueEnterNoticeClearedMsg{})
	}
	if mode == ModeList && t.m.Toast != "" {
		t.m = Update(t.m, ToastDismissedMsg{})
	}

	key := msg.String()
	if mode == ModeFilterEdit {
		key = filterEditKeyName(msg)
	}
	if b := binding(mode, key); b != nil && b.Action != nil {
		return b.Action(t, msg, mode)
	}
	return t.dispatchDefault(mode)
}

// handleKey translates one keypress into a console Msg and applies it,
// dispatching on whichever Mode Model.ActiveMode reports owns the keyboard
// right now. modePrecedence (model.go) is the flat, ordered data this used
// to be an if-cascade over — the five router functions below (plus
// handleListKey for the ModeList default) are what the cascade's branches
// collapsed into (issue #1543).
func (t teaModel) handleKey(msg tea.KeyMsg) (teaModel, tea.Cmd) {
	switch t.m.ActiveMode() {
	case ModeDetailModal:
		var cmd tea.Cmd
		t.m, cmd = t.handleDetailModalKey(msg)
		return t, cmd
	case ModeSidebar:
		var cmd tea.Cmd
		t.m, cmd = t.handleSidebarKey(msg)
		return t, cmd
	case ModeRebuildOutput:
		var cmd tea.Cmd
		t.m, cmd = t.handleRebuildOutputKey(msg)
		return t, cmd
	case ModeHelp:
		return t.handleHelpKey(msg), nil
	case ModeFilterEdit:
		return t.handleFilterKey(msg), nil
	case ModeTerminateConfirm:
		return t.handleTerminateConfirmKey(msg), nil
	case ModeQuitConfirm:
		return t.handleQuitConfirmKey(msg), nil
	default: // ModeList
		return t.handleListKey(msg)
	}
}

// handleHelpKey routes one keypress while the help overlay is open, through
// dispatchKey pinned to ModeHelp — see dispatchKey and the keymap's ModeHelp
// entries ("?" and "esc" both toggle it closed; everything else, including a
// quit keystroke, has no entry and so falls to dispatchDefault's no-op)
// (issue #784, #1543, #1790).
func (t teaModel) handleHelpKey(msg tea.KeyMsg) teaModel {
	t, _ = t.dispatchKey(ModeHelp, msg)
	return t
}

// handleListKey routes one keypress against the plain backlog/queue body —
// ModeList, modePrecedence's last resort — through dispatchKey pinned to
// ModeList. PendingG's "gg" leader and QueueEnterNotice's clear-on-any-key
// both still run first, inside dispatchKey's own pre-steps, exactly as they
// did before this and the mode-enum refactor: neither is a rival claimant to
// keyboard ownership (Mode's own doc comment), so neither earns a case in
// handleKey's switch above — they layer on top of ModeList instead (issue
// #1543, #1790).
func (t teaModel) handleListKey(msg tea.KeyMsg) (teaModel, tea.Cmd) {
	return t.dispatchKey(ModeList, msg)
}

// handleRebuildOutputKey routes one keypress while ModeRebuildOutput owns
// the keyboard, through dispatchKey pinned to ModeRebuildOutput — see
// dispatchKey (whose pendingGJump pre-step covers this pane's own "gg"
// leader, reusing Model.PendingG and resolvePendingG/armPendingG rather than
// a second, pane-scoped leader, issue #1630 AC3) and the keymap's
// ModeRebuildOutput and quit entries (issue #1790).
func (t teaModel) handleRebuildOutputKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	t, cmd := t.dispatchKey(ModeRebuildOutput, msg)
	return t.m, cmd
}

// handleDetailModalKey routes one keypress while ModeDetailModal owns the
// keyboard (the ticket detail modal is open — modeActive's ModeDetailModal
// case), through dispatchKey pinned to ModeDetailModal — see dispatchKey and
// the keymap's ModeDetailModal and quit entries (issue #1632, #1795, #1790).
func (t teaModel) handleDetailModalKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	t, cmd := t.dispatchKey(ModeDetailModal, msg)
	return t.m, cmd
}

// openDetailModal opens iss's fullscreen ticket detail modal: instantly,
// with the number/title/labels the highlighted Backlog row already has in
// hand, then kicks off the async body/blocker fetch (openDetailModalCmd) —
// or, when iss is already in Model.DetailCache, applies the cached detail
// synchronously with no fetch at all, so reopening a ticket already visited
// this session is instant (issue #1632).
func (t teaModel) openDetailModal(iss forge.Issue) (teaModel, tea.Cmd) {
	t.m = Update(t.m, DetailModalOpenMsg{Number: iss.Number, Title: iss.Title, Labels: iss.Labels})
	if cached, ok := t.m.DetailCache[iss.Number]; ok {
		t.m = Update(t.m, DetailModalLoadedMsg{Number: iss.Number, Body: cached.Body, BlockedBy: cached.BlockedBy, Blocks: cached.Blocks})
		return t, nil
	}
	return t, openDetailModalCmd(t.tracker, t.m.All, iss.Number)
}

// handleSidebarKey routes one keypress while ModeSidebar owns the keyboard
// (the sidebar has focus, the terminal is too narrow to dock it, or the
// operator zoomed it — modeActive's ModeSidebar case), through dispatchKey
// pinned to ModeSidebar — see dispatchKey and the keymap's ModeSidebar and
// quit entries (issue #1628, #1629, #1502, #826, #1790).
func (t teaModel) handleSidebarKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	t, cmd := t.dispatchKey(ModeSidebar, msg)
	return t.m, cmd
}

// highlightedIssue returns the backlog row under the cursor, or false when
// Visible() is empty.
func (t teaModel) highlightedIssue() (forge.Issue, bool) {
	vis := t.m.Visible()
	if len(vis) == 0 {
		return forge.Issue{}, false
	}
	return vis[t.m.Cursor], true
}

// highlightedPick returns the row under Cursor within whichever work Section
// is active, or false when that Section is empty — Enter's sidebar target on
// a work Section (ADR 0030, formerly the focused-queue case of #845).
// Meaningless for SectionBacklog, whose rows are issues, not Picks; callers
// only reach for this once ActiveSection is known to be a work Section.
func (t teaModel) highlightedPick() (Pick, bool) {
	picks := sectionPicks(t.m, t.m.ActiveSection)
	if len(picks) == 0 {
		return Pick{}, false
	}
	return picks[t.m.Cursor], true
}

// hasTranscript reports whether state is a PickState with an actual
// Transcript to drill into — running, settled, terminated, or failed all
// have a Box that ran (or is running) and left logs on disk; queued,
// claiming, held, and dissolved never launched, so Enter is a no-op on those
// rows (issue #845). PickFailed's inclusion deliberately extends past #845's
// literal AC text, which predates PickFailed's introduction in #705 —
// confirmed correct, not an oversight, by #992.
func hasTranscript(state PickState) bool {
	switch state {
	case PickRunning, PickSettled, PickTerminated, PickFailed:
		return true
	}
	return false
}

// openSidebarCmd loads number's Activity feed and whole rendered transcript
// in the background, combining ActivityFeed's derivation with DrillIn's
// transcript load into one SidebarLoadedMsg — the sidebar's default Activity
// view and its Transcript toggle both need no further I/O once this lands. A
// launch-less session (or a Launcher built without a Factory) has no Driver
// to load with — that renders as a graceful SidebarLoadedMsg error instead of
// dereferencing a nil Driver (issue #786 AC4, inherited). hasTranscript gates
// Enter on PickRunning already, but a pick can read as Running a moment
// before its Box's first log write lands on disk — DrillIn treats that as an
// error ("no logs found"), while ActivityFeed treats it as its own
// documented graceful-empty case; checking LogPaths once here picks
// ActivityFeed's contract for the combined message rather than surfacing a
// spurious failure for a Dispatch that simply hasn't written anything yet.
// This runs only on open (Enter) and loads the Transcript once, not live —
// only the Activity feed advances on its own afterward, via
// refreshPickDecorations' per-Msg SidebarActivityMsg refresh (issue #1502).
// Reopening (close then Enter again) still re-runs this whole load, picking
// up any Transcript growth the live Activity feed alone wouldn't (issue
// #719, inherited). orphan marks the drill-in as an orphan row's: the
// empty-Activity/no-logs case then also carries a graceful Notice ("no
// local logs for this dispatch") rather than staying silently blank, since
// an orphan-flagged Dispatch with nothing on disk yet is a standing state
// the operator opened deliberately, not the split-second
// claimed-but-not-yet-launched race a session-launched Pick's own Enter can
// hit (issue #1621).
func openSidebarCmd(launch *Launcher, pwd, number string, orphan bool) tea.Cmd {
	return func() tea.Msg {
		var drv driver.Driver
		if launch != nil {
			drv = launch.Driver()
		}
		if drv == nil {
			return SidebarLoadedMsg{Number: number, Err: fmt.Errorf("no Driver available for this session")}
		}
		activity := ActivityFeed(drv, pwd, number)
		if len(dispatch.LogPaths(pwd, number)) == 0 {
			msg := SidebarLoadedMsg{Number: number, Activity: activity}
			if orphan {
				msg.Notice = "no local logs for this dispatch"
			}
			return msg
		}
		// DrillIn always returns a DrillInMsg; the type assertion can't fail.
		dm, _ := DrillIn(drv, pwd, number).(DrillInMsg)
		return SidebarLoadedMsg{Number: number, Activity: activity, Rendered: dm.Rendered, Raw: dm.Raw, TranscriptErr: dm.Err}
	}
}

// handleTerminateConfirmKey routes one keypress while ModeTerminateConfirm is
// armed, through dispatchKey pinned to ModeTerminateConfirm — see dispatchKey
// (whose dispatchDefault declines the terminate for any key the keymap
// doesn't name) and the keymap's "y"/"Y" and quit entries (issue #745, #748,
// #1215, ADR 0024, issue #649/#785, #1790).
func (t teaModel) handleTerminateConfirmKey(msg tea.KeyMsg) teaModel {
	t, _ = t.dispatchKey(ModeTerminateConfirm, msg)
	return t
}

// handleQuitConfirmKey routes one keypress while ModeQuitConfirm is armed,
// through dispatchKey pinned to ModeQuitConfirm — see dispatchKey (whose
// dispatchDefault declines the quit, "s" included, for any key the keymap
// doesn't name) and the keymap's "d"/"enter"/"t" entry (issue #651, ADR
// 0023, issue #822, #1790).
func (t teaModel) handleQuitConfirmKey(msg tea.KeyMsg) teaModel {
	t, _ = t.dispatchKey(ModeQuitConfirm, msg)
	return t
}

// isLive reports whether num has an actual live Dispatch to reclaim — ADR
// 0024's Terminate is scoped to "claim to verdict", which on the Console's
// own Queue is exactly PickRunning (set the moment a claim succeeds,
// cleared only on settle); a plain backlog row that was never picked, or a
// pick still queued/held/claiming, has nothing to terminate.
func (t teaModel) isLive(num string) bool {
	if t.launch == nil {
		return false
	}
	for _, live := range t.launch.LiveIssues() {
		if live == num {
			return true
		}
	}
	return false
}

// highlightedNumber returns the cursor's highlighted issue number in
// whichever list the active Section shows — Visible() for SectionBacklog,
// the active work Section's own Picks otherwise — or "" when that list is
// empty (ADR 0030; formerly backlog-only, before the two-column split
// retired in #1500).
func (t teaModel) highlightedNumber() string {
	if t.m.ActiveSection == SectionBacklog {
		if iss, ok := t.highlightedIssue(); ok {
			return iss.Number
		}
		return ""
	}
	if p, ok := t.highlightedPick(); ok {
		return p.Number
	}
	return ""
}

// terminateTarget resolves the issue number "X" should act on: whichever row
// is actually drawn with ">" (view.go) in the active Section — isLive then
// gates whether that row actually has anything to terminate, so standing on
// a non-running row (queued, held, settled, ...) is a harmless no-op rather
// than a separate case here (issue #1500, formerly Focus-gated by #997).
func (t teaModel) terminateTarget() string {
	return t.highlightedNumber()
}

// alreadyActive reports whether num already has a non-terminal row (queued,
// held, claiming, or running) — Queue's row-scan helpers (setState,
// tryMarkClaiming) both assume at most one non-terminal row per issue
// number, scanning back-to-front for "the" live row; landing a second one
// for a number that's still active leaves the older row stuck forever and
// can hang the drain loop (issue #785 review). A terminal row (settled,
// dissolved, terminated, failed) never blocks a fresh pick — that's the
// legitimate re-pick/adopt path ADR 0024 describes.
//
// Reads Model.Picks alone, never the launcher's own queue — Pick/Unpick/
// TerminateAsync all land their snapshot on Model.Picks synchronously, in
// the same Update cycle that fired the keypress, so a stale pre-drain read
// (issue #837, the old rationale for a live-Queue bypass here) is now
// structurally impossible (issue #1542).
func (t teaModel) alreadyActive(num string) bool {
	for _, p := range t.m.Picks {
		if p.Number != num {
			continue
		}
		switch p.State {
		case PickQueued, PickHeld, PickClaiming, PickRunning:
			return true
		}
	}
	return false
}

// pickAllReady picks every issue currently Dispatchable on the tracker in
// one bulk gesture (#647 AC3) — reached via the standalone "P" key (issue
// #1838; previously the "pa" leader chord, issue #785 AC1). An issue
// already active from an earlier pick is skipped rather than re-landed
// (see alreadyActive). Each landed pick's snapshot is applied to
// Model.Picks immediately, not batched to the end of the loop, so a later
// iteration's alreadyActive check still sees every pick this same bulk
// gesture already landed (issue #1542).
func (t teaModel) pickAllReady() teaModel {
	for _, msg := range PickAllReady(t.tracker) {
		if queued, ok := msg.(PickQueuedMsg); ok && t.alreadyActive(queued.Number) {
			continue
		}
		if t.launch != nil {
			t.m = Update(t.m, QueueSnapshotMsg{Picks: t.launch.Land(msg)})
		} else {
			t.m = Update(t.m, msg)
		}
	}
	if t.launch != nil {
		t.launch.tryLaunch(t.tracker, t.pwd)
	}
	return t
}

// unpickHighlighted retracts the cursor's highlighted issue's queued pick, if
// any — a pure session-queue edit with no tracker interaction (ADR 0023):
// Launcher.Unpick already refuses to drop anything past PickQueued/PickHeld,
// so this is safe to send even when the highlighted issue never queued or
// already launched. A nil Launcher edits Model.Picks directly, matching the
// pre-#785 no-launch Console path.
func (t teaModel) unpickHighlighted() teaModel {
	num := t.highlightedNumber()
	if num == "" {
		return t
	}
	if t.launch != nil {
		t.m = Update(t.m, QueueSnapshotMsg{Picks: t.launch.Unpick(num)})
		return t
	}
	t.m = Update(t.m, UnpickMsg{Number: num})
	return t
}

// quitOrConfirmMsg picks QuitRequestedMsg over QuitMsg whenever live
// Dispatches exist, so any "q"/"ctrl+c" quit path arms the
// drain/terminate-all/stay confirm instead of exiting outright (issue
// #1216, ADR 0023).
func (t teaModel) quitOrConfirmMsg() Msg {
	if t.launch != nil && len(t.launch.LiveIssues()) > 0 {
		return QuitRequestedMsg{}
	}
	return QuitMsg{}
}

// landPick promotes num/title through Launcher.Pick and applies the fresh
// snapshot it hands back in the same Update cycle — the shared tail
// pickHighlighted and pickDetailModalIssue both land through, so their two
// pick sources (cursor row vs. open modal) can never drift apart on how a
// resolved target actually gets queued/launched. kind selects the Dispatch
// kind the pick carries: KindWork for the "p"/"P" gestures and the modal's
// own "p"; KindResearch has no console binding yet (issue #1838 leaves it
// unbound; #1709 wired the record itself) — Launcher.Pick's own trackerFor
// already routes a KindResearch pick onto ResearchTracker when one is
// wired. A nil Launcher promotes through PickIssue directly onto
// Model.Picks (matching the pre-#785 no-launch Console) but never queues on
// a live queue or launches, since there is nothing to launch it.
func (t teaModel) landPick(num, title string, kind Kind) (teaModel, Msg) {
	if t.launch == nil {
		// No Launcher means no trackerFor to pick a ResearchTracker over
		// t.tracker either — moot today since neither caller passes
		// KindResearch (issue #1838 left it unbound); a KindResearch pick
		// here would still promote on t.tracker's own label family, tagged
		// with the kind it was asked for regardless. Harmless in practice:
		// production always supplies a Launcher (cmdConsole wires
		// ResearchTracker unconditionally), so this branch is exercised
		// only by tests that deliberately skip the launch stack to
		// exercise bare Pick/Unpick bookkeeping.
		msg := PickIssue(t.tracker, num, title, kind)
		t.m = Update(t.m, msg)
		return t, msg
	}
	msg, picks := t.launch.Pick(t.tracker, num, title, kind)
	t.m = Update(t.m, QueueSnapshotMsg{Picks: picks})
	if _, ok := msg.(PickQueuedMsg); ok {
		t.launch.tryLaunch(t.tracker, t.pwd)
	}
	return t, msg
}

// pickHighlighted promotes the cursor's highlighted issue through landPick —
// the keypress translation of ADR 0023's Pick-is-the-launch-button rule. A
// no-op outside SectionBacklog — Cursor indexes a work Section's Picks
// there, not the backlog, and Backlog is ADR 0030's sole pick source (issue
// #1500) — and a no-op when the issue already has an active (queued, held,
// claiming, or running) row (issue #785 review).
func (t teaModel) pickHighlighted(kind Kind) teaModel {
	if t.m.ActiveSection != SectionBacklog {
		return t
	}
	visible := t.m.Visible()
	if len(visible) == 0 {
		return t
	}
	iss := visible[t.m.Cursor]
	if t.alreadyActive(iss.Number) {
		return t
	}
	t, _ = t.landPick(iss.Number, iss.Title, kind)
	return t
}

// pickDetailModalIssue promotes the open ticket detail modal's own displayed
// issue through landPick as a KindWork pick, keyed by DetailModal.Number/
// Title rather than the Backlog cursor — so a background refresh reordering
// rows underneath the open modal can never redirect the pick onto a
// different issue (issue #1835). A nil DetailModal (defensive only — the
// "p" binding this feeds is scoped to ModeDetailModal, which never fires
// without one) and an already active pick are both no-ops, same as
// pickHighlighted; a pick landPick's own PickIssue/Launcher.Pick refuses
// (already InProgress/Complete, TransitionState failure) lands as a
// PickDissolvedMsg row rather than mis-picking anything, and leaves the
// modal open so the operator can see why. Only a successful PickQueuedMsg
// closes the modal.
func (t teaModel) pickDetailModalIssue() teaModel {
	dm := t.m.DetailModal
	if dm == nil {
		return t
	}
	if t.alreadyActive(dm.Number) {
		return t
	}
	var msg Msg
	t, msg = t.landPick(dm.Number, dm.Title, KindWork)
	if _, ok := msg.(PickQueuedMsg); ok {
		t.m = Update(t.m, DetailModalCloseMsg{})
	}
	return t
}

// handleFilterKey routes one keypress while ModeFilterEdit owns the
// keyboard, through dispatchKey pinned to ModeFilterEdit — Enter applies
// (exits editing, keeping the already-live-narrowed Filter), Esc cancels
// (reverts to the pre-edit Filter), Backspace trims one rune, and any other
// printable key appends to the filter text — narrowing the list live. See
// dispatchKey's filterEditKeyName translation (this is the one mode whose
// dispatch is keyed by msg.Type rather than msg.String()) and the keymap's
// ModeFilterEdit entries (issue #1790).
func (t teaModel) handleFilterKey(msg tea.KeyMsg) teaModel {
	t, _ = t.dispatchKey(ModeFilterEdit, msg)
	return t
}

// refreshCmd re-queries tracker for the backlog in the background — the "r"
// key, the initial load, and both async signals all funnel through this one
// Cmd so their result lands on Model identically.
func refreshCmd(tracker forge.IssueTracker) tea.Cmd {
	return func() tea.Msg {
		return Refresh(tracker)
	}
}

// pollTick arms the next background-poll tick.
func pollTick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return pollTickMsg{} })
}

// refreshPickDecorations recomputes every Model.Picks row's live Heartbeat
// and Age display fields in place, so every render — not just the one right
// after a pick — shows an up-to-date elapsed time and, for a running row,
// its latest on-disk heartbeat line (#647 AC2). Unlike the syncQueue pull
// this replaces, it never touches the launcher's queue: Model.Picks is
// already the queue's authoritative mirror — landed synchronously by
// Pick/Unpick/TerminateAsync's own snapshot return, or pushed by a
// background transition via refreshSignalMsg — so this only decorates the
// rows already there (issue #1542). heartbeats caches the on-disk read per
// pick number, so a call whose latest pass log is unchanged since last time
// skips the ReadFile+reparse entirely (issue #731) — this runs on every
// tea.Msg, not just a render tick, so most calls see the same on-disk bytes
// as last time. The open sidebar's own Activity feed is refreshed the same
// way, scoped to whichever Dispatch it has open and only while that
// Dispatch is still running — ADR 0030's live-tail, piggybacking this same
// per-Msg sync rather than a dedicated timer, and bounded to one Dispatch's
// I/O no matter how many others are running (issue #1502). It also installs
// the session's current live parallelism cap and live count (issue #653),
// read straight off the Launcher's Limiter — no Msg carries a resize, so
// this per-render pull is the only path that keeps them current. A nil
// launch leaves m untouched.
func refreshPickDecorations(m Model, launch *Launcher, pwd string, heartbeats *HeartbeatCache, sidebarActivity *SidebarActivityCache, sidebarTranscript *SidebarTranscriptCache) Model {
	if launch == nil {
		return m
	}
	drv := launch.Driver()
	picks := make([]Pick, len(m.Picks))
	copy(picks, m.Picks)
	for i := range picks {
		if drv != nil && picks[i].State == PickRunning {
			picks[i].Heartbeat = heartbeats.RunningHeartbeat(drv, pwd, picks[i].Number)
		}
		if !picks[i].QueuedAt.IsZero() {
			picks[i].Age = formatAge(time.Since(picks[i].QueuedAt))
		}
	}
	m = Update(m, QueueSnapshotMsg{Picks: picks})
	// The len(m.OrphanHeartbeats) > 0 half of this guard, not just
	// len(m.OrphanNums) > 0, matters once OrphanNums drops to empty (the
	// last orphan adopted, or the whole list re-detected empty): without it
	// this whole branch is skipped and the previous tick's map is never
	// replaced with an empty one, leaving a stale heartbeat parked in
	// Model.OrphanHeartbeats indefinitely (harmless today since view.go
	// only ever reads it behind IsOrphan, but still stale state).
	if drv != nil && (len(m.OrphanNums) > 0 || len(m.OrphanHeartbeats) > 0) {
		// An orphan row has no Pick of its own for the heartbeat loop above
		// to reach — same on-disk log, same RunningHeartbeat/HeartbeatCache
		// machinery, just keyed straight off OrphanNums instead of a Pick
		// slice (issue #1621).
		orphanHeartbeats := make(map[string]string, len(m.OrphanNums))
		for _, num := range m.OrphanNums {
			orphanHeartbeats[num] = heartbeats.RunningHeartbeat(drv, pwd, num)
		}
		m = Update(m, OrphanHeartbeatsMsg{Heartbeats: orphanHeartbeats})
	}
	// An orphan-flagged sidebar has no Pick of its own to read a running
	// state off — isRunningNumber alone would starve it of the same live
	// tail a session-launched Dispatch gets (issue #1621). Refresh's own
	// stat-based cache keeps this cheap on every no-op call between actual
	// log writes (issue #731), same as the Pick-backed case.
	if m.Sidebar != nil && drv != nil && (isRunningNumber(picks, m.Sidebar.Number) || m.IsOrphan(m.Sidebar.Number)) {
		if activity, ok := sidebarActivity.Refresh(drv, pwd, m.Sidebar.Number); ok {
			m = Update(m, SidebarActivityMsg{Number: m.Sidebar.Number, Activity: activity})
		}
		// Scoped to ShowTranscript, on top of the same running-or-orphan gate
		// above: DrillIn re-reads and re-renders every pass log, heavier than
		// the Activity feed's single-file read, so it only runs while the
		// operator is actually looking at the Transcript view (issue #1736
		// AC1/AC3).
		if m.Sidebar.ShowTranscript {
			if rendered, raw, ok := sidebarTranscript.Refresh(drv, pwd, m.Sidebar.Number); ok {
				m = Update(m, SidebarTranscriptMsg{Number: m.Sidebar.Number, Rendered: rendered, Raw: raw})
			}
		}
	}
	return Update(m, CapMsg{Cap: launch.Cap(), Live: launch.Live()})
}

// isRunningNumber reports whether picks carries number in PickRunning state
// — refreshPickDecorations' gate on refreshing the open sidebar's Activity
// feed only while its Dispatch is actually live, since a Settled/Terminated/
// Failed Dispatch's logs never change again (issue #1502).
func isRunningNumber(picks []Pick, number string) bool {
	for _, p := range picks {
		if p.Number == number {
			return p.State == PickRunning
		}
	}
	return false
}

// syncStale installs launch's live image-freshness/rebuild state onto m, so
// every render reflects a stale verdict a background drain saw, or a
// rebuild's progress/outcome, exactly as refreshPickDecorations does for the
// picks queue (issue #652). A nil launch leaves m untouched.
func syncStale(m Model, launch *Launcher) Model {
	if launch == nil {
		return m
	}
	return Update(m, StaleStatusMsg{RebuildStatus: launch.StaleStatus()})
}

// orphanDetectCmd reports every issue OrphanedIssues finds running with no
// live goroutine in this process — a crash or dropped SSH from a prior
// session, or a competing process (a live dogfood loop, a second console
// session) that legitimately owns boxes this one has no goroutine for
// (issue #651, issue #822). It never adopts them: a runner-visible sandbox
// is not proof of abandonment, and an automatic adopt used to race a second
// settle against whichever process actually owns the box (issue #1619,
// demoting the auto-adopt ADR 0023 held). Detection is best-effort and
// silent on its own failure — a failed OrphanedIssues() call degrades to "no
// orphans found" rather than a startup warning, mirroring DogfoodNotice's
// read-error fallback, now that nothing here ever adopts for a warning to
// guard against. nil (no Cmd) when launch is nil, matching
// waitRefreshSignal's nil-launch convention.
func orphanDetectCmd(launch *Launcher) tea.Cmd {
	if launch == nil {
		return nil
	}
	return func() tea.Msg {
		nums, err := launch.OrphanedIssues()
		if err != nil {
			return nil
		}
		return OrphanDetectedMsg{Numbers: nums}
	}
}

// adoptOrphanCmd adopts num through launch's RecoverFn in the background —
// the operator's explicit gesture on an orphan-flagged Backlog row (issue
// #1619), the same settle-adoption path startup used to invoke on its own.
// A failure (no open PR, a draft PR, or a resolve error) surfaces through
// the returned OrphanRecoveryMsg exactly as a startup adopt failure used to
// (issue #1218) — Update threads it onto Model.OrphanRecoveryErr — and
// changes nothing else. A success returns OrphanAdoptedMsg, clearing num's
// orphan flag so a second press on the same row can't fire RecoverFn again.
// Either result also clears num out of Model.AdoptingOrphans, the in-flight
// mark handleKey sets synchronously before this Cmd ever starts — necessary
// because this whole call is the in-flight window itself: gating a repeat
// press on the orphan flag alone would still let a second "A" pressed
// before this goroutine returns fire a second concurrent RecoverFn call,
// racing the first over the same PR (review finding). nil (no Cmd) when
// launch or RecoverFn is nil.
func adoptOrphanCmd(launch *Launcher, num string) tea.Cmd {
	if launch == nil || launch.RecoverFn == nil {
		return nil
	}
	return func() tea.Msg {
		if err := launch.RecoverFn(num); err != nil {
			return OrphanRecoveryMsg{Number: num, Err: fmt.Sprintf("failed to adopt orphan #%s: %s", num, err)}
		}
		return OrphanAdoptedMsg{Number: num}
	}
}

// waitRefreshSignal blocks on launch's refresh channel in the background,
// translating one arrival into a refreshSignalMsg — nil (no Cmd) when launch
// is nil, since there is then no Queue whose writes could ever signal one.
// It also selects on done, closed by Update's Quitting choke point, since
// bubbletea has no way to cancel a Cmd goroutine itself once spawned (issue
// #823) — without this, a session that quits before ever signaling a
// refresh leaks this goroutine parked on <-ch for the process's lifetime.
func waitRefreshSignal(launch *Launcher, done <-chan struct{}) tea.Cmd {
	if launch == nil {
		return nil
	}
	ch := launch.Refreshes()
	return func() tea.Msg {
		select {
		case <-ch:
			picks, ok := launch.TakePendingSnapshot()
			return refreshSignalMsg{picks: picks, hasPicks: ok}
		case <-done:
			return nil
		}
	}
}
