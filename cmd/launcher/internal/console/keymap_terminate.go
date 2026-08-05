package console

import (
	tea "github.com/charmbracelet/bubbletea"
)

// terminateBindings is the keymap cluster for ModeTerminateConfirm's
// confirm/decline key (entry 35 of the original keymap literal).
var terminateBindings = []Binding{
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
}
