// Run drives the console as a real full-screen Bubble Tea program (issue
// #784) — the sole entry point; the earlier bufio.Scanner line-command loop
// is retired. teaModel is a thin adapter: tea.Model.Update translates
// tea.KeyMsg and the two async signals (background poll, launch-refresh)
// into the same console Msg values Update already handles; tea.Model.View
// delegates straight to the pure View. Pick/Unpick/pick-all-ready/Terminate/
// Resize/Rebuild act on the cursor's highlighted row (issue #785); DrillIn is
// wired too (issue #786).
package console

import (
	"fmt"
	"io"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/forge"
)

// defaultPollInterval is the background backlog poll's fixed cadence when a
// Launcher doesn't override it (production always uses this) — slow enough
// to never spend the rate-limit window the session's Agents share (#647 AC5).
const defaultPollInterval = 90 * time.Second

// drillInPageSize is how many lines pgup/pgdown move the drill-in scroll
// offset — j/k and the arrows move one line at a time (issue #786).
const drillInPageSize = 10

// teaModel is the Bubble Tea adapter around the pure Model: it carries the
// I/O seams (tracker, pwd, launch) Update itself never touches, and
// translates tea.Msg values into console Msg values before calling Update.
type teaModel struct {
	m            Model
	tracker      forge.IssueTracker
	pwd          string
	launch       *Launcher
	pollInterval time.Duration
}

