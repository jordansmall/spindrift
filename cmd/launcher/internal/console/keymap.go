package console

import (
	"fmt"
	"slices"

	tea "github.com/charmbracelet/bubbletea"
)

// Action is a keymap entry's dispatch behaviour: given the tea layer's
// model, the triggering keypress, and the mode dispatchKey (tea.go) looked
// it up under, it applies whatever Msg(s) that key means and returns the
// resulting teaModel plus any tea.Cmd to run — the same (teaModel, tea.Cmd)
// shape handleKey itself returns. mode is passed explicitly rather than
// re-derived via t.m.ActiveMode() so an entry spanning several modes with
// different behaviour (e.g. the shared quit entry below) can switch on mode
// inside its own Action rather than splitting into several near-duplicate
// entries (issue #1790).
type Action func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd)

// Binding is one entry in the console's declarative keymap: the single
// source of truth for both a key's hint text (the "?" help overlay and
// per-view footers) and its dispatch behaviour. dispatchKey (tea.go) is the
// only caller of Action; TestKeymapParity (keymap_test.go) enforces that
// every entry carries one (issue #1789, #1790).
type Binding struct {
	// Keys are the literal key names (msg.String() form) this binding's
	// Action fires on, e.g. []string{"j", "down"}.
	Keys []string
	// Modes are the Mode(s) dispatchKey looks Keys up under. The quit entry
	// below lists every Mode where "q"/"ctrl+c" hard-quits ("global" in the
	// loose sense the console uses it, not literally every Mode: ModeHelp,
	// ModeFilterEdit, and ModeQuitConfirm each handle "q" differently or not
	// at all, so they're deliberately left out of that entry's Modes).
	Modes []Mode
	// Help is this binding's line(s) in the "?" overlay, verbatim (may embed
	// "\n" for a wrapped continuation). Empty when the binding has no
	// standalone overlay entry of its own — usually because another entry's
	// Help already documents it (e.g. the rebuild-output pane's scroll keys
	// are folded into "o"'s own paragraph).
	Help string
	// Footer is this binding's short "[key] verb" fragment for a per-view
	// footer hint. Empty when the binding never appears in a footer.
	Footer string
	// FooterCompact overrides Footer for the one footer tight enough to need
	// shorter wording (the docked sidebar's 42-column budget, view.go). Only
	// set where it actually differs from Footer.
	FooterCompact string
	// Action is this binding's dispatch behaviour — see Action's own doc
	// comment. Every entry in keymap below carries one; TestKeymapParity
	// fails on any that doesn't (issue #1790).
	Action Action
}

