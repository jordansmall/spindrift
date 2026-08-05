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

// TestKeymapUniqueness fails if any (Mode, key) pair is claimed by more than
// one keymap entry — dispatchKey (tea.go) looks a keypress up by exactly
// that pair, so a collision would make one of the two entries permanently
// unreachable under that mode. Several entries legitimately share a Keys
// value across different Modes (e.g. "esc" appears under ModeFilterEdit,
// ModeDetailModal, ModeHelp, and paired with ModeSidebar/ModeList
// elsewhere) — that's fine, since dispatchKey scopes its lookup by mode.
// It's the narrower (mode, key) pair that must stay unique.
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
