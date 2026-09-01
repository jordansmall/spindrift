// Package console renders the launcher's full-screen Bubble Tea program.
// teaModel is a thin adapter: Update translates tea.KeyMsg and the async
// signals (background poll, launch-refresh) into the same console Msg values
// the pure Update already handles; View delegates straight to the pure View.
// Keys act on the cursor's highlighted row. Enter is context-sensitive: Pick
// on a focused Backlog row, open the highlighted work row's sidebar when it
// has a Transcript.
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
// transcript's scroll offset. Fixed, unlike the body's own page jump
// (sectionPageSize), which derives from the live viewport height.
const fixedPaneScrollDelta = 10

// teaModel carries the I/O seams (tracker, pwd, launch) the pure Update
// itself never touches.
type teaModel struct {
	m            Model
	tracker      forge.IssueTracker
	pwd          string
	launch       *Launcher
	pollInterval time.Duration
	// The three caches below skip re-reading unchanged on-disk state on
	// refreshPickDecorations' per-Msg refresh. All are pointers/reference
	// types so they survive Update's value-receiver copies of teaModel.
	heartbeats      *HeartbeatCache
	sidebarActivity *SidebarActivityCache
	// sidebarTranscript re-derives while ShowTranscript is active, so the
	// Transcript view live-tails instead of freezing at open time.
	sidebarTranscript *SidebarTranscriptCache
	// sidebarTickArmed keeps Update to at most one in-flight
	// sidebarActivityTick instead of stacking a second on the first.
	sidebarTickArmed bool
	// sidebarTickGen and toastGen stamp each armed tea.Tick. A tea.Tick
	// can't be cancelled once scheduled, so a close-then-reopen (or a
	// replaced toast) leaves a stale timer in flight; the fired Msg carries
	// the generation that armed it and Update drops a superseded one rather
	// than re-arming a duplicate chain or clearing the current toast.
	sidebarTickGen uint64
	toastGen       uint64
	// watcher fires a logWriteMsg on a write to any watchedPaths entry, so
	// the heartbeat refresh runs within moments of new bytes landing instead
	// of waiting for the next pollTickMsg. nil for a launch-less session or
	// when fsnotify couldn't acquire a platform handle — the console stays
	// correct either way, just back to the slower per-Msg/poll cadence.
	watcher *fsnotify.Watcher
	// watchedPaths mirrors watcher's own watch set, which fsnotify exposes
	// no cheaper query for than WatchList's full slice, so reconcileWatches
	// can diff against it in place.
	watchedPaths map[string]struct{}
	// done is closed exactly once, at the Quitting choke point below, to
	// unblock waitRefreshSignal's goroutine — bubbletea can't cancel a Cmd
	// goroutine, so the closure selects on this instead of blocking on
	// Launcher.Refreshes() forever.
	done chan struct{}
}

// newTeaModel builds the tea layer's starting state. Only the
// dogfood-competition notice is checked synchronously (a cheap pid-file read
// plus signal-0 probe); the initial backlog load, background poll, and
// launch-refresh listener all start as Cmds from Init.
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

// Run drives the console's full-screen Bubble Tea program to completion.
// launch is nil for a launch-less session; production wires a real Launcher.
func Run(tracker forge.IssueTracker, pwd string, in io.Reader, out io.Writer, launch *Launcher) error {
	p := tea.NewProgram(newTeaModel(tracker, pwd, launch), tea.WithInput(in), tea.WithOutput(out), tea.WithAltScreen())
	_, err := p.Run()
	if launch != nil {
		launch.Wait()
	}
	return err
}

// pollTickMsg is the background-poll tick, re-armed on every arrival so the
// poll continues for the program's whole lifetime.
type pollTickMsg struct{}

// refreshSignalMsg translates Launcher.Refreshes() firing — the session's own
// tracker write (a claim, a settle, a promotion) asking for an out-of-band
// refresh. picks carries the queue snapshot recorded at the moment it fired,
// with hasPicks distinguishing "nothing pending" from a genuinely empty
// queue, so the tea side never pulls Queue itself.
type refreshSignalMsg struct {
	picks    []Pick
	hasPicks bool
}

// toastDismissDelay is how long a pick-transition toast stays visible — long
// enough to read a short "#NN started: title" line, short enough that it
// never lingers into the next transition's own toast.
const toastDismissDelay = 4 * time.Second

