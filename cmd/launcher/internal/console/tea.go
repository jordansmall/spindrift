// Run drives the console as a full-screen Bubble Tea program (issue #784).
// teaModel is a thin adapter: Update translates tea.KeyMsg and the two async
// signals (background poll, launch-refresh) into console Msg values, and View
// delegates to the pure View (issues #785, #786, #845, #1501).
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
// transcript's scroll offset (issue #786). Fixed, unlike sectionPageSize,
// which derives from the live viewport height (issue #1037).
const fixedPaneScrollDelta = 10

// teaModel is the Bubble Tea adapter around the pure Model: it carries the
// I/O seams (tracker, pwd, launch) Update itself never touches, and
// translates tea.Msg values into console Msg values before calling Update.
type teaModel struct {
	m Model
	// layout caches m's resolved layout across the tea layer's dispatch. nil
	// means unresolved, the zero value a bare teaModel{m: m} test literal
	// starts with. apply is the only seam that sets it, so any direct t.m
	// mutation elsewhere must nil it back out (issue #3018).
	layout       *layout
	tracker      forge.IssueTracker
	pwd          string
	launch       *Launcher
	pollInterval time.Duration
	// heartbeats lets refreshPickDecorations skip the ReadFile+reparse when a
	// pick's latest pass log is unchanged since the last call (issue #731). A
	// pointer, so it survives Update's value-receiver copies of teaModel.
	heartbeats *HeartbeatCache
	// sidebarActivity caches the open sidebar's Activity feed the same way,
	// scoped to the one selected Dispatch so I/O stays bounded even with many
	// running (issue #1502, ADR 0030).
	sidebarActivity *SidebarActivityCache
	// sidebarTranscript caches the open sidebar's Transcript render, so the
	// Transcript view live-tails a running Dispatch instead of staying frozen
	// at open time (issue #1736).
	sidebarTranscript *SidebarTranscriptCache
	// sidebarTickArmed keeps Update from stacking a second sidebarActivityTick
	// while the first is still pending (issue #1735).
	sidebarTickArmed bool
	// sidebarTickGen is the generation each armed sidebarActivityTick carries,
	// so a stale timer left by a close-then-reopen within one tick interval is
	// dropped instead of re-arming a second, permanently duplicate tick chain.
	sidebarTickGen uint64
	// toastGen is the generation each armed toastDismissTick carries (issue
	// #1830). A tea.Tick can't be cancelled once scheduled, so a superseded
	// toast's dismiss timer still fires; Update drops it as stale rather than
	// clearing the newer toast.
	toastGen uint64
	// watcher fires a logWriteMsg on a write to any watchedPaths entry, so the
	// heartbeat refresh runs within moments of new bytes landing instead of
	// waiting for the next pollTickMsg (issue #1748). nil for a launch-less
	// session, or when fsnotify.NewWatcher failed; refreshPickDecorations
	// still runs on every Msg, just back to the slower poll cadence.
	watcher *fsnotify.Watcher
	// watchedPaths mirrors watcher's own watch set (fsnotify offers no query
	// cheaper than WatchList's full slice) so reconcileWatches can diff
	// against it in place. A map, so every copy of teaModel shares one.
	watchedPaths map[string]struct{}
	// done is closed exactly once, at the Quitting choke point below, to
	// unblock waitRefreshSignal's goroutine: bubbletea can't cancel a Cmd
	// goroutine itself (issue #823), so the closure selects on this instead of
	// blocking on Launcher.Refreshes() forever. A chan, so every copy shares one.
	done chan struct{}
}

