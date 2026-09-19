package console

import (
	"testing"

	"github.com/charmbracelet/x/exp/golden"

	"spindrift.dev/launcher/internal/forge"
)

// The fixture packs a stale alert and a dogfood notice into one frame so the
// snapshot covers enough role and glyph combinations to catch a styling
// regression without a separate snapshot per alert.
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
	m = Update(m, StaleStatusMsg{RebuildStatus: RebuildStatus{Stale: true, Message: "rebuild needed"}})
	m = Update(m, DogfoodNoticeMsg{Live: true})
	return m
}

// Pins the header's exact bytes with role styling (ADR 0031) on a
// color-capable terminal, so a change to the palette resolver or the glyph
// set shows up as a diff here instead of shipping silently (issue #1499 AC).
func TestView_Header_Golden_Styled(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	golden.RequireEqual(t, []byte(View(goldenHeaderModel())))
}

// Pins the same header under NO_COLOR, which must degrade to plain text with
// no ANSI escape sequences at all, not just less styling (issue #1499 AC).
func TestView_Header_Golden_NoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "xterm-256color")

	golden.RequireEqual(t, []byte(View(goldenHeaderModel())))
}

// The size is the narrowest width that still docks, and short enough to keep
// the golden file small, so one snapshot covers the bordered panels, the
// docked footer hints, and the width and height budget math (issue #1755).
func goldenDockedModel() Model {
	m := NewModel()
	m = Update(m, SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + dockedBorderCols, Height: 12})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1", Title: "still visible", Labels: []string{"bug"}}}})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "#42 · hi"}}})
	return m
}

// Pins the docked panels' exact bytes, rounded RoleDim borders around both
// columns, so a change to the border styling or the budget math shows up as
// a diff here (issue #1755 AC).
func TestView_Docked_Golden_Styled(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	golden.RequireEqual(t, []byte(View(goldenDockedModel())))
}

// Pins the same docked layout under NO_COLOR, where the panel borders must
// degrade to plain ASCII glyphs with no ANSI escape sequences (issue #1755
// AC).
func TestView_Docked_Golden_NoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "xterm-256color")

	golden.RequireEqual(t, []byte(View(goldenDockedModel())))
}
