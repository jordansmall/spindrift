package console

import (
	"testing"

	"github.com/charmbracelet/x/exp/golden"

	"spindrift.dev/launcher/internal/forge"
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
	m = Update(m, StaleStatusMsg{RebuildStatus: RebuildStatus{Stale: true, Message: "rebuild needed"}})
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

// goldenDockedModel builds a Model with the docked sidebar open at a
// representative terminal size — wide enough to dock, short enough to keep
// the golden file small — so the bordered list/sidebar panels, the docked
// footer hints, and the width/height budget math all land in one snapshot
// (issue #1755).
func goldenDockedModel() Model {
	m := NewModel()
	m = Update(m, SizeChangedMsg{Width: sidebarMinListWidth + sidebarWidth + dockedBorderCols, Height: 12})
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1", Title: "still visible", Labels: []string{"bug"}}}})
	m = Update(m, SidebarLoadedMsg{Number: "42", Activity: []ActivityLine{{Text: "#42 · hi"}}})
	return m
}

// TestView_Docked_Golden_Styled pins the docked list/sidebar panels' exact
// byte output — rounded RoleDim borders around both columns — on a
// color-capable terminal, so a change to the border styling or the
// width/height budget math shows up as a diff here (issue #1755 AC).
func TestView_Docked_Golden_Styled(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	golden.RequireEqual(t, []byte(View(goldenDockedModel())))
}

// TestView_Docked_Golden_NoColor pins the same docked layout's exact byte
// output under NO_COLOR, verifying the panel borders degrade to plain ASCII
// glyphs with no ANSI escape sequences at all (issue #1755 AC).
func TestView_Docked_Golden_NoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "xterm-256color")

	golden.RequireEqual(t, []byte(View(goldenDockedModel())))
}