// toastDismissTickMsg is the one-shot auto-dismiss signal for a
// pick-transition toast. gen pins it to the toastGen that armed it.
type toastDismissTickMsg struct{ gen uint64 }

func toastDismissTick(gen uint64) tea.Cmd {
	return tea.Tick(toastDismissDelay, func(time.Time) tea.Msg { return toastDismissTickMsg{gen: gen} })
}

// sidebarActivityTickInterval is how often the docked sidebar's live-tail
// tick fires — independent of keypresses and of the pollTick backlog cadence,
// so an open sidebar advances while the operator sits and watches.
const sidebarActivityTickInterval = time.Second

// sidebarActivityTickMsg is the dedicated live-tail signal; landing it is
// enough to reach Update's refreshPickDecorations call. gen pins it to the
// sidebarTickGen that armed it.
type sidebarActivityTickMsg struct{ gen uint64 }

func sidebarActivityTick(gen uint64) tea.Cmd {
	return tea.Tick(sidebarActivityTickInterval, func(time.Time) tea.Msg { return sidebarActivityTickMsg{gen: gen} })
}

// sidebarActivityLive reports whether the docked sidebar is open on a
// Dispatch whose Activity feed can still change — the same gate
// refreshPickDecorations applies, so the live-tail tick arms and disarms in
// lockstep with the refresh it exists to drive.
func sidebarActivityLive(m Model) bool {
	return m.Sidebar != nil && (isRunningNumber(m.Picks, m.Sidebar.Number) || m.IsOrphan(m.Sidebar.Number))
}

// gChordTimeout is how long a lone "g" waits for a trailing "g" before the
// leader window cancels — long enough that a deliberate two-key "gg" always
// lands within it, short enough that a lone "g" still reads as responsive.
const gChordTimeout = 200 * time.Millisecond

// gChordTimeoutMsg signals that "g"'s leader window elapsed with no trailing
// "g", cancelling the still-pending leader.
type gChordTimeoutMsg struct{}

func gChordTick() tea.Cmd {
	return tea.Tick(gChordTimeout, func(time.Time) tea.Msg { return gChordTimeoutMsg{} })
}

// resolvePendingG resolves an armed "gg" leader against msg. A no-op when
// PendingG isn't set. Otherwise it clears the leader and, if msg is the
// second "g", applies onFirst and reports the key consumed. Any other key
// clears the leader but is reported unconsumed: that key's own binding still
// applies, so the caller falls through rather than returning.
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
// startup — the only outside read of the queue's full contents past
// construction. Every later transition reaches the Model synchronously
// through Pick/Unpick/TerminateAsync's return value or asynchronously through
// a pushed refreshSignalMsg; there is no per-message pull that would
// otherwise catch Model.Picks up with a queue that started non-empty.
func initialQueueSyncCmd(launch *Launcher) tea.Cmd {
	return func() tea.Msg {
		return QueueSnapshotMsg{Picks: launch.Snapshot()}
	}
}

