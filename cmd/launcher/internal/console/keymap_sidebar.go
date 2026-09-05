package console

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
)

// sidebarBindings is the keymap cluster for ModeSidebar's focus, scroll, and
// zoom keys (entries 12-19 of the original keymap literal).
var sidebarBindings = []Binding{
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
			if resolveLayout(t.m).sidebarArrangement == arrangementSidebarDocked {
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
		// FooterCompact shortened to make room for the docked footer's new
		// "H/L" hint (issue #1846) within the 42-column sidebarWidth floor.
		FooterCompact: "[z]",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			t.m = Update(t.m, SidebarZoomToggleMsg{})
			return t, nil
		},
	},
}
