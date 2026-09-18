package console

import (
	tea "github.com/charmbracelet/bubbletea"
)

var terminateBindings = []Binding{
	{
		Keys: []string{"y", "Y"}, Modes: []Mode{ModeTerminateConfirm},
		Footer: "[y/N/q/ctrl+c]",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			num := t.m.TerminateConfirm.Number
			if t.launch != nil {
				// Terminate logs a reap failure to stderr itself (launcher.go);
				// logging it again here duplicates the line and smears the
				// alt-screen render mid-frame. picks is the queue at initiation;
				// the PickTerminated transition arrives later via a
				// refreshSignalMsg (#1542).
				picks := t.launch.TerminateAsync(t.tracker, num)
				t = t.apply(QueueSnapshotMsg{Picks: picks})
			}
			t = t.apply(TerminateConfirmedMsg{Number: num})
			return t, nil
		},
	},
}