// newTeaModel builds the tea layer's starting state: the dogfood-competition
// notice is checked synchronously (a cheap os.Stat, matching the pre-#784
// Run's own startup check) — the initial backlog load, background poll, and
// launch-refresh listener all start as Cmds from Init instead.
func newTeaModel(tracker forge.IssueTracker, pwd string, launch *Launcher) teaModel {
	m := NewModel()
	m = Update(m, DogfoodNotice(pwd))
	interval := defaultPollInterval
	if launch != nil && launch.pollInterval > 0 {
		interval = launch.pollInterval
	}
	return teaModel{m: m, tracker: tracker, pwd: pwd, launch: launch, pollInterval: interval}
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

// Init starts the initial backlog load and both async signal sources
// (background poll, launch-refresh) as Cmds — none of them block the
// program's own startup.
func (t teaModel) Init() tea.Cmd {
	cmds := []tea.Cmd{refreshCmd(t.tracker), pollTick(t.pollInterval)}
	if t.launch != nil {
		cmds = append(cmds, waitRefreshSignal(t.launch))
	}
	return tea.Batch(cmds...)
}

// Update is the tea layer's whole adapter surface: it translates tea.KeyMsg
// and the two async signals into console Msg values already handled by the
// pure Update, then re-syncs the launcher's live Queue/stale state onto the
// Model exactly as the pre-#784 Run loop did on every render.
func (t teaModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.KeyMsg:
		t, cmd = t.handleKey(msg)
	case IssuesLoadedMsg:
		t.m = Update(t.m, msg)
	case DrillInMsg:
		t.m = Update(t.m, msg)
	case pollTickMsg:
		if t.launch != nil {
			t.launch.tryLaunch(t.tracker, t.pwd)
		}
		cmd = tea.Batch(refreshCmd(t.tracker), pollTick(t.pollInterval))
	case refreshSignalMsg:
		cmd = tea.Batch(refreshCmd(t.tracker), waitRefreshSignal(t.launch))
	}

	t.m = syncQueue(t.m, t.launch, t.pwd)
	t.m = syncStale(t.m, t.launch)
	if t.m.Quitting {
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
// gated by whichever modal state (drill-in pane, help overlay, filter edit)
// is active — mirroring applyCommand's old PendingTerminate/PendingQuit
// precedence, now keyed off Model's own modal fields instead of a line
// parse. An open drill-in takes precedence over everything else: it replaces
// the whole screen, so help/filter keys don't apply while it's up (#786).
func (t teaModel) handleKey(msg tea.KeyMsg) (teaModel, tea.Cmd) {
	if t.m.DrillIn != nil {
		t.m = t.handleDrillInKey(msg)
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
	switch msg.String() {
	case "j", "down":
		t.m = Update(t.m, CursorMoveMsg{Delta: 1})
	case "up":
		t.m = Update(t.m, CursorMoveMsg{Delta: -1})
	case "/":
		t.m = Update(t.m, FilterEditStartMsg{})
	case "d", "enter":
		if iss, ok := t.highlightedIssue(); ok {
			return t, openDrillInCmd(t.launch, t.pwd, iss.Number)
		}
	case "r":
		return t, refreshCmd(t.tracker)
	case "q", "ctrl+c":
		t.m = Update(t.m, QuitMsg{})
	case "?":
		t.m = Update(t.m, HelpToggleMsg{})
	case "p":
		t = t.pickHighlighted()
	case "u":
		t = t.unpickHighlighted()
	case "P":
		t = t.pickAllReady()
	case "k":
		if num := t.highlightedNumber(); num != "" {
			t.m = Update(t.m, TerminateRequestedMsg{Number: num})
		}
	case "+":
		if t.launch != nil {
			t.launch.Resize(1)
		}
	case "-":
		if t.launch != nil {
			t.launch.Resize(-1)
		}
	case "b":
		if t.launch != nil {
			t.launch.Rebuild(t.tracker, t.pwd)
		}
	}
	return t, nil
}

// handleDrillInKey routes one keypress while the drill-in transcript pane is
// open: "t" toggles rendered/raw, "x"/Esc closes back to the backlog, and
// the scroll keys page through the loaded content (issue #786).
func (t teaModel) handleDrillInKey(msg tea.KeyMsg) Model {
	switch msg.String() {
	case "t":
		return Update(t.m, DrillInToggleMsg{})
	case "x", "esc":
		return Update(t.m, DrillInCloseMsg{})
	case "j", "down":
		return Update(t.m, DrillInScrollMsg{Delta: 1})
	case "k", "up":
		return Update(t.m, DrillInScrollMsg{Delta: -1})
	case "pgdown":
		return Update(t.m, DrillInScrollMsg{Delta: drillInPageSize})
	case "pgup":
		return Update(t.m, DrillInScrollMsg{Delta: -drillInPageSize})
	}
	return t.m
}

// highlightedIssue returns the backlog row under the cursor, or false when
// Visible() is empty — the "d"/Enter drill-in target.
func (t teaModel) highlightedIssue() (forge.Issue, bool) {
	vis := t.m.Visible()
	if len(vis) == 0 {
		return forge.Issue{}, false
	}
	return vis[t.m.Cursor], true
}

// openDrillInCmd loads and renders number's whole transcript in the
// background. A launch-less session (or a Launcher built without a Factory)
// has no Driver to render with — that renders as a graceful DrillInMsg error
// instead of dereferencing a nil Driver (issue #786 AC4).
func openDrillInCmd(launch *Launcher, pwd, number string) tea.Cmd {
	return func() tea.Msg {
		drv := driverOf(launch)
		if drv == nil {
			return DrillInMsg{Number: number, Err: fmt.Errorf("no Driver available for this session")}
		}
		return DrillIn(drv, pwd, number)
	}
}

// handleTerminateConfirmKey routes one keypress while PendingTerminate is
// armed: "y" confirms — calling Launcher.Terminate before applying
// TerminateConfirmedMsg, matching TerminateConfirmedMsg's own doc ("the run
// loop has already called Launcher.Terminate by the time this reaches
// Update") — anything else declines (ADR 0024, issue #649/#785).
func (t teaModel) handleTerminateConfirmKey(msg tea.KeyMsg) teaModel {
	num := t.m.PendingTerminate
	if msg.String() == "y" {
		if t.launch != nil {
			if err := t.launch.Terminate(t.tracker, num); err != nil {
				fmt.Fprintf(os.Stderr, "    ?? #%s: terminate: %v\n", num, err)
			}
		}
		t.m = Update(t.m, TerminateConfirmedMsg{Number: num})
		return t
	}
	t.m = Update(t.m, TerminateCancelledMsg{})
	return t
}

// highlightedNumber returns the cursor's highlighted issue number, or "" when
// the backlog is empty.
func (t teaModel) highlightedNumber() string {
	visible := t.m.Visible()
	if len(visible) == 0 {
		return ""
	}
	return visible[t.m.Cursor].Number
}

// pickAllReady picks every issue currently Dispatchable on the tracker in
// one bulk gesture (#647 AC3) — bound to shift+p ("P") so it can never
// collide with, or need to wait behind, the single-issue "p" launch button.
func (t teaModel) pickAllReady() teaModel {
	for _, msg := range PickAllReady(t.tracker) {
		t.m = Update(t.m, msg)
		if queued, ok := msg.(PickQueuedMsg); ok && t.launch != nil {
			t.launch.Queue.Add(Pick{Number: queued.Number, Title: queued.Title, Kind: queued.Kind, State: PickQueued})
		}
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

// pickHighlighted promotes the cursor's highlighted issue through PickIssue
// and lands the result on both the pure Model and the Launcher's live Queue
// — the keypress translation of ADR 0023's Pick-is-the-launch-button rule. A
// nil Launcher still promotes and lands the pick on Model.Picks (matching
// the pre-#785 no-launch Console) but never queues on a live Queue or
// launches, since there is nothing to launch it.
func (t teaModel) pickHighlighted() teaModel {
	visible := t.m.Visible()
	if len(visible) == 0 {
		return t
	}
	iss := visible[t.m.Cursor]
	msg := PickIssue(t.tracker, iss.Number, iss.Title, KindWork)
	t.m = Update(t.m, msg)
	if queued, ok := msg.(PickQueuedMsg); ok && t.launch != nil {
		t.launch.Queue.Add(Pick{Number: queued.Number, Title: queued.Title, Kind: queued.Kind, State: PickQueued})
		t.launch.tryLaunch(t.tracker, t.pwd)
	}
	return t
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
// forces on Refresh. It also installs the session's current live
// parallelism cap and live count (issue #653), read straight off the
// Launcher's Limiter — no Msg carries a resize, so this per-render pull is
// the only path that keeps them current. A nil launch leaves m untouched.
func syncQueue(m Model, launch *Launcher, pwd string) Model {
	if launch == nil {
		return m
	}
	picks := launch.Queue.Snapshot()
	if drv := driverOf(launch); drv != nil {
		for i := range picks {
			if picks[i].State == PickRunning {
				picks[i].Heartbeat = RunningHeartbeat(drv, pwd, picks[i].Number)
			}
		}
	}
	m = Update(m, QueueSnapshotMsg{Picks: picks})
	return Update(m, CapMsg{Cap: launch.Cap(), Live: launch.Live()})
}

// syncStale installs launch's live image-freshness/rebuild state onto m, so
// every render reflects a stale verdict a background drain saw, or a
// rebuild's progress/outcome, exactly as syncQueue does for the picks queue
// (issue #652). A nil launch leaves m untouched.
func syncStale(m Model, launch *Launcher) Model {
	if launch == nil {
		return m
	}
	stale, msg, rebuilding, rebuildErr := launch.StaleStatus()
	return Update(m, StaleStatusMsg{Stale: stale, Message: msg, Rebuilding: rebuilding, RebuildErr: rebuildErr})
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

// waitRefreshSignal blocks on launch's refresh channel in the background,
// translating one arrival into a refreshSignalMsg — nil (no Cmd) when launch
// is nil, since there is then no Queue whose writes could ever signal one.
func waitRefreshSignal(launch *Launcher) tea.Cmd {
	if launch == nil {
		return nil
	}
	ch := launch.Refreshes()
	return func() tea.Msg {
		<-ch
		return refreshSignalMsg{}
	}
}
