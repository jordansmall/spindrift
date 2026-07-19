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
	if launch != nil {
		interval = launch.PollInterval()
	}
	return teaModel{m: m, tracker: tracker, pwd: pwd, launch: launch, pollInterval: interval, heartbeats: NewHeartbeatCache(), sidebarActivity: NewSidebarActivityCache(), done: make(chan struct{})}
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

// pickChordTimeout is how long "p" waits for a trailing "a" before resolving
// to a single-issue pick — long enough that a deliberate two-key "pa" always
// lands within it, short enough that a lone "p" still reads as instant
// (issue #785 AC1).
const pickChordTimeout = 200 * time.Millisecond

// pickChordTimeoutMsg is the tea layer's signal that "p"'s leader window
// elapsed with no trailing "a" — resolves a still-pending chord to a
// single-issue pick.
type pickChordTimeoutMsg struct{}

// pickChordTick arms the leader-window timeout.
func pickChordTick() tea.Cmd {
	return tea.Tick(pickChordTimeout, func(time.Time) tea.Msg { return pickChordTimeoutMsg{} })
}

// gChordTimeout is how long a lone "g" waits for a trailing "g" before the
// leader window cancels — same duration as pickChordTimeout, mirroring the
// "pa" chord precedent (issue #1628).
const gChordTimeout = 200 * time.Millisecond

// gChordTimeoutMsg is the tea layer's signal that "g"'s leader window
// elapsed with no trailing "g" — cancels the still-pending leader.
type gChordTimeoutMsg struct{}

// gChordTick arms the "gg" leader-window timeout.
func gChordTick() tea.Cmd {
	return tea.Tick(gChordTimeout, func(time.Time) tea.Msg { return gChordTimeoutMsg{} })
}

