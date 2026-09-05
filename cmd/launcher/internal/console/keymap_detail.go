package console

import (
	tea "github.com/charmbracelet/bubbletea"
)

// detailBindings is the keymap cluster for ModeDetailModal's scroll and
// action keys (entries 20-28 of the original keymap literal).
var detailBindings = []Binding{
	{
		Keys: []string{"esc"}, Modes: []Mode{ModeDetailModal},
		Help:   "  esc         close the ticket detail modal (while it is open)",
		Footer: "[esc] close",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			t = t.apply(DetailModalCloseMsg{})
			return t, nil
		},
	},
	{
		Keys: []string{"j", "down", "k", "up"}, Modes: []Mode{ModeDetailModal},
		Help: "  j/k, up/down  scroll the ticket detail modal's body (while it is\n" +
			"              open)",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			delta := 1
			if s := msg.String(); s == "k" || s == "up" {
				delta = -1
			}
			t = t.apply(DetailModalScrollMsg{Delta: delta})
			return t, nil
		},
	},
	{
		Keys: []string{"pgdown", "ctrl+f", "pgup", "ctrl+b"}, Modes: []Mode{ModeDetailModal},
		Help: "  ctrl+f/ctrl+b, pgdown/pgup  page the ticket detail modal's body\n" +
			"              (while it is open)",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			delta := t.currentLayout().detailScrollBudget
			if s := msg.String(); s == "pgup" || s == "ctrl+b" {
				delta = -delta
			}
			t = t.apply(DetailModalScrollMsg{Delta: delta})
			return t, nil
		},
	},
	{
		Keys: []string{"ctrl+d", "ctrl+u"}, Modes: []Mode{ModeDetailModal},
		Help: "  ctrl+d/ctrl+u  scroll the ticket detail modal's body a half page\n" +
			"              (while it is open)",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			delta := t.currentLayout().detailScrollBudget / 2
			if msg.String() == "ctrl+u" {
				delta = -delta
			}
			t = t.apply(DetailModalScrollMsg{Delta: delta})
			return t, nil
		},
	},
	{
		Keys: []string{"G"}, Modes: []Mode{ModeDetailModal},
		Help: "  G           jump to the ticket detail modal's last page (while it\n" +
			"              is open)",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			t = t.apply(DetailModalJumpToLastMsg{})
			return t, nil
		},
	},
	{
		Keys: []string{"g"}, Modes: []Mode{ModeDetailModal},
		Help: "  gg          jump to the ticket detail modal's first page (while\n" +
			"              it is open; same \"g\" leader as the list body's gg)",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			var cmd tea.Cmd
			t, cmd = armPendingG(t)
			return t, cmd
		},
	},
	{
		Keys: []string{"p"}, Modes: []Mode{ModeDetailModal},
		Help: "  p           pick the displayed issue as a work-kind dispatch\n" +
			"              (same launch button as the Backlog's \"p\"), then close\n" +
			"              the modal",
		Footer: "[p] pick",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			return t.pickDetailModalIssue(KindWork), nil
		},
	},
	{
		Keys: []string{"r"}, Modes: []Mode{ModeDetailModal},
		Help: "  r           pick the displayed issue as a research dispatch\n" +
			"              (advise-only: posts one verdict comment, never opens a\n" +
			"              branch/PR), then close the modal",
		Footer: "[r] research",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			return t.pickDetailModalIssue(KindResearch), nil
		},
	},
	{
		Keys: []string{"u"}, Modes: []Mode{ModeDetailModal},
		Help:   "  u           unpick the displayed issue's queued pick, if any",
		Footer: "[u] unpick",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			return t.unpickDetailModalIssue(), nil
		},
	},
}
