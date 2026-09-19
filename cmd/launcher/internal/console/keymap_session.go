package console

import (
	tea "github.com/charmbracelet/bubbletea"
)

// sessionBindings holds ModeList's session-level keys: orphan adoption,
// parallelism cap, rebuild, and rebuild-output open.
var sessionBindings = []Binding{
	{
		Keys: []string{"A"}, Modes: []Mode{ModeList},
		Help: "  A           adopt the highlighted orphan-flagged Backlog row (a\n" +
			"              running sandbox this session didn't launch); reports\n" +
			"              why and changes nothing without an open PR",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			if t.m.ActiveSection == SectionBacklog && t.launch != nil && t.launch.RecoverFn != nil {
				if iss, ok := t.highlightedIssue(); ok && t.m.IsOrphan(iss.Number) && !t.m.IsAdoptingOrphan(iss.Number) {
					t = t.apply(AdoptOrphanStartedMsg{Number: iss.Number})
					return t, adoptOrphanCmd(t.launch, iss.Number)
				}
			}
			return t, nil
		},
	},
	{
		Keys: []string{"+"}, Modes: []Mode{ModeList},
		Help: "  +           raise the live parallelism cap",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			if t.launch != nil {
				t.launch.Resize(1)
				// Resize's Grown signal only reaches a drain that is already
				// running, so a session with no active drain would miss the
				// raise. tryLaunch covers that case and is a no-op when a
				// drain is running or nothing is queued or held (#754).
				t.launch.tryLaunch(t.tracker, t.pwd)
			}
			return t, nil
		},
	},
	{
		Keys: []string{"-"}, Modes: []Mode{ModeList},
		Help: "  -           lower the live parallelism cap",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			if t.launch != nil {
				t.launch.Resize(-1)
			}
			return t, nil
		},
	},
	{
		Keys: []string{"b"}, Modes: []Mode{ModeList},
		Help: "  b           rebuild the stale image in-session",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			if t.launch != nil && t.m.RebuildStatus.Stale {
				t.launch.Rebuild(t.tracker, t.pwd)
			}
			return t, nil
		},
	},
	{
		Keys: []string{"o"}, Modes: []Mode{ModeList},
		Help: "  o           open the rebuild output pane (once a rebuild has run);\n" +
			"              j/k, ctrl+f/ctrl+b, pgup/pgdown scroll it, x/esc closes\n" +
			"              G jumps to its last page, gg to its first (\"g\" arms a\n" +
			"              pending leader, awaiting a trailing \"g\");\n" +
			"              ctrl+d/ctrl+u scroll it a half page (half of ctrl+f/ctrl+b)",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			if t.m.RebuildStatus.Output != "" {
				t = t.apply(RebuildOutputOpenMsg{})
			}
			return t, nil
		},
	},
}
