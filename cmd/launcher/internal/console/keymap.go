package console

import (
	"fmt"
	"slices"
)

// Binding is one entry in the console's declarative keymap: the single
// source of truth for a key's hint text, both in the "?" help overlay and in
// per-view footers. handleKey (tea.go) still owns dispatch — this table only
// has to stay in sync with which keys it recognizes, which
// TestKeymapParity (keymap_test.go) enforces (issue #1789).
type Binding struct {
	// Keys are the literal key names (msg.String() form) some handleKey
	// sub-handler recognizes for this binding, e.g. []string{"j", "down"}.
	Keys []string
	// Modes are the Mode(s) whose handler recognizes Keys. Several entries
	// below list every Mode where isQuitKey fires ("global" in the loose
	// sense the console uses it, not literally every Mode: ModeHelp,
	// ModeFilterEdit, and ModeQuitConfirm each handle "q" differently or not
	// at all, so they're deliberately left out of those entries' Modes).
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
}

// keymap is every binding handleKey's sub-handlers recognize, grouped in the
// same order the retired static renderHelp slice documented them in, so
// rebuilding that slice from Help fields below reproduces its text exactly.
// Entries with an empty Help contribute no overlay line of their own — they
// exist only so TestKeymapParity's bijection holds and/or to carry Footer
// text for a per-view footer (issue #1789).
var keymap = []Binding{
	{
		Keys: []string{"j", "down", "k", "up"}, Modes: []Mode{ModeList},
		Help: "  j/k, down/up  move cursor within the active Section",
	},
	{
		Keys: []string{"G"}, Modes: []Mode{ModeList},
		Help: "  G           jump to the active Section's last row, scrolling it\n" +
			"              into view",
	},
	{
		Keys: []string{"g"}, Modes: []Mode{ModeList},
		Help: "  gg          jump to the active Section's first row (\"g\" arms a\n" +
			"              pending leader, awaiting a trailing \"g\")",
	},
	{
		Keys: []string{"H", "L"}, Modes: []Mode{ModeList},
		Help:   "  H / L       switch to the previous / next Section",
		Footer: "H/L",
	},
	{
		Keys: []string{"1", "2", "3", "4", "5"}, Modes: []Mode{ModeList},
		Help: "  1-5         jump straight to a Section (Backlog, Running, Held,\n" +
			"              Settled, Failed)",
		Footer: "1-5",
	},
	{
		Keys: []string{"pgdown", "ctrl+f", "pgup", "ctrl+b"}, Modes: []Mode{ModeList},
		Help: "  ctrl+f/ctrl+b, pgup/pgdown  jump a full page of the active Section's live\n" +
			"              rendered rows without moving the cursor; the page\n" +
			"              size tracks terminal resizes",
	},
	{
		Keys: []string{"ctrl+d", "ctrl+u"}, Modes: []Mode{ModeList},
		Help: "  ctrl+d/ctrl+u  jump a half page of the active Section's live\n" +
			"              rendered rows without moving the cursor; half of the\n" +
			"              ctrl+f/ctrl+b page above",
	},
	{
		Keys: []string{"/"}, Modes: []Mode{ModeList},
		Help: "  /           filter the Backlog by label substring",
	},
	{
		Keys: []string{"enter"}, Modes: []Mode{ModeList, ModeFilterEdit},
		Help: "  enter       apply filter (while filter-editing); otherwise: open\n" +
			"              the highlighted row's ticket detail (Backlog Section),\n" +
			"              or open the highlighted pick's live-tail sidebar (a\n" +
			"              work Section, only when it has run)",
		Footer: "[enter] apply",
	},
	{
		Keys: []string{"l", "right", "h", "left"}, Modes: []Mode{ModeList},
		Help: "  h/l, left/right  move focus between the list and the sidebar\n" +
			"              (while a sidebar is open)",
	},
	{
		Keys: []string{"esc"}, Modes: []Mode{ModeFilterEdit},
		Help:   "  esc         cancel filter edit",
		Footer: "[esc] cancel",
	},
	{
		Keys: []string{"t"}, Modes: []Mode{ModeSidebar},
		Help: "  t           cycle the sidebar's activity feed -> transcript ->\n" +
			"              raw JSONL -> activity feed (while the sidebar has focus)",
		Footer:        "[t] cycle activity/transcript",
		FooterCompact: "[t] cycle",
	},
	{
		// The docked layout's own "return focus to the list" case (ModeList
		// never sees this key with this meaning — it's handleListKey's own
		// "h"/"left" no-op instead, covered by the entry above).
		Keys: []string{"h", "left"}, Modes: []Mode{ModeSidebar},
		Footer: "[h] list",
	},
	{
		Keys: []string{"x", "esc"}, Modes: []Mode{ModeSidebar, ModeList},
		Help:   "  x / esc     close the sidebar (while it has focus)",
		Footer: "[x] close",
	},
	{
		Keys: []string{"j", "down", "k", "up", "pgdown", "ctrl+f", "pgup", "ctrl+b"}, Modes: []Mode{ModeSidebar},
		Help: "  j/k, ctrl+f/ctrl+b, pgup/pgdown  scroll the sidebar (while it has focus); its\n" +
			fmt.Sprintf("              pgup/pgdown page jump is fixed at %d lines, unlike the", fixedPaneScrollDelta) + "\n" +
			"              body's live-viewport-derived one above; scrolling up\n" +
			"              detaches the running Activity feed's live follow",
	},
	{
		Keys: []string{"ctrl+d", "ctrl+u"}, Modes: []Mode{ModeSidebar},
		Help: "  ctrl+d/ctrl+u  scroll the sidebar a half page (while it has focus,\n" +
			fmt.Sprintf("              fixed at %d lines, half of ctrl+f/ctrl+b above)", fixedPaneScrollDelta/2),
	},
	{
		Keys: []string{"G", "end"}, Modes: []Mode{ModeSidebar},
		Help: "  G / end     re-attach follow and jump to the sidebar's bottom\n" +
			"              (while the sidebar has focus)",
	},
	{
		Keys: []string{"g"}, Modes: []Mode{ModeSidebar},
		Help: "  gg          detach follow and jump to the sidebar's top (while it\n" +
			"              has focus; same \"g\" leader as the list body's gg)",
	},
	{
		Keys: []string{"z"}, Modes: []Mode{ModeSidebar},
		Help: "  z           toggle the sidebar's fullscreen zoom (while it has\n" +
			"              focus)",
		Footer: "[z] zoom",
	},
	{
		Keys: []string{"esc"}, Modes: []Mode{ModeDetailModal},
		Help:   "  esc         close the ticket detail modal (while it is open)",
		Footer: "[esc] close",
	},
	{
		Keys: []string{"j", "down", "k", "up"}, Modes: []Mode{ModeDetailModal},
		Help: "  j/k, up/down  scroll the ticket detail modal's body (while it is\n" +
			"              open)",
		Footer: "[j/k] scroll",
	},
	{
		Keys: []string{"r"}, Modes: []Mode{ModeList},
		Help: "  r           refresh the backlog",
	},
	{
		Keys: []string{"p"}, Modes: []Mode{ModeList},
		Help: "  p           pick the highlighted Backlog row (launch button)",
	},
	{
		Keys: []string{"u"}, Modes: []Mode{ModeList},
		Help: "  u           unpick the highlighted queued pick",
	},
	{
		Keys: []string{"a"}, Modes: []Mode{ModePick},
		Help: "  pa          pick all ready (bulk pick-all-ready gesture)",
	},
	{
		Keys: []string{"r"}, Modes: []Mode{ModePick},
		Help: "  pr          pick the highlighted Backlog row as a research\n" +
			"              dispatch (advise-only: posts one verdict comment,\n" +
			"              never opens a branch/PR)",
	},
	{
		Keys: []string{"X"}, Modes: []Mode{ModeList},
		Help: "  X           terminate the highlighted live Dispatch (confirm y/N,\n" +
			"              q/ctrl+c decline and quit)",
	},
	{
		Keys: []string{"y", "Y"}, Modes: []Mode{ModeTerminateConfirm},
		Footer: "[y/N/q/ctrl+c]",
	},
	{
		Keys: []string{"A"}, Modes: []Mode{ModeList},
		Help: "  A           adopt the highlighted orphan-flagged Backlog row (a\n" +
			"              running sandbox this session didn't launch); reports\n" +
			"              why and changes nothing without a non-draft open PR",
	},
	{
		Keys: []string{"+"}, Modes: []Mode{ModeList},
		Help: "  +           raise the live parallelism cap",
	},
	{
		Keys: []string{"-"}, Modes: []Mode{ModeList},
		Help: "  -           lower the live parallelism cap",
	},
	{
		Keys: []string{"b"}, Modes: []Mode{ModeList},
		Help: "  b           rebuild the stale image in-session",
	},
	{
		Keys: []string{"o"}, Modes: []Mode{ModeList},
		Help: "  o           open the rebuild output pane (once a rebuild has run);\n" +
			"              j/k, ctrl+f/ctrl+b, pgup/pgdown scroll it, x/esc closes\n" +
			"              G jumps to its last page, gg to its first (\"g\" arms a\n" +
			"              pending leader, awaiting a trailing \"g\");\n" +
			"              ctrl+d/ctrl+u scroll it a half page (half of ctrl+f/ctrl+b)",
	},
	{
		Keys: []string{"j", "down", "k", "up", "pgdown", "ctrl+f", "pgup", "ctrl+b",
			"ctrl+d", "ctrl+u", "G", "g", "x", "esc"},
		Modes:  []Mode{ModeRebuildOutput},
		Footer: "[x] close",
	},
	{
		Keys: []string{"q", "ctrl+c"},
		Modes: []Mode{
			ModeList, ModePick, ModeRebuildOutput, ModeDetailModal, ModeSidebar, ModeTerminateConfirm,
		},
		Help: "  q / ctrl+c  quit",
	},
	{
		Keys: []string{"?"}, Modes: []Mode{ModeList, ModeHelp},
		Help: "  ?           toggle this help",
	},
	{
		// ModeHelp's own "esc" close, folded silently into the "?" entry's
		// Help text above rather than duplicated.
		Keys: []string{"esc"}, Modes: []Mode{ModeHelp},
	},
	{
		Keys: []string{"d", "enter", "t"}, Modes: []Mode{ModeQuitConfirm},
		Footer: "quit with live Dispatches: drain (d, default) / terminate-all (t) / stay (s)?",
	},
	{
		// Filter-edit's text-input keys: no footer/help text of their own —
		// backspace/typed runes read back through the "/%s" echo itself.
		Keys: []string{"backspace", "runes", "space"}, Modes: []Mode{ModeFilterEdit},
	},
}

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
