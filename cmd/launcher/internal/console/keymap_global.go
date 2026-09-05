package console

import (
	tea "github.com/charmbracelet/bubbletea"
)

// globalBindings is the keymap cluster for the cross-mode quit/help keys and
// ModeQuitConfirm's own keys (entries 42-45 of the original keymap literal).
var globalBindings = []Binding{
	{
		Keys: []string{"q", "ctrl+c"},
		Modes: []Mode{
			ModeList, ModeRebuildOutput, ModeDetailModal, ModeSidebar, ModeTerminateConfirm,
		},
		Help: "  q / ctrl+c  quit",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			switch mode {
			case ModeList:
				t = t.apply(t.quitOrConfirmMsg())
			case ModeTerminateConfirm:
				// A quit keystroke declines the terminate (returning to
				// ModeList so the next keypress reaches ModeQuitConfirm
				// instead of looping back here) and arms the quit confirm
				// rather than quitting directly (issue #1215).
				t = t.apply(TerminateCancelledMsg{})
				t = t.apply(QuitRequestedMsg{})
			default: // ModeRebuildOutput, ModeDetailModal, ModeSidebar
				t = t.apply(QuitMsg{})
			}
			return t, nil
		},
	},
	{
		Keys: []string{"?"}, Modes: []Mode{ModeList, ModeHelp},
		Help: "  ?           toggle this help",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			t = t.apply(HelpToggleMsg{})
			return t, nil
		},
	},
	{
		// ModeHelp's own "esc" close, folded silently into the "?" entry's
		// Help text above rather than duplicated.
		Keys: []string{"esc"}, Modes: []Mode{ModeHelp},
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			t = t.apply(HelpToggleMsg{})
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
			t = t.apply(QuitMsg{})
			return t, nil
		},
	},
}