// keymap is every binding dispatchKey dispatches through, grouped in the
// same order the retired static renderHelp slice documented them in, so
// rebuilding that slice from Help fields below reproduces its text exactly.
// Entries with an empty Help contribute no overlay line of their own — they
// exist only so TestKeymapParity's bijection holds and/or to carry Footer
// text for a per-view footer (issue #1789).
var keymap = []Binding{
	{
		Keys: []string{"j", "down", "k", "up"}, Modes: []Mode{ModeList},
		Help: "  j/k, down/up  move cursor within the active Section",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			delta := 1
			if s := msg.String(); s == "k" || s == "up" {
				delta = -1
			}
			t.m = Update(t.m, CursorMoveMsg{Delta: delta})
			return t, nil
		},
	},
	{
		Keys: []string{"G"}, Modes: []Mode{ModeList},
		Help: "  G           jump to the active Section's last row, scrolling it\n" +
			"              into view",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			t.m = Update(t.m, CursorJumpToLastMsg{})
			return t, nil
		},
	},
	{
		Keys: []string{"g"}, Modes: []Mode{ModeList},
		Help: "  gg          jump to the active Section's first row (\"g\" arms a\n" +
			"              pending leader, awaiting a trailing \"g\")",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			var cmd tea.Cmd
			t.m, cmd = armPendingG(t.m)
			return t, cmd
		},
	},
	{
		Keys: []string{"H", "L"}, Modes: []Mode{ModeList},
		Help:   "  H / L       switch to the previous / next Section",
		Footer: "H/L",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			if msg.String() == "H" {
				t.m = Update(t.m, SectionPrevMsg{})
			} else {
				t.m = Update(t.m, SectionNextMsg{})
			}
			return t, nil
		},
	},
	{
		Keys: []string{"1", "2", "3", "4", "5"}, Modes: []Mode{ModeList},
		Help: "  1-5         jump straight to a Section (Backlog, Running, Held,\n" +
			"              Settled, Failed)",
		Footer: "1-5",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			sections := map[string]Section{
				"1": SectionBacklog, "2": SectionRunning, "3": SectionHeld,
				"4": SectionSettled, "5": SectionFailed,
			}
			t.m = Update(t.m, SectionJumpMsg{Section: sections[msg.String()]})
			return t, nil
		},
	},
	{
		Keys: []string{"pgdown", "ctrl+f", "pgup", "ctrl+b"}, Modes: []Mode{ModeList},
		Help: "  ctrl+f/ctrl+b, pgup/pgdown  jump a full page of the active Section's live\n" +
			"              rendered rows without moving the cursor; the page\n" +
			"              size tracks terminal resizes",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			delta := sectionPageSize(t.m)
			if s := msg.String(); s == "pgup" || s == "ctrl+b" {
				delta = -delta
			}
			t.m = Update(t.m, ScrollMsg{Delta: delta})
			return t, nil
		},
	},
	{
		Keys: []string{"ctrl+d", "ctrl+u"}, Modes: []Mode{ModeList},
		Help: "  ctrl+d/ctrl+u  jump a half page of the active Section's live\n" +
			"              rendered rows without moving the cursor; half of the\n" +
			"              ctrl+f/ctrl+b page above",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			delta := sectionPageSize(t.m) / 2
			if msg.String() == "ctrl+u" {
				delta = -delta
			}
			t.m = Update(t.m, ScrollMsg{Delta: delta})
			return t, nil
		},
	},
	{
		Keys: []string{"/"}, Modes: []Mode{ModeList},
		Help:   "  /           filter the Backlog by label substring",
		Footer: "[/] filter",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			t.m = Update(t.m, FilterEditStartMsg{})
			return t, nil
		},
	},
	{
		Keys: []string{"enter"}, Modes: []Mode{ModeList, ModeFilterEdit},
		Help: "  enter       apply filter (while filter-editing); otherwise: open\n" +
			"              the highlighted row's ticket detail (Backlog Section),\n" +
			"              or open the highlighted pick's live-tail sidebar (a\n" +
			"              work Section, only when it has run)",
		Footer: "[enter] apply",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			if mode == ModeFilterEdit {
				t.m = Update(t.m, FilterEditConfirmMsg{})
				return t, nil
			}
			if t.m.ActiveSection == SectionBacklog {
				iss, ok := t.highlightedIssue()
				if !ok {
					return t, nil
				}
				if t.m.IsOrphan(iss.Number) {
					return t, openSidebarCmd(t.launch, t.pwd, iss.Number, true)
				}
				return t.openDetailModal(iss)
			}
			if p, ok := t.highlightedPick(); ok {
				if hasTranscript(p.State) {
					return t, openSidebarCmd(t.launch, t.pwd, p.Number, false)
				}
				t.m = Update(t.m, QueueEnterNoticedMsg{})
			}
			return t, nil
		},
	},
	{
		Keys: []string{"l", "right", "h", "left"}, Modes: []Mode{ModeList},
		Help: "  h/l, left/right  move focus between the list and the sidebar\n" +
			"              (while a sidebar is open)",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			if s := msg.String(); s == "l" || s == "right" {
				t.m = Update(t.m, FocusSidebarMsg{})
			}
			// "h"/"left": already on the list — nothing to move away from.
			// Present as an explicit no-op case (rather than silently falling
			// out of a switch) so the h/l pair reads as one symmetric gesture
			// at the call site.
			return t, nil
		},
	},
	{
		Keys: []string{"esc"}, Modes: []Mode{ModeFilterEdit},
		Help:   "  esc         cancel filter edit",
		Footer: "[esc] cancel",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			t.m = Update(t.m, FilterEditCancelMsg{})
			return t, nil
		},
	},
	{
		Keys: []string{"t"}, Modes: []Mode{ModeSidebar},
		Help: "  t           cycle the sidebar's activity feed -> transcript ->\n" +
			"              raw JSONL -> activity feed (while the sidebar has focus)",
		Footer:        "[t] cycle activity/transcript",
		FooterCompact: "[t] cycle",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			t.m = Update(t.m, SidebarToggleMsg{})
			return t, nil
		},
	},
	{
		// The docked layout's own "return focus to the list" case (ModeList
		// never sees this key with this meaning — it's handleListKey's own
		// "h"/"left" no-op instead, covered by the entry above).
		Keys: []string{"h", "left"}, Modes: []Mode{ModeSidebar},
		Footer: "[h] list",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			if sidebarFits(t.m) && !t.m.SidebarZoom {
				t.m = Update(t.m, FocusListMsg{})
			}
			return t, nil
		},
	},
	{
		Keys: []string{"x", "esc"}, Modes: []Mode{ModeSidebar, ModeList},
		Help:   "  x / esc     close the sidebar (while it has focus)",
		Footer: "[x] close",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			// ModeSidebar closes unconditionally; ModeList only when a docked
			// sidebar is actually open (a fullscreen/zoomed one routes to
			// ModeSidebar instead, per ActiveMode, so ModeList never sees this
			// key with Sidebar nil in that case either — the guard exists for
			// the plain "no sidebar at all" case).
			if mode == ModeSidebar || t.m.Sidebar != nil {
				t.m = Update(t.m, SidebarCloseMsg{})
			}
			return t, nil
		},
	},
	{
		Keys: []string{"j", "down", "k", "up", "pgdown", "ctrl+f", "pgup", "ctrl+b"}, Modes: []Mode{ModeSidebar},
		Help: "  j/k, ctrl+f/ctrl+b, pgup/pgdown  scroll the sidebar (while it has focus); its\n" +
			fmt.Sprintf("              pgup/pgdown page jump is fixed at %d lines, unlike the", fixedPaneScrollDelta) + "\n" +
			"              body's live-viewport-derived one above; scrolling up\n" +
			"              detaches the running Activity feed's live follow",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			var delta int
			switch msg.String() {
			case "j", "down":
				delta = 1
			case "k", "up":
				delta = -1
			case "pgdown", "ctrl+f":
				delta = fixedPaneScrollDelta
			case "pgup", "ctrl+b":
				delta = -fixedPaneScrollDelta
			}
			t.m = Update(t.m, SidebarScrollMsg{Delta: delta})
			return t, nil
		},
	},
	{
		Keys: []string{"ctrl+d", "ctrl+u"}, Modes: []Mode{ModeSidebar},
		Help: "  ctrl+d/ctrl+u  scroll the sidebar a half page (while it has focus,\n" +
			fmt.Sprintf("              fixed at %d lines, half of ctrl+f/ctrl+b above)", fixedPaneScrollDelta/2),
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			delta := fixedPaneScrollDelta / 2
			if msg.String() == "ctrl+u" {
				delta = -delta
			}
			t.m = Update(t.m, SidebarScrollMsg{Delta: delta})
			return t, nil
		},
	},
	{
		Keys: []string{"G", "end"}, Modes: []Mode{ModeSidebar},
		Help: "  G / end     re-attach follow and jump to the sidebar's bottom\n" +
			"              (while the sidebar has focus)",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			t.m = Update(t.m, SidebarJumpToEndMsg{})
			return t, nil
		},
	},
	{
		Keys: []string{"g"}, Modes: []Mode{ModeSidebar},
		Help: "  gg          detach follow and jump to the sidebar's top (while it\n" +
			"              has focus; same \"g\" leader as the list body's gg)",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			var cmd tea.Cmd
			t.m, cmd = armPendingG(t.m)
			return t, cmd
		},
	},
	{
		Keys: []string{"z"}, Modes: []Mode{ModeSidebar},
		Help: "  z           toggle the sidebar's fullscreen zoom (while it has\n" +
			"              focus)",
		Footer: "[z] zoom",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			t.m = Update(t.m, SidebarZoomToggleMsg{})
			return t, nil
		},
	},
	{
		Keys: []string{"esc"}, Modes: []Mode{ModeDetailModal},
		Help:   "  esc         close the ticket detail modal (while it is open)",
		Footer: "[esc] close",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			t.m = Update(t.m, DetailModalCloseMsg{})
			return t, nil
		},
	},
	{
		Keys: []string{"j", "down", "k", "up"}, Modes: []Mode{ModeDetailModal},
		Help: "  j/k, up/down  scroll the ticket detail modal's body (while it is\n" +
			"              open)",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			delta := 1
			if s := msg.String(); s == "k" || s == "up" {
				delta = -1
			}
			t.m = Update(t.m, DetailModalScrollMsg{Delta: delta})
			return t, nil
		},
	},
	{
		Keys: []string{"pgdown", "ctrl+f", "pgup", "ctrl+b"}, Modes: []Mode{ModeDetailModal},
		Help: "  ctrl+f/ctrl+b, pgdown/pgup  page the ticket detail modal's body\n" +
			"              (while it is open)",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			delta := detailModalScrollBudget(t.m)
			if s := msg.String(); s == "pgup" || s == "ctrl+b" {
				delta = -delta
			}
			t.m = Update(t.m, DetailModalScrollMsg{Delta: delta})
			return t, nil
		},
	},
	{
		Keys: []string{"ctrl+d", "ctrl+u"}, Modes: []Mode{ModeDetailModal},
		Help: "  ctrl+d/ctrl+u  scroll the ticket detail modal's body a half page\n" +
			"              (while it is open)",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			delta := detailModalScrollBudget(t.m) / 2
			if msg.String() == "ctrl+u" {
				delta = -delta
			}
			t.m = Update(t.m, DetailModalScrollMsg{Delta: delta})
			return t, nil
		},
	},
	{
		Keys: []string{"G"}, Modes: []Mode{ModeDetailModal},
		Help: "  G           jump to the ticket detail modal's last page (while it\n" +
			"              is open)",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			t.m = Update(t.m, DetailModalJumpToLastMsg{})
			return t, nil
		},
	},
	{
		Keys: []string{"g"}, Modes: []Mode{ModeDetailModal},
		Help: "  gg          jump to the ticket detail modal's first page (while\n" +
			"              it is open; same \"g\" leader as the list body's gg)",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			var cmd tea.Cmd
			t.m, cmd = armPendingG(t.m)
			return t, cmd
		},
	},
	{
		Keys: []string{"p"}, Modes: []Mode{ModeDetailModal},
		Help: "  p           pick the displayed issue as a work-kind dispatch\n" +
			"              (same launch button as the Backlog's \"p\"), then close\n" +
			"              the modal",
		Footer: "[p] pick",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			return t.pickDetailModalIssue(), nil
		},
	},
	{
		Keys: []string{"r"}, Modes: []Mode{ModeList},
		Help:   "  r           refresh the backlog",
		Footer: "[r] refresh",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			t.m = Update(t.m, DetailCacheInvalidatedMsg{})
			return t, refreshCmd(t.tracker)
		},
	},
	{
		Keys: []string{"p"}, Modes: []Mode{ModeList},
		Help:   "  p           pick the highlighted Backlog row (launch button)",
		Footer: "[p] pick",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			return t.pickHighlighted(KindWork), nil
		},
	},
	{
		Keys: []string{"P"}, Modes: []Mode{ModeList},
		Help:   "  P           pick all ready (bulk pick-all-ready gesture)",
		Footer: "[P] pick all",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			return t.pickAllReady(), nil
		},
	},
	{
		Keys: []string{"u"}, Modes: []Mode{ModeList},
		Help: "  u           unpick the highlighted queued pick",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			return t.unpickHighlighted(), nil
		},
	},
	{
		Keys: []string{"X"}, Modes: []Mode{ModeList},
		Help: "  X           terminate the highlighted live Dispatch (confirm y/N,\n" +
			"              q/ctrl+c decline and quit)",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			if num := t.terminateTarget(); num != "" && t.isLive(num) {
				t.m = Update(t.m, TerminateRequestedMsg{Number: num})
			}
			return t, nil
		},
	},
	{
		Keys: []string{"y", "Y"}, Modes: []Mode{ModeTerminateConfirm},
		Footer: "[y/N/q/ctrl+c]",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			num := t.m.TerminateConfirm.Number
			if t.launch != nil {
				// Terminate already logs a reap failure to stderr itself
				// (launcher.go); writing it again here would both duplicate
				// the line and risk smearing the alt-screen render mid-frame.
				// The actual PickTerminated transition lands later, once
				// Terminate's background goroutine reaches it, through a
				// pushed refreshSignalMsg — this snapshot is the queue as it
				// stands at initiation (issue #1542).
				picks := t.launch.TerminateAsync(t.tracker, num)
				t.m = Update(t.m, QueueSnapshotMsg{Picks: picks})
			}
			t.m = Update(t.m, TerminateConfirmedMsg{Number: num})
			return t, nil
		},
	},
	{
		Keys: []string{"A"}, Modes: []Mode{ModeList},
		Help: "  A           adopt the highlighted orphan-flagged Backlog row (a\n" +
			"              running sandbox this session didn't launch); reports\n" +
			"              why and changes nothing without a non-draft open PR",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			if t.m.ActiveSection == SectionBacklog && t.launch != nil && t.launch.RecoverFn != nil {
				if iss, ok := t.highlightedIssue(); ok && t.m.IsOrphan(iss.Number) && !t.m.IsAdoptingOrphan(iss.Number) {
					t.m = Update(t.m, AdoptOrphanStartedMsg{Number: iss.Number})
					return t, adoptOrphanCmd(t.launch, iss.Number)
				}
			}
			return t, nil
		},
	},
	{
		Keys: []string{"+"}, Modes: []Mode{ModeList},
		Help: "  +           raise the live parallelism cap",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			if t.launch != nil {
				t.launch.Resize(1)
				// Resize's own Grown signal only reaches a drain already
				// running; a session with no active drain (nothing picked
				// yet, or the last one already went idle) has no listener to
				// catch it, so a raise falls back to tryLaunch — a no-op if a
				// drain is in fact already running, or if nothing is
				// queued/held to launch into the freed slot (#754).
				t.launch.tryLaunch(t.tracker, t.pwd)
			}
			return t, nil
		},
	},
	{
		Keys: []string{"-"}, Modes: []Mode{ModeList},
		Help: "  -           lower the live parallelism cap",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			if t.launch != nil {
				t.launch.Resize(-1)
			}
			return t, nil
		},
	},
	{
		Keys: []string{"b"}, Modes: []Mode{ModeList},
		Help: "  b           rebuild the stale image in-session",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			if t.launch != nil && t.m.RebuildStatus.Stale {
				t.launch.Rebuild(t.tracker, t.pwd)
			}
			return t, nil
		},
	},
	{
		Keys: []string{"o"}, Modes: []Mode{ModeList},
		Help: "  o           open the rebuild output pane (once a rebuild has run);\n" +
			"              j/k, ctrl+f/ctrl+b, pgup/pgdown scroll it, x/esc closes\n" +
			"              G jumps to its last page, gg to its first (\"g\" arms a\n" +
			"              pending leader, awaiting a trailing \"g\");\n" +
			"              ctrl+d/ctrl+u scroll it a half page (half of ctrl+f/ctrl+b)",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			if t.m.RebuildStatus.Output != "" {
				t.m = Update(t.m, RebuildOutputOpenMsg{})
			}
			return t, nil
		},
	},
	{
		Keys: []string{"j", "down", "k", "up", "pgdown", "ctrl+f", "pgup", "ctrl+b",
			"ctrl+d", "ctrl+u", "G", "g", "x", "esc"},
		Modes:  []Mode{ModeRebuildOutput},
		Footer: "[x] close",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			switch msg.String() {
			case "x", "esc":
				t.m = Update(t.m, RebuildOutputCloseMsg{})
			case "j", "down":
				t.m = Update(t.m, RebuildOutputScrollMsg{Delta: 1})
			case "k", "up":
				t.m = Update(t.m, RebuildOutputScrollMsg{Delta: -1})
			case "pgdown", "ctrl+f":
				t.m = Update(t.m, RebuildOutputScrollMsg{Delta: fixedPaneScrollDelta})
			case "pgup", "ctrl+b":
				t.m = Update(t.m, RebuildOutputScrollMsg{Delta: -fixedPaneScrollDelta})
			case "ctrl+d":
				t.m = Update(t.m, RebuildOutputScrollMsg{Delta: fixedPaneScrollDelta / 2})
			case "ctrl+u":
				t.m = Update(t.m, RebuildOutputScrollMsg{Delta: -(fixedPaneScrollDelta / 2)})
			case "G":
				t.m = Update(t.m, RebuildOutputJumpToLastMsg{})
			case "g":
				var cmd tea.Cmd
				t.m, cmd = armPendingG(t.m)
				return t, cmd
			}
			return t, nil
		},
	},
	{
		Keys: []string{"q", "ctrl+c"},
		Modes: []Mode{
			ModeList, ModeRebuildOutput, ModeDetailModal, ModeSidebar, ModeTerminateConfirm,
		},
		Help: "  q / ctrl+c  quit",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			switch mode {
			case ModeList:
				t.m = Update(t.m, t.quitOrConfirmMsg())
			case ModeTerminateConfirm:
				// A quit keystroke declines the terminate (returning to
				// ModeList so the next keypress reaches ModeQuitConfirm
				// instead of looping back here) and arms the quit confirm
				// rather than quitting directly (issue #1215).
				t.m = Update(t.m, TerminateCancelledMsg{})
				t.m = Update(t.m, QuitRequestedMsg{})
			default: // ModeRebuildOutput, ModeDetailModal, ModeSidebar
				t.m = Update(t.m, QuitMsg{})
			}
			return t, nil
		},
	},
	{
		Keys: []string{"?"}, Modes: []Mode{ModeList, ModeHelp},
		Help: "  ?           toggle this help",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			t.m = Update(t.m, HelpToggleMsg{})
			return t, nil
		},
	},
	{
		// ModeHelp's own "esc" close, folded silently into the "?" entry's
		// Help text above rather than duplicated.
		Keys: []string{"esc"}, Modes: []Mode{ModeHelp},
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			t.m = Update(t.m, HelpToggleMsg{})
			return t, nil
		},
	},
	{
		Keys: []string{"d", "enter", "t"}, Modes: []Mode{ModeQuitConfirm},
		Footer: "quit with live Dispatches: drain (d, default) / terminate-all (t) / stay (s)?",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			if msg.String() == "t" {
				if t.launch != nil {
					for _, num := range t.launch.LiveIssues() {
						t.launch.TerminateAsync(t.tracker, num)
					}
				}
			}
			t.m = Update(t.m, QuitMsg{})
			return t, nil
		},
	},
	{
		// Filter-edit's text-input keys: no footer/help text of their own —
		// backspace/typed runes read back through the "/%s" echo itself.
		Keys: []string{"backspace", "runes", "space"}, Modes: []Mode{ModeFilterEdit},
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			switch msg.Type {
			case tea.KeyBackspace:
				if n := len(t.m.Filter); n > 0 {
					t.m = Update(t.m, FilterChangedMsg{Filter: t.m.Filter[:n-1]})
				}
			case tea.KeyRunes, tea.KeySpace:
				t.m = Update(t.m, FilterChangedMsg{Filter: t.m.Filter + msg.String()})
			}
			return t, nil
		},
	},
}

