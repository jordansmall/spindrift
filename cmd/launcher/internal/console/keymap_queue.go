package console

import (
	tea "github.com/charmbracelet/bubbletea"
)

// queueBindings is the keymap cluster for ModeList's pick/research/refresh/
// unpick/terminate keys (entries 29-34 of the original keymap literal).
var queueBindings = []Binding{
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
		Keys: []string{"r"}, Modes: []Mode{ModeList},
		Help:   "  r           research the highlighted Backlog row (advise-only pick)",
		Footer: "[r] research",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			return t.pickHighlighted(KindResearch), nil
		},
	},
	{
		Keys: []string{"R"}, Modes: []Mode{ModeList},
		Help:   "  R           refresh the backlog",
		Footer: "[R] refresh",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			t = t.apply(DetailCacheInvalidatedMsg{})
			return t, refreshCmd(t.tracker)
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
				t = t.apply(TerminateRequestedMsg{Number: num})
			}
			return t, nil
		},
	},
}
