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
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/forge"
)

// defaultPollInterval is the background backlog poll's fixed cadence when a
// Launcher doesn't override it (production always uses this) — slow enough
// to never spend the rate-limit window the session's Agents share (#647 AC5).
const defaultPollInterval = 90 * time.Second

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
	// syncQueue's per-Update refresh (line ~145) skips the ReadFile+reparse
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
	if launch != nil && launch.pollInterval > 0 {
		interval = launch.pollInterval
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
// asking for an out-of-band refresh (#647 AC4).
type refreshSignalMsg struct{}

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

// Init starts the initial backlog load and both async signal sources
// (background poll, launch-refresh) as Cmds — none of them block the
// program's own startup.
func (t teaModel) Init() tea.Cmd {
	cmds := []tea.Cmd{refreshCmd(t.tracker), pollTick(t.pollInterval)}
	if t.launch != nil {
		cmds = append(cmds, waitRefreshSignal(t.launch, t.done), orphanRecoveryCmd(t.launch))
	}
	return tea.Batch(cmds...)
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
	case pollTickMsg:
		if t.launch != nil {
			t.launch.tryLaunch(t.tracker, t.pwd)
		}
		cmd = tea.Batch(refreshCmd(t.tracker), pollTick(t.pollInterval))
	case refreshSignalMsg:
		cmd = tea.Batch(refreshCmd(t.tracker), waitRefreshSignal(t.launch, t.done))
	case pickChordTimeoutMsg:
		if t.m.PendingPick {
			t.m = Update(t.m, PickResolvedMsg{})
			t = t.pickHighlighted()
		}
	}

	t.m = syncQueue(t.m, t.launch, t.pwd, t.heartbeats, t.sidebarActivity)
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

// handleKey translates one keypress into a console Msg and applies it,
// gated by whichever modal state (a focused or fullscreen sidebar, help
// overlay, filter edit) is active — mirroring applyCommand's old
// PendingTerminate/PendingQuit precedence, now keyed off Model's own modal
// fields instead of a line parse. An open sidebar takes precedence over
// everything else whenever it has focus or the terminal is too narrow to
// dock it beside the list (sidebarFits) — in the fullscreen-fallback case
// there is no list on screen to route list-only keys to regardless of
// Model.Focus (ADR 0030, #1501, inherited from #786's drill-in precedence).
func (t teaModel) handleKey(msg tea.KeyMsg) (teaModel, tea.Cmd) {
	if t.m.Sidebar != nil && (t.m.Focus == FocusSidebar || !sidebarFits(t.m) || t.m.SidebarZoom) {
		t.m = t.handleSidebarKey(msg)
		return t, nil
	}
	if t.m.ShowRebuildOutput {
		t.m = t.handleRebuildOutputKey(msg)
		return t, nil
	}
	if t.m.ShowHelp {
		if s := msg.String(); s == "?" || s == "esc" {
			t.m = Update(t.m, HelpToggleMsg{})
		}
		return t, nil
	}
	if t.m.FilterEditing {
		return t.handleFilterKey(msg), nil
	}
	if t.m.PendingTerminate != "" {
		return t.handleTerminateConfirmKey(msg), nil
	}
	if t.m.PendingQuit {
		return t.handleQuitConfirmKey(msg), nil
	}
	if t.m.PendingPick {
		t.m = Update(t.m, PickResolvedMsg{})
		switch s := msg.String(); s {
		case "a":
			return t.pickAllReady(), nil
		case "q", "ctrl+c":
			// The universal quit keystroke must never be swallowed by the
			// chord: resolve the pending pick (matching any other non-"a"
			// key) and then still quit — but the pick just landed may
			// itself be live now, so gate on LiveIssues() the same way the
			// main quit handler below does, not an unconditional QuitMsg
			// (issue #1216).
			t = t.pickHighlighted()
			t.m = Update(t.m, t.quitOrConfirmMsg())
			return t, nil
		default:
			// Any other key resolves the chord to a single-issue pick, same
			// as letting the leader window time out — that second key's own
			// meaning (e.g. a cursor move) is not separately reprocessed.
			return t.pickHighlighted(), nil
		}
	}
	if t.m.QueueEnterNotice != "" {
		t.m = Update(t.m, QueueEnterNoticeClearedMsg{})
	}
	switch msg.String() {
	case "j", "down":
		t.m = Update(t.m, CursorMoveMsg{Delta: 1})
	case "k", "up":
		// "k" is vim's cursor-up key, freed by Terminate's move to "X"
		// (issue #1500); "i"-as-up (#838) is retired now that "k" covers it.
		t.m = Update(t.m, CursorMoveMsg{Delta: -1})
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
			t = t.pickHighlighted()
			return t, nil
		}
		if p, ok := t.highlightedPick(); ok {
			if hasTranscript(p.State) {
				return t, openSidebarCmd(t.launch, t.pwd, p.Number)
			}
			t.m = Update(t.m, QueueEnterNoticedMsg{})
		}
	case "l", "right":
		t.m = Update(t.m, FocusSidebarMsg{})
	case "h", "left":
		// Already on the list — nothing to move away from. Present as an
		// explicit case (rather than falling through to the default no-op)
		// so the h/l pair reads as one symmetric gesture at the call site.
	case "r":
		return t, refreshCmd(t.tracker)
	case "q", "ctrl+c":
		t.m = Update(t.m, t.quitOrConfirmMsg())
	case "?":
		t.m = Update(t.m, HelpToggleMsg{})
	case "p":
		t.m = Update(t.m, PickPendingMsg{})
		return t, pickChordTick()
	case "u":
		t = t.unpickHighlighted()
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

// handleRebuildOutputKey routes one keypress while the rebuild-output pane
// is open: "x"/Esc closes back to the backlog, the scroll keys page through
// the captured output, and "q"/"ctrl+c" hard-quit — the universal quit
// keystroke must never be swallowed by the pane, matching handleDrillInKey
// (issue #1128).
func (t teaModel) handleRebuildOutputKey(msg tea.KeyMsg) Model {
	switch msg.String() {
	case "q", "ctrl+c":
		return Update(t.m, QuitMsg{})
	case "x", "esc":
		return Update(t.m, RebuildOutputCloseMsg{})
	case "j", "down":
		return Update(t.m, RebuildOutputScrollMsg{Delta: 1})
	case "k", "up":
		return Update(t.m, RebuildOutputScrollMsg{Delta: -1})
	case "pgdown":
		return Update(t.m, RebuildOutputScrollMsg{Delta: fixedPaneScrollDelta})
	case "pgup":
		return Update(t.m, RebuildOutputScrollMsg{Delta: -fixedPaneScrollDelta})
	}
	return t.m
}

// handleSidebarKey routes one keypress while the sidebar has focus (or the
// terminal is too narrow to dock it, forcing fullscreen, or the operator
// zoomed it — handleKey's sidebarFits/SidebarZoom guard): "t" advances the
// Activity/Transcript/raw cycle, "h"/left returns focus to the list when the
// sidebar is docked (a no-op in the fullscreen fallback, which has no list
// on screen to focus), "x"/Esc closes back to the body, the scroll keys page
// through the loaded content (issue #786's drill-in precedent), "G"/"end"
// re-attaches Follow and jumps to the bottom, "z" toggles the fullscreen
// zoom (issue #1502), and "q"/"ctrl+c" hard-quit — the universal quit
// keystroke must never be swallowed by the sidebar, same principle as the
// PendingPick chord in handleKey (issue #826) but not the same mechanics:
// PendingPick resolves the chord (pickHighlighted) before quitting, while the
// sidebar quits directly with no resolve step.
func (t teaModel) handleSidebarKey(msg tea.KeyMsg) Model {
	switch msg.String() {
	case "q", "ctrl+c":
		return Update(t.m, QuitMsg{})
	case "t":
		return Update(t.m, SidebarToggleMsg{})
	case "h", "left":
		if sidebarFits(t.m) && !t.m.SidebarZoom {
			return Update(t.m, FocusListMsg{})
		}
		return t.m
	case "x", "esc":
		return Update(t.m, SidebarCloseMsg{})
	case "j", "down":
		return Update(t.m, SidebarScrollMsg{Delta: 1})
	case "k", "up":
		return Update(t.m, SidebarScrollMsg{Delta: -1})
	case "pgdown":
		return Update(t.m, SidebarScrollMsg{Delta: fixedPaneScrollDelta})
	case "pgup":
		return Update(t.m, SidebarScrollMsg{Delta: -fixedPaneScrollDelta})
	case "G", "end":
		return Update(t.m, SidebarJumpToEndMsg{})
	case "z":
		return Update(t.m, SidebarZoomToggleMsg{})
	}
	return t.m
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
// only the Activity feed advances on its own afterward, via syncQueue's
// per-Msg SidebarActivityMsg refresh (issue #1502). Reopening (close then
// Enter again) still re-runs this whole load, picking up any Transcript
// growth the live Activity feed alone wouldn't (issue #719, inherited).
func openSidebarCmd(launch *Launcher, pwd, number string) tea.Cmd {
	return func() tea.Msg {
		drv := driverOf(launch)
		if drv == nil {
			return SidebarLoadedMsg{Number: number, Err: fmt.Errorf("no Driver available for this session")}
		}
		activity := ActivityFeed(drv, pwd, number)
		if len(dispatch.LogPaths(pwd, number)) == 0 {
			return SidebarLoadedMsg{Number: number, Activity: activity}
		}
		// DrillIn always returns a DrillInMsg; the type assertion can't fail.
		dm, _ := DrillIn(drv, pwd, number).(DrillInMsg)
		return SidebarLoadedMsg{Number: number, Activity: activity, Rendered: dm.Rendered, Raw: dm.Raw, TranscriptErr: dm.Err}
	}
}

// handleTerminateConfirmKey routes one keypress while PendingTerminate is
// armed: "y" confirms — firing Launcher.TerminateAsync before applying
// TerminateConfirmedMsg, so the blocking tracker I/O runs off the Update
// path (issue #745) — "q"/"ctrl+c" must never be swallowed by the confirm
// prompt, mirroring the PendingPick chord precedent above (issue #748), but
// must not quit outright either: a terminate-confirm prompt is only ever
// armed via isLive, so a live Dispatch was present when it appeared, and the
// operator still deserves the drain/terminate-all/stay choice ADR 0023
// promises elsewhere (unconditionally arming that confirm is harmless even
// on the rare race where the Dispatch settles between the prompt and this
// keypress — "t" just iterates an empty LiveIssues) — so "q"/"ctrl+c"
// declines the terminate (TerminateCancelledMsg, clearing PendingTerminate
// so the next keypress reaches handleQuitConfirmKey instead of looping back
// here) and arms the quit confirm (QuitRequestedMsg) rather than quitting
// directly (issue #1215) — everything else declines the terminate only
// (ADR 0024, issue #649/#785).
func (t teaModel) handleTerminateConfirmKey(msg tea.KeyMsg) teaModel {
	num := t.m.PendingTerminate
	switch s := msg.String(); s {
	case "y", "Y":
		if t.launch != nil {
			// Terminate already logs a reap failure to stderr itself
			// (launcher.go); writing it again here would both duplicate the
			// line and risk smearing the alt-screen render mid-frame.
			t.launch.TerminateAsync(t.tracker, num)
		}
		t.m = Update(t.m, TerminateConfirmedMsg{Number: num})
	case "q", "ctrl+c":
		t.m = Update(t.m, TerminateCancelledMsg{})
		t.m = Update(t.m, QuitRequestedMsg{})
	default:
		t.m = Update(t.m, TerminateCancelledMsg{})
	}
	return t
}

// handleQuitConfirmKey routes one keypress while PendingQuit is armed: "d"/
// enter drains — quits without touching any live Dispatch, which settles on
// its own — "t" terminates every live Dispatch first, then quits; "s" (or
// anything else) stays, declining the quit (issue #651, ADR 0023, issue
// #822). Unlike handleTerminateConfirmKey, quit is already the pending
// action here, so there is no separate "q"/"ctrl+c" quit-escape case.
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
// When a launch is live, this reads t.launch.Queue.Snapshot() directly —
// mirroring isLive's own live read a few lines up — rather than Model.Picks,
// which is only refreshed once per Update via syncQueue and so can still show
// a row's pre-drain state for the rest of that same keypress's handling
// (issue #837). A nil launch has no live Queue to read, so it falls back to
// Model.Picks, matching the pre-#785 no-launch Console path.
func (t teaModel) alreadyActive(num string) bool {
	picks := t.m.Picks
	if t.launch != nil {
		picks = t.launch.Queue.Snapshot()
	}
	for _, p := range picks {
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
// than re-landed (see alreadyActive).
func (t teaModel) pickAllReady() teaModel {
	for _, msg := range PickAllReady(t.tracker) {
		if queued, ok := msg.(PickQueuedMsg); ok && t.alreadyActive(queued.Number) {
			continue
		}
		t.m = Update(t.m, msg)
		t.landPick(msg)
	}
	if t.launch != nil {
		t.launch.tryLaunch(t.tracker, t.pwd)
	}
	return t
}

// unpickHighlighted retracts the cursor's highlighted issue's queued pick, if
// any — a pure session-queue edit with no tracker interaction (ADR 0023):
// Update's own removePick already refuses to drop anything past PickQueued/
// PickHeld, so this is safe to send even when the highlighted issue never
// queued or already launched.
func (t teaModel) unpickHighlighted() teaModel {
	num := t.highlightedNumber()
	if num == "" {
		return t
	}
	t.m = Update(t.m, UnpickMsg{Number: num})
	if t.launch != nil {
		t.launch.Queue.Remove(num)
	}
	return t
}

// quitOrConfirmMsg picks QuitRequestedMsg over QuitMsg whenever live
// Dispatches exist, so any "q"/"ctrl+c" quit path — chorded through
// PendingPick or not — arms the drain/terminate-all/stay confirm instead of
// exiting outright (issue #1216, ADR 0023).
func (t teaModel) quitOrConfirmMsg() Msg {
	if t.launch != nil && len(t.launch.LiveIssues()) > 0 {
		return QuitRequestedMsg{}
	}
	return QuitMsg{}
}

// pickHighlighted promotes the cursor's highlighted issue through PickIssue
// and lands the result on both the pure Model and the Launcher's live Queue
// — the keypress translation of ADR 0023's Pick-is-the-launch-button rule. A
// nil Launcher still promotes and lands the pick on Model.Picks (matching
// the pre-#785 no-launch Console) but never queues on a live Queue or
// launches, since there is nothing to launch it. A no-op outside
// SectionBacklog — Cursor indexes a work Section's Picks there, not the
// backlog, and Backlog is ADR 0030's sole pick source (issue #1500).
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
	msg := PickIssue(t.tracker, iss.Number, iss.Title, KindWork)
	t.m = Update(t.m, msg)
	t.landPick(msg)
	if _, ok := msg.(PickQueuedMsg); ok && t.launch != nil {
		t.launch.tryLaunch(t.tracker, t.pwd)
	}
	return t
}

// landPick mirrors a Pick adapter's result onto the Launcher's live Queue —
// not just the pure Model — so the row survives the very next per-render
// Queue resync (syncQueue's QueueSnapshotMsg replaces Model.Picks wholesale
// from launch.Queue.Snapshot()). A failed promotion lands its dissolved row
// on the Queue exactly as a queued one does; otherwise the operator's only
// feedback that a pick raced, closed, or got relabeled would vanish on the
// very next frame. A nil Launcher (matching the pre-#785 no-launch Console)
// leaves Model.Picks as the pick's only record.
func (t teaModel) landPick(msg Msg) {
	if t.launch == nil {
		return
	}
	switch msg := msg.(type) {
	case PickQueuedMsg:
		t.launch.Queue.Add(Pick{Number: msg.Number, Title: msg.Title, Kind: msg.Kind, State: PickQueued})
	case PickDissolvedMsg:
		t.launch.Queue.Add(Pick{Number: msg.Number, Title: msg.Title, State: PickDissolved, Reason: msg.Reason})
	default:
		return
	}
	// A pick's promotion attempt is always a tracker write, win or lose —
	// the same rationale drain's own discover() closure documents — so it
	// triggers the same out-of-band refresh every other session write does
	// (#647 AC4), not just the eventual one a successful pick's own drain
	// happens to also fire.
	t.launch.signalRefresh()
}

// handleFilterKey routes one keypress while FilterEditing: Enter applies
// (exits editing, keeping the already-live-narrowed Filter), Esc cancels
// (reverts to the pre-edit Filter), Backspace trims one rune, and any other
// printable key appends to the filter text — narrowing the list live.
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

// syncQueue installs launch's live Queue state onto m, so every render —
// not just the one right after a pick — reflects claim/run/settle/dissolve
// transitions that happen entirely on the background Queue. Every running
// row's Heartbeat is also refreshed from its on-disk log on the way in
// (#647 AC2) — a plain local read, unlike the backlog refresh, so it is not
// gated behind the write/poll-triggered cadence the tracker's rate limit
// forces on Refresh. heartbeats caches that refresh per pick number, so a
// call whose latest pass log is unchanged since last time skips the
// ReadFile+reparse entirely (issue #731) — syncQueue runs on every tea.Msg,
// not just a render tick, so most calls see the same on-disk bytes as last
// time. The open sidebar's own Activity feed is refreshed the same way,
// scoped to whichever Dispatch it has open and only while that Dispatch is
// still running — ADR 0030's live-tail, piggybacking this same per-Msg sync
// rather than a dedicated timer, and bounded to one Dispatch's I/O no matter
// how many others are running (issue #1502). It also installs the session's
// current live parallelism cap and live count (issue #653), read straight
// off the Launcher's Limiter — no Msg carries a resize, so this per-render
// pull is the only path that keeps them current. A nil launch leaves m
// untouched.
func syncQueue(m Model, launch *Launcher, pwd string, heartbeats *HeartbeatCache, sidebarActivity *SidebarActivityCache) Model {
	if launch == nil {
		return m
	}
	picks := launch.Queue.Snapshot()
	drv := driverOf(launch)
	for i := range picks {
		if drv != nil && picks[i].State == PickRunning {
			picks[i].Heartbeat = heartbeats.RunningHeartbeat(drv, pwd, picks[i].Number)
		}
		if !picks[i].QueuedAt.IsZero() {
			picks[i].Age = formatAge(time.Since(picks[i].QueuedAt))
		}
	}
	m = Update(m, QueueSnapshotMsg{Picks: picks})
	if m.Sidebar != nil && drv != nil && isRunningNumber(picks, m.Sidebar.Number) {
		if activity, ok := sidebarActivity.Refresh(drv, pwd, m.Sidebar.Number); ok {
			m = Update(m, SidebarActivityMsg{Number: m.Sidebar.Number, Activity: activity})
		}
	}
	return Update(m, CapMsg{Cap: launch.Cap(), Live: launch.Live()})
}

// isRunningNumber reports whether picks carries number in PickRunning state
// — syncQueue's gate on refreshing the open sidebar's Activity feed only
// while its Dispatch is actually live, since a Settled/Terminated/Failed
// Dispatch's logs never change again (issue #1502).
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
// rebuild's progress/outcome, exactly as syncQueue does for the picks queue
// (issue #652). A nil launch leaves m untouched.
func syncStale(m Model, launch *Launcher) Model {
	if launch == nil {
		return m
	}
	return Update(m, StaleStatusMsg{RebuildStatus: launch.StaleStatus()})
}

// driverOf returns the Driver a Launcher's Factory was constructed with, or
// nil when no Driver is available (a launch-less session, or a Launcher
// built without a Factory) — syncQueue's heartbeat lookup skips the read
// rather than panicking on a nil Driver.
func driverOf(launch *Launcher) driver.Driver {
	if launch == nil || launch.Factory == nil {
		return nil
	}
	return launch.Factory.Driver()
}

// orphanRecoveryCmd adopts every issue OrphanedIssues reports through
// launch's RecoverFn in the background at startup — a crash or dropped SSH
// from a prior session left these sandboxes running with no live goroutine
// in this fresh process to account for them (issue #651, issue #822). Errors
// from either call surface through the returned OrphanRecoveryMsg — Update
// threads it onto Model.OrphanRecoveryErr the same way StaleStatusMsg threads
// RebuildErr — instead of being swallowed (issue #1218); a failed adopt still
// leaves that issue for the operator to notice and Pick again like any other
// orphaned Dispatch. nil (no Cmd) when launch or RecoverFn is nil, matching
// waitRefreshSignal's nil-launch convention.
func orphanRecoveryCmd(launch *Launcher) tea.Cmd {
	if launch == nil || launch.RecoverFn == nil {
		return nil
	}
	return func() tea.Msg {
		nums, err := launch.OrphanedIssues()
		if err != nil {
			return OrphanRecoveryMsg{Err: fmt.Sprintf("failed to list orphaned issues: %s", err)}
		}
		var failures []string
		for _, num := range nums {
			if err := launch.RecoverFn(num); err != nil {
				failures = append(failures, fmt.Sprintf("failed to adopt orphan #%s: %s", num, err))
			}
		}
		if len(failures) == 0 {
			return nil
		}
		return OrphanRecoveryMsg{Err: strings.Join(failures, "; ")}
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
			return refreshSignalMsg{}
		case <-done:
			return nil
		}
	}
}
