package console

import (
	"slices"

	tea "github.com/charmbracelet/bubbletea"
)

// Action applies a keypress to the tea layer's model and returns the result,
// the same (teaModel, tea.Cmd) shape handleKey returns. dispatchKey (tea.go)
// passes mode explicitly rather than letting the Action re-derive it from
// t.m.ActiveMode(), so one entry can span several modes and switch on mode
// itself instead of splitting into near-duplicate entries (issue #1790).
type Action func(t teaModel, msg tea.KeyMsg, mode Mode) (teaModel, tea.Cmd)

// Binding is one entry in the console's declarative keymap, the single source
// of truth for both a key's hint text and its dispatch behaviour.
// TestKeymapParity (keymap_test.go) fails on any entry without an Action
// (issue #1789, #1790).
type Binding struct {
	// Keys are literal key names in msg.String() form, e.g. "j", "down".
	Keys []string
	// Modes are the modes dispatchKey looks Keys up under. The quit entry
	// deliberately omits ModeHelp, ModeFilterEdit and ModeQuitConfirm, which
	// each handle "q" differently or not at all.
	Modes []Mode
	// Help is this binding's verbatim line(s) in the "?" overlay and may embed
	// "\n". Empty when another entry's Help already documents the key.
	Help string
	// Footer is the short "[key] verb" fragment for a per-view footer hint.
	// Empty when the binding never appears in a footer.
	Footer string
	// FooterCompact overrides Footer for the docked sidebar's 42-column budget
	// (view.go). Set it only where it differs from Footer.
	FooterCompact string
	Action        Action
}

// keymap is every binding dispatchKey dispatches through. Concat order fixes
// the line order of the "?" overlay renderHelp builds from the Help fields,
// pinned by TestRenderHelp_Golden. An entry with an empty Help contributes no
// overlay line and exists only to carry Footer text or to hold
// TestKeymapParity's bijection (issue #1789).
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

// footerHint returns the Footer text keymap declares for key in mode, or "".
// view.go's footer builders look each hint up by name so the bracketed text
// has exactly one source (issue #1789).
func footerHint(mode Mode, key string) string {
	if b := binding(mode, key); b != nil {
		return b.Footer
	}
	return ""
}

// footerHintCompact prefers a binding's FooterCompact and falls back to Footer
// when it is unset.
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
// ModeFilterEdit's text-editing keys, needed because a typed rune's String()
// is the literal character, not a name an entry could list ahead of time; the
// Action reads msg to recover the rune (issue #1790). Any other key type falls
// back to msg.String(), which matches no entry in this mode and so no-ops.
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