// Init starts the initial backlog load and both async signal sources
// (background poll, launch-refresh) as Cmds — none of them block the
// program's own startup.
func (t teaModel) Init() tea.Cmd {
	cmds := []tea.Cmd{refreshCmd(t.tracker), pollTick(t.pollInterval)}
	if t.launch != nil {
		cmds = append(cmds, initialQueueSyncCmd(t.launch), waitRefreshSignal(t.launch, t.done), orphanDetectCmd(t.launch))
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
// ticks, refresh signals, pick-chord timeouts).
func (t teaModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.KeyMsg:
		t, cmd = t.handleKey(msg)
	case tea.WindowSizeMsg:
		t.m = Update(t.m, SizeChangedMsg{Width: msg.Width, Height: msg.Height})
	case IssuesLoadedMsg: // async-load result, not a reactive signal
		t.m = Update(t.m, msg)
	case SidebarLoadedMsg: // async-load result, not a reactive signal
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
	case refreshSignalMsg:
		if msg.hasPicks {
			t.m = Update(t.m, QueueSnapshotMsg{Picks: msg.picks})
		}
		cmd = tea.Batch(refreshCmd(t.tracker), waitRefreshSignal(t.launch, t.done))
	case pickChordTimeoutMsg:
		if t.m.Mode == ModePick {
			t.m = Update(t.m, PickResolvedMsg{})
			t = t.pickHighlighted()
		}
	case gChordTimeoutMsg:
		if t.m.PendingG {
			t.m = Update(t.m, GResolvedMsg{})
		}
	}

	t.m = refreshPickDecorations(t.m, t.launch, t.pwd, t.heartbeats, t.sidebarActivity)
	t.m = syncStale(t.m, t.launch)
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

// isQuitKey reports whether s is the universal quit keystroke — "q" or
// "ctrl+c" — one definition shared by every mode that must not swallow it:
// handleListKey's own primary handling below, plus the four escape hatches
// that let a *different* mode's key still reach a quit (handlePickChordKey,
// handleSidebarKey, handleRebuildOutputKey, and handleTerminateConfirmKey's
// decline-and-arm case) — replacing five separately typed string matches
// (issue #1543). ModeHelp, ModeFilterEdit, and ModeQuitConfirm deliberately
// have no escape of their own — Help swallows every key but "?"/Esc, Esc
// types a literal "q" into the filter, and QuitConfirm is already the
// pending quit, so there's nothing left to escape to.
//
// This is only the shared string match, not a single pre-dispatch quit
// check — each of the four escape hatches still resolves its own
// mode-specific state (a pending pick, a declined terminate) before
// deciding between QuitMsg and QuitRequestedMsg, and those four decisions
// genuinely differ from each other and from handleListKey's own. A future
// mode that needs the same escape (the ADR 0030 reskin's #1496 train, per
// issue #1543's own sequencing note) must still call isQuitKey from inside
// its own handler; there is no single handleKey-level check to hook into
// instead.
func isQuitKey(s string) bool {
	return s == "q" || s == "ctrl+c"
}

// handleKey translates one keypress into a console Msg and applies it,
// dispatching on whichever Mode Model.ActiveMode reports owns the keyboard
// right now. modePrecedence (model.go) is the flat, ordered data this used
// to be an if-cascade over — the six router functions below (plus
// handleListKey for the ModeList default) are what the cascade's six
// branches collapsed into (issue #1543).
func (t teaModel) handleKey(msg tea.KeyMsg) (teaModel, tea.Cmd) {
	switch t.m.ActiveMode() {
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
	case ModePick:
		return t.handlePickChordKey(msg)
	default: // ModeList
		return t.handleListKey(msg)
	}
}

// handleHelpKey routes one keypress while the help overlay is open: "?" and
// Esc both close it, and everything else — including a quit keystroke — is
// swallowed, unchanged from before the mode-enum refactor (issue #784,
// #1543).
func (t teaModel) handleHelpKey(msg tea.KeyMsg) teaModel {
	if s := msg.String(); s == "?" || s == "esc" {
		t.m = Update(t.m, HelpToggleMsg{})
	}
	return t
}

// handlePickChordKey routes one keypress while ModePick is armed, awaiting
// "pa"'s trailing "a": "a" resolves to pick-all-ready, the universal quit
// keystroke resolves the chord (matching any other non-"a" key) and then
// still quits — but the pick just landed may itself be live now, so this
// still gates on LiveIssues() via quitOrConfirmMsg rather than an
// unconditional QuitMsg (issue #1216) — and anything else resolves the
// chord to a single-issue pick, same as letting the leader window time out;
// that second key's own meaning is not separately reprocessed.
func (t teaModel) handlePickChordKey(msg tea.KeyMsg) (teaModel, tea.Cmd) {
	t.m = Update(t.m, PickResolvedMsg{})
	switch s := msg.String(); {
	case s == "a":
		return t.pickAllReady(), nil
	case isQuitKey(s):
		t = t.pickHighlighted()
		t.m = Update(t.m, t.quitOrConfirmMsg())
		return t, nil
	default:
		return t.pickHighlighted(), nil
	}
}

// handleListKey routes one keypress against the plain backlog/queue body —
// ModeList, modePrecedence's last resort. PendingG's "gg" leader and
// QueueEnterNotice's clear-on-any-key both still run first here exactly as
// they did before the mode-enum refactor: neither is a rival claimant to
// keyboard ownership (Mode's own doc comment), so neither earns a case in
// handleKey's switch above — they layer on top of ModeList instead (issue
// #1543).
func (t teaModel) handleListKey(msg tea.KeyMsg) (teaModel, tea.Cmd) {
	if t.m.PendingG {
		t.m = Update(t.m, GResolvedMsg{})
		if msg.String() == "g" {
			t.m = Update(t.m, CursorJumpToFirstMsg{})
			return t, nil
		}
		// Any other key cancels the leader, same as the timeout, but — unlike
		// handlePickChordKey's chord above — does not consume it: that key's
		// own meaning still applies, so falls through to the switch below
		// rather than returning here (issue #1628 AC).
	}
	if t.m.QueueEnterNotice != "" {
		t.m = Update(t.m, QueueEnterNoticeClearedMsg{})
	}
	if isQuitKey(msg.String()) {
		t.m = Update(t.m, t.quitOrConfirmMsg())
		return t, nil
	}
	switch msg.String() {
	case "j", "down":
		t.m = Update(t.m, CursorMoveMsg{Delta: 1})
	case "k", "up":
		// "k" is vim's cursor-up key, freed by Terminate's move to "X"
		// (issue #1500); "i"-as-up (#838) is retired now that "k" covers it.
		t.m = Update(t.m, CursorMoveMsg{Delta: -1})
	case "G":
		t.m = Update(t.m, CursorJumpToLastMsg{})
	case "g":
		t.m = Update(t.m, GPendingMsg{})
		return t, gChordTick()
	case "H":
		t.m = Update(t.m, SectionPrevMsg{})
	case "L":
		t.m = Update(t.m, SectionNextMsg{})
	case "1":
		t.m = Update(t.m, SectionJumpMsg{Section: SectionBacklog})
	case "2":
		t.m = Update(t.m, SectionJumpMsg{Section: SectionRunning})
	case "3":
		t.m = Update(t.m, SectionJumpMsg{Section: SectionHeld})
	case "4":
		t.m = Update(t.m, SectionJumpMsg{Section: SectionSettled})
	case "5":
		t.m = Update(t.m, SectionJumpMsg{Section: SectionFailed})
	case "pgdown":
		t.m = Update(t.m, ScrollMsg{Delta: sectionPageSize(t.m)})
	case "pgup":
		t.m = Update(t.m, ScrollMsg{Delta: -sectionPageSize(t.m)})
	case "/":
		t.m = Update(t.m, FilterEditStartMsg{})
	case "enter":
		if t.m.ActiveSection == SectionBacklog {
			if iss, ok := t.highlightedIssue(); ok && t.m.IsOrphan(iss.Number) {
				return t, openSidebarCmd(t.launch, t.pwd, iss.Number, true)
			}
			t = t.pickHighlighted()
			return t, nil
		}
		if p, ok := t.highlightedPick(); ok {
			if hasTranscript(p.State) {
				return t, openSidebarCmd(t.launch, t.pwd, p.Number, false)
			}
			t.m = Update(t.m, QueueEnterNoticedMsg{})
		}
	case "l", "right":
		t.m = Update(t.m, FocusSidebarMsg{})
	case "h", "left":
		// Already on the list — nothing to move away from. Present as an
		// explicit case (rather than falling through to the default no-op)
		// so the h/l pair reads as one symmetric gesture at the call site.
	case "x", "esc":
		// Mirrors handleSidebarKey's close case (line ~392): a docked sidebar
		// with focus moved back to the list is still open and still needs a
		// single dismissal key, not a re-focus-then-close two-step (issue
		// #1582). Fullscreen/zoomed sidebars never reach here — ActiveMode
		// already resolves to ModeSidebar for those, so handleKey routes
		// there instead of handleListKey.
		if t.m.Sidebar != nil {
			t.m = Update(t.m, SidebarCloseMsg{})
		}
	case "r":
		return t, refreshCmd(t.tracker)
	case "?":
		t.m = Update(t.m, HelpToggleMsg{})
	case "p":
		t.m = Update(t.m, PickPendingMsg{})
		return t, pickChordTick()
	case "u":
		t = t.unpickHighlighted()
	case "A":
		if t.m.ActiveSection == SectionBacklog && t.launch != nil && t.launch.RecoverFn != nil {
			if iss, ok := t.highlightedIssue(); ok && t.m.IsOrphan(iss.Number) && !t.m.IsAdoptingOrphan(iss.Number) {
				t.m = Update(t.m, AdoptOrphanStartedMsg{Number: iss.Number})
				return t, adoptOrphanCmd(t.launch, iss.Number)
			}
		}
	case "X":
		if num := t.terminateTarget(); num != "" && t.isLive(num) {
			t.m = Update(t.m, TerminateRequestedMsg{Number: num})
		}
	case "+":
		if t.launch != nil {
			t.launch.Resize(1)
			// Resize's own Grown signal only reaches a drain already
			// running; a session with no active drain (nothing picked yet,
			// or the last one already went idle) has no listener to catch
			// it, so a raise falls back to tryLaunch — a no-op if a drain
			// is in fact already running, or if nothing is queued/held to
			// launch into the freed slot (#754).
			t.launch.tryLaunch(t.tracker, t.pwd)
		}
	case "-":
		if t.launch != nil {
			t.launch.Resize(-1)
		}
	case "b":
		if t.launch != nil && t.m.RebuildStatus.Stale {
			t.launch.Rebuild(t.tracker, t.pwd)
		}
	case "o":
		if t.m.RebuildStatus.Output != "" {
			t.m = Update(t.m, RebuildOutputOpenMsg{})
		}
	}
	return t, nil
}

// handleRebuildOutputKey routes one keypress while ModeRebuildOutput owns
// the keyboard: "x"/Esc closes back to the backlog, the scroll keys page
// through the captured output, "G"/"gg" jump to the last/first page, and the
// universal quit keystroke (isQuitKey) hard-quits — it must never be
// swallowed by the pane, matching handleDrillInKey (issue #1128). ActiveMode
// routes here before it ever reaches handleListKey's own PendingG check, so
// the "gg" chord's PendingG gate lives here instead — reusing Model.PendingG
// and gChordTick rather than a second, pane-scoped leader (issue #1630 AC3).
func (t teaModel) handleRebuildOutputKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	if t.m.PendingG {
		t.m = Update(t.m, GResolvedMsg{})
		if msg.String() == "g" {
			return Update(t.m, RebuildOutputJumpToFirstMsg{}), nil
		}
		// Any other key cancels the leader without consuming it — that key's
		// own meaning still applies below, mirroring handleListKey's own
		// PendingG fallthrough (issue #1628 AC).
	}
	if isQuitKey(msg.String()) {
		return Update(t.m, QuitMsg{}), nil
	}
	switch msg.String() {
	case "x", "esc":
		return Update(t.m, RebuildOutputCloseMsg{}), nil
	case "j", "down":
		return Update(t.m, RebuildOutputScrollMsg{Delta: 1}), nil
	case "k", "up":
		return Update(t.m, RebuildOutputScrollMsg{Delta: -1}), nil
	case "pgdown":
		return Update(t.m, RebuildOutputScrollMsg{Delta: fixedPaneScrollDelta}), nil
	case "pgup":
		return Update(t.m, RebuildOutputScrollMsg{Delta: -fixedPaneScrollDelta}), nil
	case "G":
		return Update(t.m, RebuildOutputJumpToLastMsg{}), nil
	case "g":
		t.m = Update(t.m, GPendingMsg{})
		return t.m, gChordTick()
	}
	return t.m, nil
}

// handleSidebarKey routes one keypress while ModeSidebar owns the keyboard
// (the sidebar has focus, the terminal is too narrow to dock it, or the
// operator zoomed it — modeActive's ModeSidebar case): "t" advances the
// Activity/Transcript/raw cycle, "h"/left returns focus to the list when the
// sidebar is docked (a no-op in the fullscreen fallback, which has no list
// on screen to focus), "x"/Esc closes back to the body, the scroll keys page
// through the loaded content (issue #786's drill-in precedent), "G"/"end"
// re-attaches Follow and jumps to the bottom, "gg" detaches Follow and jumps
// to the top — reusing the same Model.PendingG leader and gChordTick timeout
// the list body's own "gg" arms (issue #1628), rather than a second chord
// mechanism (issue #1629) — "z" toggles the fullscreen zoom (issue #1502),
// and the universal quit keystroke (isQuitKey) hard-quits — it must never be
// swallowed by the sidebar, same principle as handlePickChordKey (issue
// #826) but not the same mechanics: handlePickChordKey resolves the chord
// (pickHighlighted) before quitting, while the sidebar quits directly with
// no resolve step.
func (t teaModel) handleSidebarKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	if t.m.PendingG {
		t.m = Update(t.m, GResolvedMsg{})
		if msg.String() == "g" {
			t.m = Update(t.m, SidebarJumpToBeginningMsg{})
			return t.m, nil
		}
		// Any other key cancels the leader, same as the timeout, but — like
		// handleListKey's own branch — does not consume it: that key's own
		// meaning still applies, so falls through to the switch below rather
		// than returning here (issue #1628 AC, #1629).
	}
	if isQuitKey(msg.String()) {
		return Update(t.m, QuitMsg{}), nil
	}
	switch msg.String() {
	case "t":
		return Update(t.m, SidebarToggleMsg{}), nil
	case "h", "left":
		if sidebarFits(t.m) && !t.m.SidebarZoom {
			return Update(t.m, FocusListMsg{}), nil
		}
		return t.m, nil
	case "x", "esc":
		return Update(t.m, SidebarCloseMsg{}), nil
	case "j", "down":
		return Update(t.m, SidebarScrollMsg{Delta: 1}), nil
	case "k", "up":
		return Update(t.m, SidebarScrollMsg{Delta: -1}), nil
	case "pgdown":
		return Update(t.m, SidebarScrollMsg{Delta: fixedPaneScrollDelta}), nil
	case "pgup":
		return Update(t.m, SidebarScrollMsg{Delta: -fixedPaneScrollDelta}), nil
	case "G", "end":
		return Update(t.m, SidebarJumpToEndMsg{}), nil
	case "g":
		t.m = Update(t.m, GPendingMsg{})
		return t.m, gChordTick()
	case "z":
		return Update(t.m, SidebarZoomToggleMsg{}), nil
	}
	return t.m, nil
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
// armed: "y" confirms — firing Launcher.TerminateAsync before applying
// TerminateConfirmedMsg, so the blocking tracker I/O runs off the Update
// path (issue #745) — the universal quit keystroke (isQuitKey) must never be
// swallowed by the confirm prompt, mirroring handlePickChordKey's precedent
// (issue #748), but must not quit outright either: a terminate-confirm
// prompt is only ever armed via isLive, so a live Dispatch was present when
// it appeared, and the operator still deserves the drain/terminate-all/stay
// choice ADR 0023 promises elsewhere (unconditionally arming that confirm is
// harmless even on the rare race where the Dispatch settles between the
// prompt and this keypress — "t" just iterates an empty LiveIssues) — so a
// quit keystroke declines the terminate (TerminateCancelledMsg, returning to
// ModeList so the next keypress reaches handleQuitConfirmKey instead of
// looping back here) and arms the quit confirm (QuitRequestedMsg) rather
// than quitting directly (issue #1215) — everything else declines the
// terminate only (ADR 0024, issue #649/#785).
func (t teaModel) handleTerminateConfirmKey(msg tea.KeyMsg) teaModel {
	num := t.m.TerminateConfirm.Number
	switch s := msg.String(); {
	case s == "y" || s == "Y":
		if t.launch != nil {
			// Terminate already logs a reap failure to stderr itself
			// (launcher.go); writing it again here would both duplicate the
			// line and risk smearing the alt-screen render mid-frame. The
			// actual PickTerminated transition lands later, once Terminate's
			// background goroutine reaches it, through a pushed
			// refreshSignalMsg — this snapshot is the queue as it stands at
			// initiation (issue #1542).
			picks := t.launch.TerminateAsync(t.tracker, num)
			t.m = Update(t.m, QueueSnapshotMsg{Picks: picks})
		}
		t.m = Update(t.m, TerminateConfirmedMsg{Number: num})
	case isQuitKey(s):
		t.m = Update(t.m, TerminateCancelledMsg{})
		t.m = Update(t.m, QuitRequestedMsg{})
	default:
		t.m = Update(t.m, TerminateCancelledMsg{})
	}
	return t
}

// handleQuitConfirmKey routes one keypress while ModeQuitConfirm is armed:
// "d"/enter drains — quits without touching any live Dispatch, which settles
// on its own — "t" terminates every live Dispatch first, then quits; "s" (or
// anything else) stays, declining the quit (issue #651, ADR 0023, issue
// #822). Unlike handleTerminateConfirmKey, quit is already the pending
// action here, so there is no separate quit-escape case.
func (t teaModel) handleQuitConfirmKey(msg tea.KeyMsg) teaModel {
	switch s := msg.String(); s {
	case "d", "enter":
		t.m = Update(t.m, QuitMsg{})
	case "t":
		if t.launch != nil {
			for _, num := range t.launch.LiveIssues() {
				t.launch.TerminateAsync(t.tracker, num)
			}
		}
		t.m = Update(t.m, QuitMsg{})
	default:
		t.m = Update(t.m, QuitCancelledMsg{})
	}
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
// one bulk gesture (#647 AC3) — reached via the "pa" leader chord (issue
// #785 AC1). An issue already active from an earlier pick is skipped rather
// than re-landed (see alreadyActive). Each landed pick's snapshot is applied
// to Model.Picks immediately, not batched to the end of the loop, so a later
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
// Dispatches exist, so any "q"/"ctrl+c" quit path — chorded through
// ModePick or not — arms the drain/terminate-all/stay confirm instead of
// exiting outright (issue #1216, ADR 0023).
func (t teaModel) quitOrConfirmMsg() Msg {
	if t.launch != nil && len(t.launch.LiveIssues()) > 0 {
		return QuitRequestedMsg{}
	}
	return QuitMsg{}
}

// pickHighlighted promotes the cursor's highlighted issue through
// Launcher.Pick and applies the fresh snapshot it hands back in the same
// Update cycle — the keypress translation of ADR 0023's
// Pick-is-the-launch-button rule. A nil Launcher promotes through PickIssue
// directly onto Model.Picks (matching the pre-#785 no-launch Console) but
// never queues on a live queue or launches, since there is nothing to
// launch it. A no-op outside SectionBacklog — Cursor indexes a work
// Section's Picks there, not the backlog, and Backlog is ADR 0030's sole
// pick source (issue #1500).
func (t teaModel) pickHighlighted() teaModel {
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
	if t.launch == nil {
		t.m = Update(t.m, PickIssue(t.tracker, iss.Number, iss.Title, KindWork))
		return t
	}
	msg, picks := t.launch.Pick(t.tracker, iss.Number, iss.Title, KindWork)
	t.m = Update(t.m, QueueSnapshotMsg{Picks: picks})
	if _, ok := msg.(PickQueuedMsg); ok {
		t.launch.tryLaunch(t.tracker, t.pwd)
	}
	return t
}

// handleFilterKey routes one keypress while ModeFilterEdit owns the
// keyboard: Enter applies (exits editing, keeping the already-live-narrowed
// Filter), Esc cancels (reverts to the pre-edit Filter), Backspace trims one
// rune, and any other printable key appends to the filter text — narrowing
// the list live.
func (t teaModel) handleFilterKey(msg tea.KeyMsg) teaModel {
	switch msg.Type {
	case tea.KeyEnter:
		t.m = Update(t.m, FilterEditConfirmMsg{})
	case tea.KeyEsc:
		t.m = Update(t.m, FilterEditCancelMsg{})
	case tea.KeyBackspace:
		if n := len(t.m.Filter); n > 0 {
			t.m = Update(t.m, FilterChangedMsg{Filter: t.m.Filter[:n-1]})
		}
	case tea.KeyRunes, tea.KeySpace:
		t.m = Update(t.m, FilterChangedMsg{Filter: t.m.Filter + msg.String()})
	}
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
func refreshPickDecorations(m Model, launch *Launcher, pwd string, heartbeats *HeartbeatCache, sidebarActivity *SidebarActivityCache) Model {
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
