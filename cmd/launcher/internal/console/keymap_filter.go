package console

import (
	tea "github.com/charmbracelet/bubbletea"
)

// filterBindings is the keymap cluster for ModeFilterEdit's text-input keys
// (entry 46 of the original keymap literal).
var filterBindings = []Binding{
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
