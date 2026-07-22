package console

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestDimBase_RestylesAlreadyStyledLineToRoleDim verifies dimBase replaces an
// already-styled base line's own SGR escapes with RoleDim's, rather than
// wrapping the existing style: the scrim behind a floating detail modal
// (issue #1760) must read as uniformly dimmed, not as whatever mix of colors
// the list happened to render that frame, and leaving the old escapes in
// place risks a stray reset splitting the new dim style mid-line.
func TestDimBase_RestylesAlreadyStyledLineToRoleDim(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	styled := roleStyle(RoleFailed).Render("bbbbbbbbbb")
	base := "aaaaaaaaaa\n" + styled

	got := dimBase(base)
	want := "\x1b[90maaaaaaaaaa\x1b[0m\n\x1b[90mbbbbbbbbbb\x1b[0m"
	if got != want {
		t.Errorf("dimBase(%q) = %q, want %q", base, got, want)
	}
}

// TestDimBase_PreservesBlankPaddedRowWidth verifies a base row padded out to
// the terminal frame with trailing spaces (padBaseForOverlay's blank rows,
// and the space-padded tail of a short row) keeps its full display width
// after dimBase styles it: compositeOverlay depends on every base row
// already reaching the box's column span, so a style that trimmed a
// space-only or space-tailed line would misalign the box on the very rows
// most likely to sit fully behind it.
func TestDimBase_PreservesBlankPaddedRowWidth(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	blank := "          "
	tailed := "abc       "
	base := blank + "\n" + tailed

	got := dimBase(base)
	gotLines := strings.Split(got, "\n")
	want := []string{blank, tailed}
	for i := range want {
		if w := ansi.StringWidth(gotLines[i]); w != len(want[i]) {
			t.Errorf("dimBase(%q) row %d = %q, display width %d, want %d", base, i, gotLines[i], w, len(want[i]))
		}
	}
}