// Update is the tea layer's whole adapter surface: it translates every Bubble
// Tea message (key presses, resizes) and internal signal into console Msg
// values the pure Update handles, then re-syncs the launcher's live
// Queue/stale state onto the Model on every render.
func (t teaModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	prevToast := t.m.Toast
	switch msg := msg.(type) {
	case tea.KeyMsg:
		t, cmd = t.handleKey(msg)
	case tea.WindowSizeMsg:
		t.m = Update(t.m, SizeChangedMsg{Width: msg.Width, Height: msg.Height})
	case IssuesLoadedMsg:
		t.m = Update(t.m, msg)
	case SidebarLoadedMsg:
		t.m = Update(t.m, msg)
	case DetailModalLoadedMsg:
		t.m = Update(t.m, msg)
	case OrphanRecoveryMsg:
		t.m = Update(t.m, msg)
	case OrphanDetectedMsg:
		t.m = Update(t.m, msg)
	case OrphanAdoptedMsg:
		t.m = Update(t.m, msg)
	case QueueSnapshotMsg:
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
			return t, nil // stale straggler; see sidebarTickGen
		}
		// This fire already consumed the Cmd that scheduled it — clear the
		// flag so the re-arm check below issues a fresh one instead of
		// reading it as "still in flight" and skipping re-arm entirely.
		t.sidebarTickArmed = false
	case toastDismissTickMsg:
		if msg.gen != t.toastGen {
			return t, nil // stale straggler; see toastGen
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
		// Arm under a new generation so a still-in-flight timer from the
		// toast this one replaced is dropped instead of clearing this one
		// early.
		t.toastGen++
		cmd = tea.Batch(cmd, toastDismissTick(t.toastGen))
	}
	if t.m.Quitting {
		select {
		case <-t.done:
			// Already closed by an earlier Update call. This check-then-close
			// is race-free only because bubbletea invokes Update serially
			// from its single event-loop goroutine, never concurrently.
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

// View delegates straight to the pure View — the tea layer adds no rendering
// of its own.
func (t teaModel) View() string {
	return View(t.m)
}

// pendingGJump returns the mode-specific "jump to first" transition a
// trailing "g" resolves a pending leader to, or nil for a mode with no "gg"
// leader of its own — one per pane, since each keeps its own "first".
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

// dispatchDefault handles a key the keymap doesn't name for mode. Most modes
// do nothing; the two confirm modes decline their prompt.
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
// then the keymap entry naming (mode, key) is looked up and its Action
// invoked — or dispatchDefault when no entry matches. Every handleXKey method
// below wraps this, pinning mode rather than re-deriving it from
// t.m.ActiveMode(): handleKey's routing switch already made that choice.
func (t teaModel) dispatchKey(mode Mode, msg tea.KeyMsg) (teaModel, tea.Cmd) {
	if onFirst := pendingGJump(mode); onFirst != nil {
		m, consumed := resolvePendingG(t.m, msg, onFirst)
		t.m = m
		if consumed {
			return t, nil
		}
		// Any other key cancels the leader without consuming it — that key's
		// own meaning still applies below.
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
// right now (modePrecedence, model.go).
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

// handleHelpKey routes one keypress while the help overlay is open. Only "?"
// and "esc" are bound; everything else, a quit keystroke included, falls to
// dispatchDefault's no-op.
func (t teaModel) handleHelpKey(msg tea.KeyMsg) teaModel {
	t, _ = t.dispatchKey(ModeHelp, msg)
	return t
}

// handleListKey routes one keypress against the plain backlog/queue body,
// modePrecedence's last resort. PendingG's "gg" leader and QueueEnterNotice
// layer on top of ModeList rather than earning a case in handleKey: neither
// is a rival claimant to keyboard ownership.
func (t teaModel) handleListKey(msg tea.KeyMsg) (teaModel, tea.Cmd) {
	return t.dispatchKey(ModeList, msg)
}

// handleRebuildOutputKey routes one keypress while ModeRebuildOutput owns the
// keyboard. Its "gg" leader reuses Model.PendingG through dispatchKey's
// pendingGJump pre-step rather than a second, pane-scoped leader.
func (t teaModel) handleRebuildOutputKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	t, cmd := t.dispatchKey(ModeRebuildOutput, msg)
	return t.m, cmd
}

// handleDetailModalKey routes one keypress while the ticket detail modal is
// open.
func (t teaModel) handleDetailModalKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	t, cmd := t.dispatchKey(ModeDetailModal, msg)
	return t.m, cmd
}

// openDetailModal opens iss's fullscreen ticket detail modal instantly, with
// the number/title/labels the highlighted Backlog row already holds, then
// kicks off the async body/blocker fetch — or applies a Model.DetailCache hit
// synchronously, so reopening a ticket visited this session is instant.
func (t teaModel) openDetailModal(iss forge.Issue) (teaModel, tea.Cmd) {
	t.m = Update(t.m, DetailModalOpenMsg{Number: iss.Number, Title: iss.Title, Labels: iss.Labels})
	if cached, ok := t.m.DetailCache[iss.Number]; ok {
		t.m = Update(t.m, DetailModalLoadedMsg{Number: iss.Number, Body: cached.Body, BlockedBy: cached.BlockedBy, Blocks: cached.Blocks})
		return t, nil
	}
	return t, openDetailModalCmd(t.tracker, t.m.All, iss.Number)
}

// handleSidebarKey routes one keypress while ModeSidebar owns the keyboard:
// the sidebar has focus, the terminal is too narrow to dock it, or the
// operator zoomed it.
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
// is active, or false when that Section is empty. Meaningless for
// SectionBacklog, whose rows are issues, not Picks; callers only reach for
// this once ActiveSection is known to be a work Section.
func (t teaModel) highlightedPick() (Pick, bool) {
	picks := sectionPicks(t.m, t.m.ActiveSection)
	if len(picks) == 0 {
		return Pick{}, false
	}
	return picks[t.m.Cursor], true
}

// hasTranscript reports whether state has an actual Transcript to drill
// into: running, settled, terminated, and failed all have a Box that ran and
// left logs on disk; queued, claiming, held, and dissolved never launched, so
// Enter is a no-op on those rows.
func hasTranscript(state PickState) bool {
	switch state {
	case PickRunning, PickSettled, PickTerminated, PickFailed:
		return true
	}
	return false
}

// openSidebarCmd loads number's Activity feed and rendered transcript in the
// background as one SidebarLoadedMsg, so neither the default Activity view
// nor the Transcript toggle needs further I/O once it lands. A session with
// no Driver renders a graceful error rather than dereferencing nil. Only the
// Activity feed advances afterward; the Transcript is re-read on reopen.
//
// A pick can read as Running a moment before its Box's first log write lands.
// DrillIn calls that an error ("no logs found") while ActivityFeed treats it
// as a graceful-empty case; checking LogPaths once here picks ActivityFeed's
// contract rather than surfacing a spurious failure.
//
// orphan makes the no-logs case carry a Notice instead of staying blank: an
// orphan-flagged Dispatch with nothing on disk is a standing state the
// operator opened deliberately, not that split-second race.
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
// armed (ADR 0024). Any key the keymap doesn't name declines the terminate.
func (t teaModel) handleTerminateConfirmKey(msg tea.KeyMsg) teaModel {
	t, _ = t.dispatchKey(ModeTerminateConfirm, msg)
	return t
}

// handleQuitConfirmKey routes one keypress while ModeQuitConfirm is armed
// (ADR 0023). Any key the keymap doesn't name, "s" included, declines.
func (t teaModel) handleQuitConfirmKey(msg tea.KeyMsg) teaModel {
	t, _ = t.dispatchKey(ModeQuitConfirm, msg)
	return t
}

// isLive reports whether num has an actual live Dispatch to reclaim. ADR
// 0024's Terminate is scoped to "claim to verdict", which on this Queue is
// exactly PickRunning; a never-picked backlog row, or a pick still
// queued/held/claiming, has nothing to terminate.
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
// whichever list the active Section shows — Visible() for SectionBacklog, the
// active work Section's own Picks otherwise — or "" when that list is empty.
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
// is actually drawn with ">" in the active Section. isLive then gates whether
// that row has anything to terminate, so standing on a non-running row is a
// harmless no-op rather than a separate case here.
func (t teaModel) terminateTarget() string {
	return t.highlightedNumber()
}

// alreadyActive reports whether num already has a non-terminal row. Queue's
// row-scan helpers assume at most one non-terminal row per issue number;
// landing a second leaves the older row stuck forever and can hang the drain
// loop. A terminal row never blocks a fresh pick — that's ADR 0024's
// legitimate re-pick/adopt path.
//
// Reads Model.Picks alone, never the launcher's queue: Pick/Unpick/
// TerminateAsync land their snapshot synchronously in the same Update cycle
// as the keypress, so a stale pre-drain read is structurally impossible.
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
// bulk gesture. An issue already active from an earlier pick is skipped. Each
// landed pick's snapshot applies to Model.Picks immediately, not batched to
// the end, so a later iteration's alreadyActive check still sees every pick
// this same gesture already landed.
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
// any — a pure session-queue edit with no tracker interaction (ADR 0023).
// Launcher.Unpick refuses to drop anything past PickQueued/PickHeld, so this
// is safe to send even for an issue that never queued or already launched. A
// nil Launcher edits Model.Picks directly.
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

// hasPickNumber reports whether picks carries a row for num, in any state.
// The only way a row leaves Model.Picks is a Remove call actually dropping it
// — Queue never purges a terminal row on its own — so comparing this before
// and after an unpick tells the caller whether anything was really removed,
// immune to Model.Picks lagging the live Queue by a background claim.
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
// (see pickDetailModalIssue). A nil DetailModal is a defensive no-op; so is
// an issue with nothing to unpick — hasPickNumber's before/after comparison
// reports no removal and the modal stays open, like a rejected pick.
func (t teaModel) unpickDetailModalIssue() teaModel {
	dm := t.m.DetailModal
	if dm == nil {
		return t
	}
	existed := hasPickNumber(t.m.Picks, dm.Number)
	if t.launch != nil {
		t.m = Update(t.m, QueueSnapshotMsg{Picks: t.launch.Unpick(dm.Number)})
	} else {
		t.m = Update(t.m, UnpickMsg{Number: dm.Number})
	}
	if existed && !hasPickNumber(t.m.Picks, dm.Number) {
		t.m = Update(t.m, DetailModalCloseMsg{})
	}
	return t
}

// quitOrConfirmMsg picks QuitRequestedMsg over QuitMsg whenever live
// Dispatches exist, so any quit path arms the drain/terminate-all/stay
// confirm instead of exiting outright (ADR 0023).
func (t teaModel) quitOrConfirmMsg() Msg {
	if t.launch != nil && len(t.launch.LiveIssues()) > 0 {
		return QuitRequestedMsg{}
	}
	return QuitMsg{}
}

// landPick promotes num/title through Launcher.Pick and applies the fresh
// snapshot in the same Update cycle — the shared tail pickHighlighted and
// pickDetailModalIssue both land through, so the cursor-row and open-modal
// pick sources can never drift apart on how a target gets queued. kind
// selects the Dispatch kind; Launcher.Pick's trackerFor routes a KindResearch
// pick onto ResearchTracker when one is wired. A nil Launcher promotes
// through PickIssue directly onto Model.Picks but never queues or launches.
func (t teaModel) landPick(num, title string, kind Kind) (teaModel, Msg) {
	if t.launch == nil {
		// Without a Launcher there is no trackerFor, so a KindResearch pick
		// still promotes on t.tracker's own label family, tagged with the
		// kind regardless. Harmless: production always supplies a Launcher,
		// so only tests exercising bare Pick/Unpick bookkeeping land here.
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
// no-op outside SectionBacklog, where Cursor indexes a work Section's Picks
// rather than the backlog (ADR 0030 makes Backlog the sole pick source), and
// a no-op when the issue already has an active row.
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

// pickDetailModalIssue promotes the open detail modal's displayed issue
// through landPick, keyed by DetailModal.Number/Title rather than the Backlog
// cursor, so a background refresh reordering rows underneath the modal can
// never redirect the pick onto a different issue. A nil DetailModal
// (defensive only) and an already-active pick are both no-ops. A pick
// landPick refuses lands as a PickDissolvedMsg row and leaves the modal open
// so the operator can see why; only a successful PickQueuedMsg closes it.
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
		t.m = Update(t.m, DetailModalCloseMsg{})
	}
	return t
}

// handleFilterKey routes one keypress while ModeFilterEdit owns the keyboard:
// Enter applies (keeping the already-live-narrowed Filter), Esc reverts to
// the pre-edit Filter, Backspace trims one rune, and any other printable key
// appends. This is the one mode dispatched by msg.Type rather than
// msg.String() — see dispatchKey's filterEditKeyName translation.
func (t teaModel) handleFilterKey(msg tea.KeyMsg) teaModel {
	t, _ = t.dispatchKey(ModeFilterEdit, msg)
	return t
}

// refreshCmd re-queries tracker for the backlog in the background. The "R"
// key, the initial load, and both async signals funnel through this one Cmd
// so their result lands on Model identically.
func refreshCmd(tracker forge.IssueTracker) tea.Cmd {
	return func() tea.Msg {
		return Refresh(tracker)
	}
}

func pollTick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return pollTickMsg{} })
}

