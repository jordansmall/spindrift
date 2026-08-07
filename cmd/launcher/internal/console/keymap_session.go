package console

import (
	tea "github.com/charmbracelet/bubbletea"
)

// sessionBindings is the keymap cluster for ModeList's session-level keys —
// orphan adoption, parallelism cap, rebuild, and rebuild-output open
// (entries 36-40 of the original keymap literal).
var sessionBindings = []Binding{
	{
		Keys: []string{"A"}, Modes: []Mode{ModeList},
		Help: "  A           adopt the highlighted orphan-flagged Backlog row (a\n" +
			"              running sandbox this session didn't launch); reports\n" +
			"              why and changes nothing without an open PR",
		Action: func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd) {
			if t.m.ActiveSection == SectionBacklog && t.launch != nil && t.launch.RecoverFn != nil {
				if iss, ok := t.highlightedIssue(); ok && t.m.IsOrphan(iss.Number) && !t.m.IsAdoptingOrphan(iss.Number) {
					t.m = Update(t.m, AdoptOrphanStartedMsg{Number: iss.Number})
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
				// Resize's own Grown signal only reaches a drain already
				// running; a session with no active drain (nothing picked
				// yet, or the last one already went idle) has no listener to
				// catch it, so a raise falls back to tryLaunch — a no-op if a
				// drain is in fact already running, or if nothing is
				// queued/held to launch into the freed slot (#754).
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
				t.m = Update(t.m, RebuildOutputOpenMsg{})
			}
			return t, nil
		},
	},
}
