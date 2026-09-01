package console

import (
	"slices"

	tea "github.com/charmbracelet/bubbletea"
)

// Action is a keymap entry's dispatch behaviour. mode is passed explicitly
// rather than re-derived via t.m.ActiveMode() so an entry spanning several
// modes with different behaviour (e.g. the shared quit entry below) can
// switch on mode inside its own Action rather than splitting into several
// near-duplicate entries.
type Action func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd)

// Binding is one entry in the console's declarative keymap: the single
// source of truth for both a key's hint text (the "?" help overlay and
// per-view footers) and its dispatch behaviour. TestKeymapParity
// (keymap_test.go) enforces that every entry carries an Action.
type Binding struct {
	// Keys are the literal key names (msg.String() form) this binding's
	// Action fires on, e.g. []string{"j", "down"}.
	Keys []string
	// Modes are the Mode(s) dispatchKey looks Keys up under. ModeHelp,
	// ModeFilterEdit, and ModeQuitConfirm each handle "q" differently or not
	// at all, so they are deliberately absent from the shared quit entry.
	Modes []Mode
	// Help is this binding's line(s) in the "?" overlay, verbatim (may embed
	// "\n" for a wrapped continuation). Empty when another entry's Help
	// already documents it.
	Help string
	// Footer is this binding's short "[key] verb" fragment for a per-view
	// footer hint. Empty when the binding never appears in a footer.
	Footer string
	// FooterCompact overrides Footer for the one footer tight enough to need
	// shorter wording (the docked sidebar's 42-column budget, view.go). Only
	// set where it actually differs from Footer.
	FooterCompact string
	Action        Action
}

// keymap is every binding dispatchKey dispatches through, ordered so that
// rebuilding the help overlay from Help fields reproduces its text exactly.
// Entries with an empty Help contribute no overlay line of their own — they
// exist only to satisfy TestKeymapParity's bijection and/or to carry Footer
// text.
var keymap = slices.Concat(
	listBindings, sidebarBindings, detailBindings, queueBindings,
	terminateBindings, sessionBindings, rebuildBindings, globalBindings,
	filterBindings,
)

// binding returns the keymap entry naming key under mode, or nil if none does.
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
// if no matching binding carries footer text.
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
// ModeFilterEdit's text-editing keys. A typed rune's own String() is the
// literal character, not a name a keymap entry could list ahead of time, so
// dispatchKey substitutes this for msg.String() while ModeFilterEdit is
// active and the binding's Action reads msg directly to recover the rune.
// Falls back to msg.String() for any other tea.KeyType.
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
