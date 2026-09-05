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
				t = t.apply(RebuildOutputCloseMsg{})
			case "j", "down":
				t = t.apply(RebuildOutputScrollMsg{Delta: 1})
			case "k", "up":
				t = t.apply(RebuildOutputScrollMsg{Delta: -1})
			case "pgdown", "ctrl+f":
				t = t.apply(RebuildOutputScrollMsg{Delta: fixedPaneScrollDelta})
			case "pgup", "ctrl+b":
				t = t.apply(RebuildOutputScrollMsg{Delta: -fixedPaneScrollDelta})
			case "ctrl+d":
				t = t.apply(RebuildOutputScrollMsg{Delta: fixedPaneScrollDelta / 2})
			case "ctrl+u":
				t = t.apply(RebuildOutputScrollMsg{Delta: -(fixedPaneScrollDelta / 2)})
			case "G":
				t = t.apply(RebuildOutputJumpToLastMsg{})
			case "g":
				var cmd tea.Cmd
				t, cmd = armPendingG(t)
				return t, cmd
			}
			return t, nil
		},
	},
}
