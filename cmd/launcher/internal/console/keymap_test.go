package console

import "testing"

// dispatchKey (tea.go) dispatches a keypress by calling the Action on the
// entry naming its (mode, key), so an entry with no Action never fires and
// silently drops every key it names. Because keymap is the single source of
// both hint text and dispatch, this check is the structural form of the
// bijection that issue #1789 originally enforced by parsing handleKey (#1790).
func TestKeymapParity(t *testing.T) {
	for i, b := range keymap {
		if b.Action == nil {
			t.Errorf("keymap[%d] (Keys=%v, Modes=%v) has no Action", i, b.Keys, b.Modes)
		}
	}
}

// dispatchKey (tea.go) looks a keypress up by (Mode, key), so two entries
// claiming the same pair leave one permanently unreachable. Entries may share
// a Keys value across different Modes, since dispatchKey scopes its lookup by
// mode; only the narrower (Mode, key) pair must stay unique.
func TestKeymapUniqueness(t *testing.T) {
	type pair struct {
		mode Mode
		key  string
	}
	seen := make(map[pair]int)
	for i, b := range keymap {
		for _, mode := range b.Modes {
			for _, key := range b.Keys {
				p := pair{mode: mode, key: key}
				if j, ok := seen[p]; ok {
					t.Errorf("keymap[%d] and keymap[%d] both claim (Mode=%v, Key=%q)", j, i, mode, key)
					continue
				}
				seen[p] = i
			}
		}
	}
}
