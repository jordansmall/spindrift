package console

import (
	"testing"

	"github.com/charmbracelet/x/exp/golden"
)

// goldenHeaderModel builds a Model that exercises the banner, the status
// line, and a representative alert (stale) and notice (dogfood) — enough
// role/glyph combinations in one frame to catch a styling regression
// without every alert needing its own snapshot.
func goldenHeaderModel() Model {
	m := NewModel()
	m = Update(m, SizeChangedMsg{Width: 80, Height: 24})
	m = Update(m, CapMsg{Cap: 3, Live: 1})
	m.Picks = []Pick{
		{Number: "1", State: PickQueued},
		{Number: "2", State: PickHeld},
		{Number: "3", State: PickSettled},
		{Number: "4", State: PickFailed},
	}
	m = Update(m, StaleStatusMsg{Stale: true, Message: "rebuild needed"})
	m = Update(m, DogfoodNoticeMsg{Live: true})
	return m
}

// TestView_Header_Golden_Styled pins the header's exact byte output — banner,
// status line, and alerts, all styled by role (ADR 0031) — on a
// color-capable terminal, so a change to the palette-resolver or the glyph
// set shows up as a diff here instead of silently shipping (issue #1499 AC).
func TestView_Header_Golden_Styled(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	golden.RequireEqual(t, []byte(View(goldenHeaderModel())))
}

// TestView_Header_Golden_NoColor pins the same header's exact byte output
// under NO_COLOR, verifying it degrades to readable plain text — no ANSI
// escape sequences at all — rather than just "some subset of styling"
// (issue #1499 AC).
func TestView_Header_Golden_NoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "xterm-256color")

	golden.RequireEqual(t, []byte(View(goldenHeaderModel())))
}