// refreshPickDecorations recomputes every Model.Picks row's live Heartbeat
// and Age fields in place. It never touches the launcher's queue: Model.Picks
// is already the queue's authoritative mirror, so this only decorates rows
// already there. A nil launch leaves m untouched.
//
// This runs on every tea.Msg, not just a render tick, so most calls see the
// same on-disk bytes as last time — hence the caches. The open sidebar's
// Activity feed refreshes the same way (ADR 0030's live-tail piggybacks this
// per-Msg sync rather than a dedicated timer), scoped to one Dispatch so I/O
// stays bounded however many are running.
//
// It also installs the live parallelism cap and count off the Launcher's
// Limiter: no Msg carries those, so this pull is the only path keeping them
// current.
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
	// The len(m.OrphanHeartbeats) > 0 half of this guard matters once
	// OrphanNums drops to empty: without it the branch is skipped and the
	// previous tick's map is never replaced with an empty one, parking a
	// stale heartbeat in Model.OrphanHeartbeats indefinitely.
	if drv != nil && (len(m.OrphanNums) > 0 || len(m.OrphanHeartbeats) > 0) {
		// An orphan row has no Pick for the loop above to reach — same
		// machinery, keyed straight off OrphanNums instead of a Pick slice.
		orphanHeartbeats := make(map[string]string, len(m.OrphanNums))
		for _, num := range m.OrphanNums {
			orphanHeartbeats[num] = heartbeats.RunningHeartbeat(drv, pwd, num)
		}
		m = Update(m, OrphanHeartbeatsMsg{Heartbeats: orphanHeartbeats})
	}
	// An orphan-flagged sidebar has no Pick to read a running state off, so
	// isRunningNumber alone would starve it of the live tail a
	// session-launched Dispatch gets.
	if m.Sidebar != nil && drv != nil && (isRunningNumber(picks, m.Sidebar.Number) || m.IsOrphan(m.Sidebar.Number)) {
		if activity, ok := sidebarActivity.Refresh(drv, pwd, m.Sidebar.Number); ok {
			m = Update(m, SidebarActivityMsg{Number: m.Sidebar.Number, Activity: activity})
		}
		// Scoped to ShowTranscript on top of the running-or-orphan gate:
		// DrillIn re-reads and re-renders every pass log, far heavier than
		// the Activity feed's single-file read.
		if m.Sidebar.ShowTranscript {
			if rendered, raw, ok := sidebarTranscript.Refresh(drv, pwd, m.Sidebar.Number); ok {
				m = Update(m, SidebarTranscriptMsg{Number: m.Sidebar.Number, Rendered: rendered, Raw: raw})
			}
		}
	}
	return Update(m, CapMsg{Cap: launch.Cap(), Live: launch.Live()})
}

