package console

import (
	tea "github.com/charmbracelet/bubbletea"
)

// rebuildBindings is the keymap cluster for ModeRebuildOutput's scroll/close
// keys (entry 41 of the original keymap literal).
var rebuildBindings = []Binding{
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
}
