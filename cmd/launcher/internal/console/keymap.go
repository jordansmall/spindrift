package console

import (
	"slices"

	tea "github.com/charmbracelet/bubbletea"
)

// Action is a keymap entry's dispatch behaviour: given the tea layer's
// model, the triggering keypress, and the mode dispatchKey (tea.go) looked
// it up under, it applies whatever Msg(s) that key means and returns the
// resulting teaModel plus any tea.Cmd to run — the same (teaModel, tea.Cmd)
// shape handleKey itself returns. mode is passed explicitly rather than
// re-derived via t.m.ActiveMode() so an entry spanning several modes with
// different behaviour (e.g. the shared quit entry below) can switch on mode
// inside its own Action rather than splitting into several near-duplicate
// entries (issue #1790).
type Action func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd)

// Binding is one entry in the console's declarative keymap: the single
// source of truth for both a key's hint text (the "?" help overlay and
// per-view footers) and its dispatch behaviour. dispatchKey (tea.go) is the
// only caller of Action; TestKeymapParity (keymap_test.go) enforces that
// every entry carries one (issue #1789, #1790).
type Binding struct {
	// Keys are the literal key names (msg.String() form) this binding's
	// Action fires on, e.g. []string{"j", "down"}.
	Keys []string
	// Modes are the Mode(s) dispatchKey looks Keys up under. The quit entry
	// below lists every Mode where "q"/"ctrl+c" hard-quits ("global" in the
	// loose sense the console uses it, not literally every Mode: ModeHelp,
	// ModeFilterEdit, and ModeQuitConfirm each handle "q" differently or not
	// at all, so they're deliberately left out of that entry's Modes).
	Modes []Mode
	// Help is this binding's line(s) in the "?" overlay, verbatim (may embed
	// "\n" for a wrapped continuation). Empty when the binding has no
	// standalone overlay entry of its own — usually because another entry's
	// Help already documents it (e.g. the rebuild-output pane's scroll keys
	// are folded into "o"'s own paragraph).
	Help string
	// Footer is this binding's short "[key] verb" fragment for a per-view
	// footer hint. Empty when the binding never appears in a footer.
	Footer string
	// FooterCompact overrides Footer for the one footer tight enough to need
	// shorter wording (the docked sidebar's 42-column budget, view.go). Only
	// set where it actually differs from Footer.
	FooterCompact string
	// Action is this binding's dispatch behaviour — see Action's own doc
	// comment. Every entry in keymap below carries one; TestKeymapParity
	// fails on any that doesn't (issue #1790).
	Action Action
}

// keymap is every binding dispatchKey dispatches through, assembled from the
// per-cluster files below in the same order the retired static renderHelp
// slice documented them in, so rebuilding that slice from Help fields
// reproduces its text exactly. Entries with an empty Help contribute no
// overlay line of their own — they exist only so TestKeymapParity's
// bijection holds and/or to carry Footer text for a per-view footer
// (issue #1789).
var keymap = slices.Concat(
	listBindings, sidebarBindings, detailBindings, queueBindings,
	terminateBindings, sessionBindings, rebuildBindings, globalBindings,
	filterBindings,
)

// binding returns the keymap entry naming key under mode, or nil if none
// does — the shared lookup footerHint and footerHintCompact both filter
// down from (issue #1789 review).
func binding(mode Mode, key string) *Binding {
	for i := range keymap {
		b := &keymap[i]
		if slices.Contains(b.Modes, mode) && slices.Contains(b.Keys, key) {
			return b
		}
	}
	return nil
}

// footerHint returns the Footer text keymap declares for key in mode, or ""
// if no matching binding carries footer text — view.go's footer builders
// look up each hint they show by name, so the bracketed text itself has
// exactly one source (issue #1789).
func footerHint(mode Mode, key string) string {
	if b := binding(mode, key); b != nil {
		return b.Footer
	}
	return ""
}

// footerHintCompact is footerHint's counterpart for the one footer tight
// enough to need shorter wording (the docked sidebar) — it prefers a
// binding's FooterCompact and falls back to Footer when unset.
func footerHintCompact(mode Mode, key string) string {
	b := binding(mode, key)
	if b == nil {
		return ""
	}
	if b.FooterCompact != "" {
		return b.FooterCompact
	}
	return b.Footer
}

// filterEditKeyName maps msg to the pseudo-key name keymap declares for
// ModeFilterEdit's text-editing keys ("enter", "esc", "backspace", "runes",
// "space") — ModeFilterEdit is the one mode whose original handler
// (handleFilterKey) switched on msg.Type rather than msg.String(), since a
// typed rune's own String() is the literal character, not a name a keymap
// entry could list ahead of time. dispatchKey (tea.go) uses this in place of
// msg.String() only while ModeFilterEdit is active, so the same generic
// table lookup every other mode uses still finds the right entry; the
// binding's Action reads msg directly to recover the actual rune typed
// (issue #1790). Falls back to msg.String() for any other tea.KeyType,
// matching handleFilterKey's own silent no-op on an unrecognized type.
func filterEditKeyName(msg tea.KeyMsg) string {
	switch msg.Type {
	case tea.KeyEnter:
		return "enter"
	case tea.KeyEsc:
		return "esc"
	case tea.KeyBackspace:
		return "backspace"
	case tea.KeyRunes:
		return "runes"
	case tea.KeySpace:
		return "space"
	default:
		return msg.String()
	}
}