// binding returns the keymap entry naming key under mode, or nil if none
// does — the shared lookup footerHint and footerHintCompact both filter
// down from (issue #1789 review).
func binding(mode Mode, key string) *Binding {
	for i := range keymap {
		b := &keymap[i]
		if slices.Contains(b.Modes, mode) && slices.Contains(b.Keys, key) {
			return b
		}
	}
	return nil
}

// footerHint returns the Footer text keymap declares for key in mode, or ""
// if no matching binding carries footer text — view.go's footer builders
// look up each hint they show by name, so the bracketed text itself has
// exactly one source (issue #1789).
func footerHint(mode Mode, key string) string {
	if b := binding(mode, key); b != nil {
		return b.Footer
	}
	return ""
}

// footerHintCompact is footerHint's counterpart for the one footer tight
// enough to need shorter wording (the docked sidebar) — it prefers a
// binding's FooterCompact and falls back to Footer when unset.
func footerHintCompact(mode Mode, key string) string {
	b := binding(mode, key)
	if b == nil {
		return ""
	}
	if b.FooterCompact != "" {
		return b.FooterCompact
	}
	return b.Footer
}

// filterEditKeyName maps msg to the pseudo-key name keymap declares for
// ModeFilterEdit's text-editing keys ("enter", "esc", "backspace", "runes",
// "space") — ModeFilterEdit is the one mode whose original handler
// (handleFilterKey) switched on msg.Type rather than msg.String(), since a
// typed rune's own String() is the literal character, not a name a keymap
// entry could list ahead of time. dispatchKey (tea.go) uses this in place of
// msg.String() only while ModeFilterEdit is active, so the same generic
// table lookup every other mode uses still finds the right entry; the
// binding's Action reads msg directly to recover the actual rune typed
// (issue #1790). Falls back to msg.String() for any other tea.KeyType,
// matching handleFilterKey's own silent no-op on an unrecognized type.
func filterEditKeyName(msg tea.KeyMsg) string {
	switch msg.Type {
	case tea.KeyEnter:
		return "enter"
	case tea.KeyEsc:
		return "esc"
	case tea.KeyBackspace:
		return "backspace"
	case tea.KeyRunes:
		return "runes"
	case tea.KeySpace:
		return "space"
	default:
		return msg.String()
	}
}
