package console

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// dimBase replaces an already-styled line's own SGR escapes with RoleDim's
// instead of wrapping them, so the scrim behind a floating detail modal
// (issue #1760) reads as uniformly dimmed and no leftover reset splits the
// new dim style mid-line.
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

// compositeOverlay depends on every base row already reaching the box's
// column span, so a style that trimmed a space-only or space-tailed row
// would misalign the box on the very rows most likely to sit behind it.
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
