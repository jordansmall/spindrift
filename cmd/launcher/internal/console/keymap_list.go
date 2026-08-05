package console

import (
	tea "github.com/charmbracelet/bubbletea"
)

// listBindings is the keymap cluster for ModeList's row navigation and
// filter-entry keys (entries 1-11 of the original keymap literal).
var listBindings = []Binding{
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
		Keys: []string{"H", "L"}, Modes: []Mode{ModeList, ModeSidebar},
		Help: "  H / L       switch to the previous / next Section (from the\n" +
			"              sidebar, also closes it)",
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
					return t, openSidebarCmd(t.launch, t.pwd, iss.Number, iss.Title, true)
				}
				return t.openDetailModal(iss)
			}
			if p, ok := t.highlightedPick(); ok {
				if hasTranscript(p.State) {
					return t, openSidebarCmd(t.launch, t.pwd, p.Number, p.Title, false)
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
}
