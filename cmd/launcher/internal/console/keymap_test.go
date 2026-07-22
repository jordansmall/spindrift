package console

import "testing"

// TestKeymapParity fails if any keymap entry has no Action — dispatchKey
// (tea.go) dispatches a keypress by looking up the entry naming its (mode,
// key) and calling that entry's Action directly, so an entry with none can
// never fire, silently dropping whatever key(s) it names. keymap is now the
// single source of both a binding's hint text and its dispatch behaviour,
// which makes this the structural form of the bijection issue #1789
// originally enforced by parsing handleKey's own switch statements: with
// dispatch itself table-driven, there is only one list left to check, so a
// hint and its dispatch can no longer diverge by construction (issue #1790).
func TestKeymapParity(t *testing.T) {
	for i, b := range keymap {
		if b.Action == nil {
			t.Errorf("keymap[%d] (Keys=%v, Modes=%v) has no Action", i, b.Keys, b.Modes)
		}
	}
}