// newTeaModel builds the tea layer's starting state. The initial backlog
// load, background poll, and launch-refresh listener all start as Cmds from
// Init instead.
func newTeaModel(tracker forge.IssueTracker, pwd string, launch *Launcher) teaModel {
	m := NewModel()
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

// Run drives the console's full-screen Bubble Tea program to completion (issue
// #784). launch is nil for a launch-less session; production wires a real one.
func Run(tracker forge.IssueTracker, pwd string, in io.Reader, out io.Writer, launch *Launcher) error {
	p := tea.NewProgram(newTeaModel(tracker, pwd, launch), tea.WithInput(in), tea.WithOutput(out), tea.WithAltScreen())
	_, err := p.Run()
	if launch != nil {
		launch.Wait()
	}
	return err
}

// pollTickMsg is the tea layer's background-poll tick (#647 AC5), re-armed on
// every arrival so the poll runs for the program's whole lifetime.
type pollTickMsg struct{}

// refreshSignalMsg translates a Launcher.Refreshes() firing: the session's own
// tracker write asking for an out-of-band refresh (#647 AC4). picks carries the
// queue snapshot signalRefresh recorded, hasPicks distinguishing "nothing
// pending" from a genuinely empty queue, so the tea side lands it without ever
// pulling Queue itself (issue #1542).
type refreshSignalMsg struct {
	picks    []Pick
	hasPicks bool
}

// toastDismissDelay is how long a pick-transition toast (Model.Toast, issue
// #1830) stays visible: long enough to read a short "#NN started: title" line,
// short enough that it never lingers into the next transition's own toast.
const toastDismissDelay = 4 * time.Second

// toastDismissTickMsg auto-dismisses a pick-transition toast. gen pins it to
// the teaModel.toastGen that armed it, so a stale straggler is dropped rather
// than clearing a newer toast.
type toastDismissTickMsg struct{ gen uint64 }

// toastDismissTick arms one toastDismissTickMsg carrying gen.
func toastDismissTick(gen uint64) tea.Cmd {
	return tea.Tick(toastDismissDelay, func(time.Time) tea.Msg { return toastDismissTickMsg{gen: gen} })
}

// sidebarActivityTickInterval is how often the docked sidebar's live-tail tick
// fires, independent of both keypresses and the pollTick backlog cadence, so an
// open sidebar advances while the operator watches, zoomed included (#1735).
const sidebarActivityTickInterval = time.Second

// sidebarActivityTickMsg is the sidebar's live-tail signal; landing it reaches
// Update's refreshPickDecorations call. gen pins it to the sidebarTickGen that
// armed it: tea.Tick can't be cancelled once scheduled, so a close-then-reopen
// within one tick interval leaves a stale timer in flight, and gen lets Update
// drop it instead of re-arming a duplicate tick chain (issue #1735).
type sidebarActivityTickMsg struct{ gen uint64 }

// sidebarActivityTick arms one sidebarActivityTickMsg carrying gen.
func sidebarActivityTick(gen uint64) tea.Cmd {
	return tea.Tick(sidebarActivityTickInterval, func(time.Time) tea.Msg { return sidebarActivityTickMsg{gen: gen} })
}

// sidebarActivityLive reports whether the docked sidebar is open on a Dispatch
// whose Activity feed can still change, the same gate refreshPickDecorations
// applies, so the live-tail tick arms and disarms in lockstep with the refresh
// it exists to drive (issues #1502, #1621).
func sidebarActivityLive(m Model) bool {
	return m.Sidebar != nil && (isRunningNumber(m.Picks, m.Sidebar.Number) || m.IsOrphan(m.Sidebar.Number))
}

// gChordTimeout is how long a lone "g" waits for a trailing "g": long enough
// that a deliberate two-key "gg" always lands within it, short enough that a
// lone "g" still reads as responsive (issue #1628).
const gChordTimeout = 200 * time.Millisecond

// gChordTimeoutMsg cancels a still-pending "g" leader whose window elapsed
// with no trailing "g".
type gChordTimeoutMsg struct{}

// gChordTick arms the "gg" leader-window timeout.
func gChordTick() tea.Cmd {
	return tea.Tick(gChordTimeout, func(time.Time) tea.Msg { return gChordTimeoutMsg{} })
}

// resolvePendingG resolves an armed "gg" leader against msg (issue #1802). A
// no-op when PendingG isn't set. Otherwise it clears the leader and, if msg is
// the second "g", applies onFirst and reports the key consumed. Any other key
// clears the leader but is reported unconsumed, so the caller falls through to
// that key's own binding (issue #1628 AC).
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

// armPendingG arms the "gg" leader window on t (issue #1802). It goes through
// apply like every other production mutation, so t's cached layout stays
// current (issue #3018).
func armPendingG(t teaModel) (teaModel, tea.Cmd) {
	return t.apply(GPendingMsg{}), gChordTick()
}

// Init starts the initial backlog load and both async signal sources
// (background poll, launch-refresh) as Cmds; none of them block startup.
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
// startup, through the exported Snapshot accessor rather than the private
// queue (issue #1542). Every later transition reaches the Model through
// Pick/Unpick/TerminateAsync's return value or a pushed refreshSignalMsg, so
// nothing else catches up a queue that started non-empty.
func initialQueueSyncCmd(launch *Launcher) tea.Cmd {
	return func() tea.Msg {
		return QueueSnapshotMsg{Picks: launch.Snapshot()}
	}
}

// apply is the tea layer's one seam onto updateLayout, so the layout resolved
// for the post-msg Model is captured here rather than re-resolved later by
// View or a keymap Action (issue #3018).
func (t teaModel) apply(msg Msg) teaModel {
	m, l := updateLayout(t.m, msg)
	t.m = m
	t.layout = &l
	return t
}

// withModel installs m as t.m and invalidates the cached layout: the few direct
// Model mutations that skip apply can't reuse updateLayout's return, so a fresh
// resolveLayout on the next currentLayout call is the only safe move (#3018).
func (t teaModel) withModel(m Model) teaModel {
	t.m = m
	t.layout = nil
	return t
}

// currentLayout returns the layout describing t.m, resolving it fresh only when
// apply hasn't already cached one. Never memoizes into t itself: the receiver
// is a value copy, so writing t.layout here would vanish with the call (#3018).
func (t teaModel) currentLayout() layout {
	if t.layout != nil {
		return *t.layout
	}
	return resolveLayout(t.m)
}

// Update translates every Bubble Tea message (key presses, resizes) and
// internal signal into console Msg values the pure Update already handles, then
// re-syncs the launcher's live Queue and stale state onto the Model.
func (t teaModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	prevToast := t.m.Toast
	switch msg := msg.(type) {
	case tea.KeyMsg:
		t, cmd = t.handleKey(msg)
	case tea.WindowSizeMsg:
		t = t.apply(SizeChangedMsg{Width: msg.Width, Height: msg.Height})
	case IssuesLoadedMsg:
		t = t.apply(msg)
	case SidebarLoadedMsg:
		t = t.apply(msg)
	case DetailModalLoadedMsg:
		t = t.apply(msg)
	case OrphanRecoveryMsg:
		t = t.apply(msg)
	case OrphanDetectedMsg:
		t = t.apply(msg)
	case OrphanAdoptedMsg:
		t = t.apply(msg)
	case QueueSnapshotMsg:
		t = t.apply(msg)
	case pollTickMsg:
		if t.launch != nil {
			t.launch.tryLaunch(t.tracker, t.pwd)
		}
		cmd = tea.Batch(refreshCmd(t.tracker), pollTick(t.pollInterval))
	case logWriteMsg:
		cmd = waitLogWrite(t.watcher, t.done)
	case refreshSignalMsg:
		if msg.hasPicks {
			t = t.apply(QueueSnapshotMsg{Picks: msg.picks})
		}
		cmd = tea.Batch(refreshCmd(t.tracker), waitRefreshSignal(t.launch, t.done))
	case gChordTimeoutMsg:
		if t.m.PendingG {
			t = t.apply(GResolvedMsg{})
		}
	case sidebarActivityTickMsg:
		if msg.gen != t.sidebarTickGen {
			// A stale straggler from a close-then-reopen within one tick
			// interval; a fresh tick already carries the current generation.
			return t, nil
		}
		// This fire already consumed the Cmd that scheduled it, so clear the
		// flag or the re-arm check below reads it as still in flight.
		t.sidebarTickArmed = false
	case toastDismissTickMsg:
		if msg.gen != t.toastGen {
			// A stale straggler from a toast a newer one already replaced.
			return t, nil
		}
		t = t.apply(ToastDismissedMsg{})
	}

	t = t.withModel(refreshPickDecorations(t.m, t.launch, t.pwd, t.heartbeats, t.sidebarActivity, t.sidebarTranscript))
	t = t.reconcileWatches()
	t = t.withModel(syncStale(t.m, t.launch))
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
		// Arm the dismiss timer under a new generation so a still-in-flight
		// timer from the toast this one replaced is dropped as stale instead
		// of clearing this one early.
		t.toastGen++
		cmd = tea.Batch(cmd, toastDismissTick(t.toastGen))
	}
	if t.m.Quitting {
		select {
		case <-t.done:
			// Already closed by an earlier Update call. This check-then-close
			// is race-free only because bubbletea invokes Update serially from
			// its single event-loop goroutine, never concurrently.
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

// View delegates straight to the pure View; Bubble Tea's alt-screen renderer
// paints whatever string comes back across the whole terminal.
func (t teaModel) View() string {
	return viewWithLayout(t.m, t.currentLayout())
}

// pendingGJump returns the mode-specific "jump to first" transition a trailing
// "g" resolves a pending leader to, or nil for a mode with no "gg" leader of
// its own, since each pane keeps its own notion of "first" (issues #1802,
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

// dispatchDefault applies what a mode does when no key matched: most modes do
// nothing, ModeTerminateConfirm declines the terminate, and ModeQuitConfirm
// declines the quit (issue #1790).
func (t teaModel) dispatchDefault(mode Mode) (teaModel, tea.Cmd) {
	switch mode {
	case ModeTerminateConfirm:
		t = t.apply(TerminateCancelledMsg{})
	case ModeQuitConfirm:
		t = t.apply(QuitCancelledMsg{})
	}
	return t, nil
}

// dispatchKey resolves one keypress against mode: mode-specific pre-dispatch
// state (a pending "gg" leader, ModeList's queued-enter notice) runs first,
// then the keymap entry naming (mode, key) has its Action invoked, or
// dispatchDefault when no entry matches. Every handleXKey method below pins
// mode rather than re-deriving it from t.m.ActiveMode() (issue #1790).
func (t teaModel) dispatchKey(mode Mode, msg tea.KeyMsg) (teaModel, tea.Cmd) {
	if onFirst := pendingGJump(mode); onFirst != nil {
		m, consumed := resolvePendingG(t.m, msg, onFirst)
		t = t.withModel(m)
		if consumed {
			return t, nil
		}
		// Any other key cancels the leader without consuming it; that key's
		// own meaning still applies below (issue #1628 AC).
	}
	if mode == ModeList && t.m.QueueEnterNotice != "" {
		t = t.apply(QueueEnterNoticeClearedMsg{})
	}
	if mode == ModeList && t.m.Toast != "" {
		t = t.apply(ToastDismissedMsg{})
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
// right now, in modePrecedence order (model.go, issue #1543).
func (t teaModel) handleKey(msg tea.KeyMsg) (teaModel, tea.Cmd) {
	switch t.m.ActiveMode() {
	case ModeDetailModal:
		return t.handleDetailModalKey(msg)
	case ModeSidebar:
		return t.handleSidebarKey(msg)
	case ModeRebuildOutput:
		return t.handleRebuildOutputKey(msg)
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

// handleHelpKey routes one keypress while the help overlay is open. Only "?"
// and "esc" have ModeHelp entries; everything else, a quit keystroke included,
// falls to dispatchDefault's no-op (issues #784, #1543, #1790).
func (t teaModel) handleHelpKey(msg tea.KeyMsg) teaModel {
	t, _ = t.dispatchKey(ModeHelp, msg)
	return t
}

// handleListKey routes one keypress against the plain backlog/queue body,
// ModeList being modePrecedence's last resort. PendingG's "gg" leader and
// QueueEnterNotice's clear-on-any-key run first inside dispatchKey: neither is
// a rival claimant to keyboard ownership, so neither earns a case in handleKey's
// switch above, and both layer on top of ModeList (issues #1543, #1790).
func (t teaModel) handleListKey(msg tea.KeyMsg) (teaModel, tea.Cmd) {
	return t.dispatchKey(ModeList, msg)
}

// handleRebuildOutputKey routes one keypress while ModeRebuildOutput owns the
// keyboard. Its "gg" leader reuses Model.PendingG and resolvePendingG rather
// than a second, pane-scoped leader (issue #1630 AC3, #1790).
func (t teaModel) handleRebuildOutputKey(msg tea.KeyMsg) (teaModel, tea.Cmd) {
	return t.dispatchKey(ModeRebuildOutput, msg)
}

// handleDetailModalKey routes one keypress while the ticket detail modal owns
// the keyboard (issues #1632, #1795, #1790).
func (t teaModel) handleDetailModalKey(msg tea.KeyMsg) (teaModel, tea.Cmd) {
	return t.dispatchKey(ModeDetailModal, msg)
}

// openDetailModal opens iss's fullscreen ticket detail modal instantly from the
// number/title/labels the highlighted Backlog row already holds, then fetches
// the body and blockers asynchronously. A Model.DetailCache hit applies
// synchronously with no fetch, so reopening a visited ticket is instant (#1632).
func (t teaModel) openDetailModal(iss forge.Issue) (teaModel, tea.Cmd) {
	t = t.apply(DetailModalOpenMsg{Number: iss.Number, Title: iss.Title, Labels: iss.Labels})
	if cached, ok := t.m.DetailCache[iss.Number]; ok {
		t = t.apply(DetailModalLoadedMsg{Number: iss.Number, Body: cached.Body, BlockedBy: cached.BlockedBy, Blocks: cached.Blocks})
		return t, nil
	}
	return t, openDetailModalCmd(t.tracker, t.m.All, iss.Number)
}

// handleSidebarKey routes one keypress while ModeSidebar owns the keyboard: the
// sidebar has focus, the terminal is too narrow to dock it, or the operator
// zoomed it (issues #1628, #1629, #1502, #826, #1790).
func (t teaModel) handleSidebarKey(msg tea.KeyMsg) (teaModel, tea.Cmd) {
	return t.dispatchKey(ModeSidebar, msg)
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

// highlightedPick returns the row under Cursor within whichever work Section is
// active, or false when that Section is empty (ADR 0030). Meaningless for
// SectionBacklog, whose rows are issues, not Picks.
func (t teaModel) highlightedPick() (Pick, bool) {
	picks := sectionPicks(t.m, t.m.ActiveSection)
	if len(picks) == 0 {
		return Pick{}, false
	}
	return picks[t.m.Cursor], true
}

// hasTranscript reports whether state's Box ran far enough to leave logs on
// disk (issue #845). PickFailed's inclusion postdates #845's AC text, which
// predates PickFailed itself (#705), and #992 confirmed it correct.
func hasTranscript(state PickState) bool {
	switch state {
	case PickRunning, PickSettled, PickTerminated, PickFailed:
		return true
	}
	return false
}

// openSidebarCmd loads number's Activity feed and rendered transcript in the
// background as one SidebarLoadedMsg, so neither sidebar view needs further
// I/O. A pick can read as Running a moment before its first log write lands, so
// checking LogPaths first picks ActivityFeed's graceful-empty case over
// DrillIn's "no logs found" error; orphan adds a Notice there (#786, #1621).
func openSidebarCmd(launch *Launcher, pwd, number, title string, orphan bool) tea.Cmd {
	return func() tea.Msg {
		var drv driver.Driver
		if launch != nil {
			drv = launch.Driver()
		}
		if drv == nil {
			return SidebarLoadedMsg{Number: number, Title: title, Err: fmt.Errorf("no Driver available for this session")}
		}
		activity := ActivityFeed(drv, pwd, number)
		if len(dispatch.LogPaths(pwd, number)) == 0 {
			msg := SidebarLoadedMsg{Number: number, Title: title, Activity: activity}
			if orphan {
				msg.Notice = "no local logs for this dispatch"
			}
			return msg
		}
		// DrillIn always returns a DrillInMsg; the type assertion can't fail.
		dm, _ := DrillIn(drv, pwd, number).(DrillInMsg)
		return SidebarLoadedMsg{Number: number, Title: title, Activity: activity, Rendered: dm.Rendered, Raw: dm.Raw, TranscriptErr: dm.Err}
	}
}

// handleTerminateConfirmKey routes one keypress while ModeTerminateConfirm is
// armed; dispatchDefault declines the terminate for any key the keymap doesn't
// name (ADR 0024, issues #745, #748, #1215, #649, #785, #1790).
func (t teaModel) handleTerminateConfirmKey(msg tea.KeyMsg) teaModel {
	t, _ = t.dispatchKey(ModeTerminateConfirm, msg)
	return t
}

// handleQuitConfirmKey routes one keypress while ModeQuitConfirm is armed;
// dispatchDefault declines the quit, "s" included, for any key the keymap
// doesn't name (ADR 0023, issues #651, #822, #1790).
func (t teaModel) handleQuitConfirmKey(msg tea.KeyMsg) teaModel {
	t, _ = t.dispatchKey(ModeQuitConfirm, msg)
	return t
}

// isLive reports whether num has an actual live Dispatch to reclaim. ADR 0024
// scopes Terminate to "claim to verdict", which on this Queue is exactly
// PickRunning; a never-picked backlog row, or a pick still queued, held, or
// claiming, has nothing to terminate.
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

// highlightedNumber returns the cursor's highlighted issue number in whichever
// list the active Section shows (Visible() for SectionBacklog, that Section's
// own Picks otherwise), or "" when the list is empty (ADR 0030, issue #1500).
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

// terminateTarget resolves the issue number "X" acts on: whichever row is drawn
// with ">" (view.go) in the active Section. isLive gates whether that row has
// anything to terminate, so a non-running row is a harmless no-op rather than a
// separate case here (issues #1500, #997).
func (t teaModel) terminateTarget() string {
	return t.highlightedNumber()
}

// alreadyActive reports whether num already has a non-terminal row. Queue's
// row-scan helpers assume at most one non-terminal row per issue number, so
// landing a second leaves the older row stuck forever and can hang the drain
// loop (issue #785 review). Reads Model.Picks alone: every pick path lands its
// snapshot there synchronously, so a stale read can't happen (issue #1542).
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

// pickAllReady picks every issue currently Dispatchable on the tracker in one
// bulk gesture, via the "P" key (#647 AC3, issue #1838). Each landed pick's
// snapshot applies to Model.Picks immediately, not batched to the end of the
// loop, so a later iteration's alreadyActive check sees the picks this same
// gesture already landed (issue #1542).
func (t teaModel) pickAllReady() teaModel {
	for _, msg := range PickAllReady(t.tracker) {
		if queued, ok := msg.(PickQueuedMsg); ok && t.alreadyActive(queued.Number) {
			continue
		}
		if t.launch != nil {
			t = t.apply(QueueSnapshotMsg{Picks: t.launch.Land(msg)})
		} else {
			t = t.apply(msg)
		}
	}
	if t.launch != nil {
		t.launch.tryLaunch(t.tracker, t.pwd)
	}
	return t
}

// unpickHighlighted retracts the highlighted issue's queued pick, a pure
// session-queue edit with no tracker interaction (ADR 0023). Launcher.Unpick
// refuses to drop anything past PickQueued/PickHeld, so sending it is safe even
// when the issue never queued or already launched. A nil Launcher edits
// Model.Picks directly.
func (t teaModel) unpickHighlighted() teaModel {
	num := t.highlightedNumber()
	if num == "" {
		return t
	}
	if t.launch != nil {
		t = t.apply(QueueSnapshotMsg{Picks: t.launch.Unpick(num)})
		return t
	}
	t = t.apply(UnpickMsg{Number: num})
	return t
}

// hasPickNumber reports whether picks carries a row for num, in any state. A
// row only leaves Model.Picks when a Remove call drops it (Queue never purges a
// terminal row on its own), so comparing this before and after an unpick tells
// unpickDetailModalIssue whether that call really removed something (#1836).
func hasPickNumber(picks []Pick, num string) bool {
	for _, p := range picks {
		if p.Number == num {
			return true
		}
	}
	return false
}

// unpickDetailModalIssue retracts the open detail modal's displayed issue's
// queued pick, keyed by DetailModal.Number rather than the Backlog cursor
// (issue #1835). An issue with nothing to unpick is a no-op: hasPickNumber's
// before/after comparison reports no removal, so the modal stays open exactly
// as it does for a rejected pick (issue #1836).
func (t teaModel) unpickDetailModalIssue() teaModel {
	dm := t.m.DetailModal
	if dm == nil {
		return t
	}
	existed := hasPickNumber(t.m.Picks, dm.Number)
	if t.launch != nil {
		t = t.apply(QueueSnapshotMsg{Picks: t.launch.Unpick(dm.Number)})
	} else {
		t = t.apply(UnpickMsg{Number: dm.Number})
	}
	if existed && !hasPickNumber(t.m.Picks, dm.Number) {
		t = t.apply(DetailModalCloseMsg{})
	}
	return t
}

// quitOrConfirmMsg picks QuitRequestedMsg over QuitMsg whenever live Dispatches
// exist, so any quit path arms the drain/terminate-all/stay confirm instead of
// exiting outright (issue #1216, ADR 0023).
func (t teaModel) quitOrConfirmMsg() Msg {
	if t.launch != nil && len(t.launch.LiveIssues()) > 0 {
		return QuitRequestedMsg{}
	}
	return QuitMsg{}
}

// landPick promotes num/title through Launcher.Pick and applies the snapshot in
// the same Update cycle, the shared tail pickHighlighted and
// pickDetailModalIssue both land through so their two pick sources can't drift
// apart. Launcher.Pick's trackerFor routes a KindResearch pick onto
// ResearchTracker when one is wired (issues #1839, #1836).
func (t teaModel) landPick(num, title string, kind Kind) (teaModel, Msg) {
	if t.launch == nil {
		// No Launcher means no trackerFor, so a KindResearch pick here still
		// promotes on t.tracker's own label family. Harmless: production
		// always supplies a Launcher, and only tests reach this branch.
		msg := PickIssue(t.tracker, num, title, kind)
		t = t.apply(msg)
		return t, msg
	}
	msg, picks := t.launch.Pick(t.tracker, num, title, kind)
	t = t.apply(QueueSnapshotMsg{Picks: picks})
	if _, ok := msg.(PickQueuedMsg); ok {
		t.launch.tryLaunch(t.tracker, t.pwd)
	}
	return t, msg
}

// pickHighlighted promotes the cursor's highlighted issue through landPick, the
// keypress form of ADR 0023's pick-is-the-launch-button rule. A no-op outside
// SectionBacklog, where Cursor indexes a work Section's Picks and not the
// backlog (ADR 0030, issue #1500), and a no-op when the issue already has an
// active row (issue #785 review).
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

// pickDetailModalIssue promotes the open detail modal's displayed issue through
// landPick, keyed by DetailModal.Number/Title rather than the Backlog cursor, so
// a background refresh reordering rows underneath the modal can't redirect the
// pick onto a different issue (issue #1835). A pick landPick refuses lands as a
// PickDissolvedMsg row; only a successful PickQueuedMsg closes the modal (#1836).
func (t teaModel) pickDetailModalIssue(kind Kind) teaModel {
	dm := t.m.DetailModal
	if dm == nil {
		return t
	}
	if t.alreadyActive(dm.Number) {
		return t
	}
	var msg Msg
	t, msg = t.landPick(dm.Number, dm.Title, kind)
	if _, ok := msg.(PickQueuedMsg); ok {
		t = t.apply(DetailModalCloseMsg{})
	}
	return t
}

// handleFilterKey routes one keypress while ModeFilterEdit owns the keyboard.
// This is the one mode whose dispatch is keyed by msg.Type rather than
// msg.String(), through dispatchKey's filterEditKeyName translation (#1790).
func (t teaModel) handleFilterKey(msg tea.KeyMsg) teaModel {
	t, _ = t.dispatchKey(ModeFilterEdit, msg)
	return t
}

// refreshCmd re-queries tracker for the backlog in the background: the "R" key,
// the initial load, and both async signals funnel through this one Cmd so their
// result lands on Model identically.
func refreshCmd(tracker forge.IssueTracker) tea.Cmd {
	return func() tea.Msg {
		return Refresh(tracker)
	}
}

// pollTick arms the next background-poll tick.
func pollTick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return pollTickMsg{} })
}

// refreshPickDecorations recomputes every Model.Picks row's live Heartbeat and
// Age in place, so every render shows current values (#647 AC2). It never
// touches the launcher's queue: Model.Picks is already the queue's authoritative
// mirror (issue #1542). It also installs the live parallelism cap and count,
// which no Msg carries (issue #653). A nil launch leaves m untouched.
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
		// PassState reads a JSON manifest file, not drv's Driver-specific log
		// parser, so it is gated only on PickRunning, not on drv != nil the way
		// Heartbeat is above (issue #2983).
		if picks[i].State == PickRunning {
			picks[i].PassState = RunningPassState(pwd, picks[i].Number)
		}
		if !picks[i].QueuedAt.IsZero() {
			picks[i].Age = formatAge(time.Since(picks[i].QueuedAt))
		}
	}
	m = Update(m, QueueSnapshotMsg{Picks: picks})
	// The len(m.OrphanHeartbeats) > 0 half of this guard matters once
	// OrphanNums drops to empty: without it the branch is skipped and the
	// previous tick's map is never replaced, leaving a stale heartbeat parked
	// in Model.OrphanHeartbeats indefinitely.
	if drv != nil && (len(m.OrphanNums) > 0 || len(m.OrphanHeartbeats) > 0) {
		// An orphan row has no Pick of its own for the loop above to reach, so
		// the same heartbeat machinery is keyed straight off OrphanNums instead
		// of a Pick slice (issue #1621).
		orphanHeartbeats := make(map[string]string, len(m.OrphanNums))
		for _, num := range m.OrphanNums {
			orphanHeartbeats[num] = heartbeats.RunningHeartbeat(drv, pwd, num)
		}
		m = Update(m, OrphanHeartbeatsMsg{Heartbeats: orphanHeartbeats})
	}
	// An orphan-flagged sidebar has no Pick of its own to read a running state
	// off, so isRunningNumber alone would starve it of the live tail a
	// session-launched Dispatch gets (issue #1621). Refresh's stat-based cache
	// keeps this cheap on every no-op call between log writes (issue #731).
	if m.Sidebar != nil && drv != nil && (isRunningNumber(picks, m.Sidebar.Number) || m.IsOrphan(m.Sidebar.Number)) {
		if activity, ok := sidebarActivity.Refresh(drv, pwd, m.Sidebar.Number); ok {
			m = Update(m, SidebarActivityMsg{Number: m.Sidebar.Number, Activity: activity})
		}
		// DrillIn re-reads and re-renders every pass log, heavier than the
		// Activity feed's single-file read, so it runs only while the operator
		// is looking at the Transcript view (issue #1736 AC1/AC3).
		if m.Sidebar.ShowTranscript {
			if rendered, raw, ok := sidebarTranscript.Refresh(drv, pwd, m.Sidebar.Number); ok {
				m = Update(m, SidebarTranscriptMsg{Number: m.Sidebar.Number, Rendered: rendered, Raw: raw})
			}
		}
	}
	return Update(m, CapMsg{Cap: launch.Cap(), Live: launch.Live()})
}

// isRunningNumber reports whether picks carries number in PickRunning state, the
// gate on refreshing the open sidebar's Activity feed: a settled, terminated, or
// failed Dispatch's logs never change again (issue #1502).
func isRunningNumber(picks []Pick, number string) bool {
	for _, p := range picks {
		if p.Number == number {
			return p.State == PickRunning
		}
	}
	return false
}

// syncStale installs launch's live image-freshness/rebuild state onto m, so
// every render reflects a stale verdict a background drain saw, or a rebuild's
// progress (issue #652). A nil launch leaves m untouched.
func syncStale(m Model, launch *Launcher) Model {
	if launch == nil {
		return m
	}
	return Update(m, StaleStatusMsg{RebuildStatus: launch.StaleStatus()})
}

// orphanDetectCmd reports every issue OrphanedIssues finds running with no live
// goroutine in this process, whether from a crashed prior session or a competing
// process that legitimately owns those boxes (issues #651, #822). It never
// adopts them: an automatic adopt raced a second settle against whichever
// process owns the box (issue #1619). A failed call degrades to "no orphans".
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

// adoptOrphanCmd adopts num through launch's RecoverFn in the background, on the
// operator's explicit gesture (issue #1619). A failure surfaces through
// OrphanRecoveryMsg (issue #1218) and changes nothing else. Either result clears
// num out of Model.AdoptingOrphans: this call is the in-flight window itself, so
// gating on the orphan flag alone would let a second "A" race the first.
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
// translating one arrival into a refreshSignalMsg; nil when launch is nil. It
// also selects on done, closed by Update's Quitting choke point, since bubbletea
// can't cancel a Cmd goroutine once spawned (issue #823): without it, a session
// that quits before signaling a refresh leaks this goroutine for the process.
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