// isRunningNumber reports whether picks carries number in PickRunning state —
// the gate on refreshing the open sidebar's Activity feed, since a
// settled/terminated/failed Dispatch's logs never change again.
func isRunningNumber(picks []Pick, number string) bool {
	for _, p := range picks {
		if p.Number == number {
			return p.State == PickRunning
		}
	}
	return false
}

// syncStale installs launch's live image-freshness/rebuild state onto m, so
// every render reflects a stale verdict a background drain saw or a rebuild's
// progress. A nil launch leaves m untouched.
func syncStale(m Model, launch *Launcher) Model {
	if launch == nil {
		return m
	}
	return Update(m, StaleStatusMsg{RebuildStatus: launch.StaleStatus()})
}

// orphanDetectCmd reports every issue OrphanedIssues finds running with no
// live goroutine in this process — a crashed prior session, or a competing
// process (a dogfood loop, a second console) that legitimately owns boxes
// this one has no goroutine for.
//
// It never adopts them: a runner-visible sandbox is not proof of abandonment,
// and an automatic adopt raced a second settle against whichever process
// actually owns the box. Detection is best-effort — a failed call degrades to
// "no orphans found". nil (no Cmd) when launch is nil.
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

// adoptOrphanCmd adopts num through launch's RecoverFn in the background — the
// operator's explicit gesture on an orphan-flagged Backlog row. A failure
// surfaces as OrphanRecoveryMsg; a success returns OrphanAdoptedMsg, clearing
// num's orphan flag. nil (no Cmd) when launch or RecoverFn is nil.
//
// Either result also clears num out of Model.AdoptingOrphans, the in-flight
// mark handleKey sets synchronously before this Cmd starts — necessary
// because this call is the in-flight window itself: gating on the orphan flag
// alone would let a second "A" pressed before this goroutine returns fire a
// concurrent RecoverFn, racing the first over the same PR.
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
// is nil, since there is then no Queue whose writes could signal one.
//
// It also selects on done, closed by Update's Quitting choke point: bubbletea
// cannot cancel a spawned Cmd goroutine, so without this a session that quits
// before ever signaling a refresh leaks this goroutine parked on <-ch.
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
