package console

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
)

// sidebarBindings holds ModeSidebar's focus, scroll, and zoom keys.
var sidebarBindings = []Binding{
	{
		Keys: []string{"t"}, Modes: []Mode{ModeSidebar},
		Help: "  t           cycle the sidebar's activity feed -> transcript ->\n" +
			"              raw JSONL -> activity feed (while the sidebar has focus)",
		Footer:        "[t] cycle activity/transcript",
		FooterCompact: "[t] cycle",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			t = t.apply(SidebarToggleMsg{})
			return t, nil
		},
	},
	{
		// This returns focus to the list in the docked layout. ModeList has its
		// own "h"/"left" no-op in handleListKey instead.
		Keys: []string{"h", "left"}, Modes: []Mode{ModeSidebar},
		Footer: "[h] list",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			if t.currentLayout().sidebarArrangement == arrangementSidebarDocked {
				t = t.apply(FocusListMsg{})
			}
			return t, nil
		},
	},
	{
		Keys: []string{"x", "esc"}, Modes: []Mode{ModeSidebar, ModeList},
		Help:   "  x / esc     close the sidebar (while it has focus)",
		Footer: "[x] close",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			// The guard only matters for ModeList with no sidebar open. A
			// fullscreen or zoomed sidebar routes to ModeSidebar via ActiveMode.
			if mode == ModeSidebar || t.m.Sidebar != nil {
				t = t.apply(SidebarCloseMsg{})
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
			t = t.apply(SidebarScrollMsg{Delta: delta})
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
			t = t.apply(SidebarScrollMsg{Delta: delta})
			return t, nil
		},
	},
	{
		Keys: []string{"G", "end"}, Modes: []Mode{ModeSidebar},
		Help: "  G / end     re-attach follow and jump to the sidebar's bottom\n" +
			"              (while the sidebar has focus)",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			t = t.apply(SidebarJumpToEndMsg{})
			return t, nil
		},
	},
	{
		Keys: []string{"g"}, Modes: []Mode{ModeSidebar},
		Help: "  gg          detach follow and jump to the sidebar's top (while it\n" +
			"              has focus; same \"g\" leader as the list body's gg)",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			var cmd tea.Cmd
			t, cmd = armPendingG(t)
			return t, cmd
		},
	},
	{
		Keys: []string{"z"}, Modes: []Mode{ModeSidebar},
		Help: "  z           toggle the sidebar's fullscreen zoom (while it has\n" +
			"              focus)",
		Footer: "[z] zoom",
		// Shortened to fit the docked footer's "H/L" hint inside the 42-column
		// sidebarWidth floor (issue #1846).
		FooterCompact: "[z]",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			t = t.apply(SidebarZoomToggleMsg{})
			return t, nil
		},
	},
}
