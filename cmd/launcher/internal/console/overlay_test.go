package console

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

func TestCompositeOverlay_InteriorPosition(t *testing.T) {
	base := "aaaaaaaaaa\nbbbbbbbbbb\ncccccccccc"
	box := "XXX"
	got := compositeOverlay(base, box, 2, 1)
	want := "aaaaaaaaaa\nbbXXXbbbbb\ncccccccccc"
	if got != want {
		t.Errorf("compositeOverlay(...) = %q, want %q", got, want)
	}
}

func TestCompositeOverlay_TopLeftCorner(t *testing.T) {
	base := "aaaaaaaaaa\nbbbbbbbbbb"
	box := "XX"
	got := compositeOverlay(base, box, 0, 0)
	want := "XXaaaaaaaa\nbbbbbbbbbb"
	if got != want {
		t.Errorf("compositeOverlay(...) = %q, want %q", got, want)
	}
}

// A base row carrying SGR escapes is sliced by display column, not byte
// offset, so no escape sequence is split. The base style closes before the box
// and reopens after it, so it neither bleeds into the box nor vanishes from
// the untouched trailing text.
func TestCompositeOverlay_ANSIStyledBaseRow(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	styled := roleStyle(RoleFailed).Render("bbbbbbbbbb")
	base := "aaaaaaaaaa\n" + styled
	box := "XXX"

	got := compositeOverlay(base, box, 2, 1)
	gotLines := strings.Split(got, "\n")
	if len(gotLines) != 2 {
		t.Fatalf("compositeOverlay(...) = %q, want 2 lines", got)
	}

	overlaid := gotLines[1]
	want := "\x1b[31mbb\x1b[0mXXX\x1b[31mbbbbb\x1b[0m"
	if overlaid != want {
		t.Errorf("overlaid row = %q, want %q", overlaid, want)
	}
	if runewidth.StringWidth(ansi.Strip(overlaid)) != runewidth.StringWidth(ansi.Strip(styled)) {
		t.Errorf("overlaid row %q display width = %d, want %d (same as base row %q)",
			overlaid, runewidth.StringWidth(ansi.Strip(overlaid)), runewidth.StringWidth(ansi.Strip(styled)), styled)
	}
}

func TestCompositeOverlay_ClipsRightEdge(t *testing.T) {
	base := "aaaaaaaaaa"
	box := "XXXXX"
	got := compositeOverlay(base, box, 8, 0)
	want := "aaaaaaaaXX"
	if got != want {
		t.Errorf("compositeOverlay(...) = %q, want %q", got, want)
	}
}

func TestCompositeOverlay_ClipsBottomEdge(t *testing.T) {
	base := "aaaaaaaaaa\nbbbbbbbbbb"
	box := "XXX\nYYY\nZZZ"
	got := compositeOverlay(base, box, 2, 1)
	want := "aaaaaaaaaa\nbbXXXbbbbb"
	if got != want {
		t.Errorf("compositeOverlay(...) = %q, want %q", got, want)
	}
}

// Measuring by rune count instead of display width would drift the cut
// position by one column for every wide rune before it.
func TestCompositeOverlay_WideRunesNoOffByOneDrift(t *testing.T) {
	base := "永永永永永"
	box := "XX"
	got := compositeOverlay(base, box, 4, 0)
	want := "永永XX永永"
	if got != want {
		t.Errorf("compositeOverlay(...) = %q, want %q", got, want)
	}
}

// The box's own render is self-closing, so splicing it between untouched plain
// spans is enough to keep its color out of the base text after it.
func TestCompositeOverlay_StyledBoxOverPlainBaseDoesNotBleed(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	base := "bbbbbbbbbb"
	box := roleStyle(RoleFailed).Render("XXX")

	got := compositeOverlay(base, box, 2, 0)
	want := "bb" + box + "bbbbb"
	if got != want {
		t.Errorf("compositeOverlay(...) = %q, want %q", got, want)
	}
}

// ansi.Cut drops a straddled wide rune outright rather than splitting it, so
// compositeOverlay pads the line back out to the base's display width and the
// row stays aligned with the rest of a fixed-width table.
func TestCompositeOverlay_MidWideRuneEdgePadsToBaseWidth(t *testing.T) {
	base := "永永永永永" // 5 runes, 2 columns each = 10 columns
	got := compositeOverlay(base, "X", 3, 0)
	want := "永X永永永 "
	if got != want {
		t.Errorf("compositeOverlay(...) = %q, want %q", got, want)
	}
	if gotWidth := ansi.StringWidth(got); gotWidth != ansi.StringWidth(base) {
		t.Errorf("compositeOverlay(...) width = %d, want %d", gotWidth, ansi.StringWidth(base))
	}
}

// When the right-edge clip lands mid-wide-rune inside the box, the clipped
// box's re-measured width, not the untruncated available width, decides where
// the base's tail cut begins, so the leftover column shows base content
// instead of a blank.
func TestCompositeOverlay_ClippedBoxWideRuneShowsBaseTail(t *testing.T) {
	base := "aaaaa" // 5 columns
	box := "永永"     // 4 columns; clipped to available=3, dropping the second rune
	got := compositeOverlay(base, box, 2, 0)
	want := "aa永a"
	if got != want {
		t.Errorf("compositeOverlay(...) = %q, want %q", got, want)
	}
}

func TestCompositeOverlay_ClipsLeftEdge(t *testing.T) {
	base := "aaaaaaaaaa"
	box := "XXXXX"
	got := compositeOverlay(base, box, -2, 0)
	want := "XXXaaaaaaa"
	if got != want {
		t.Errorf("compositeOverlay(...) = %q, want %q", got, want)
	}
}

func TestCompositeOverlay_ClipsTopEdge(t *testing.T) {
	base := "aaaaaaaaaa\nbbbbbbbbbb"
	box := "XXX\nYYY"
	got := compositeOverlay(base, box, 2, -1)
	want := "aaYYYaaaaa\nbbbbbbbbbb"
	if got != want {
		t.Errorf("compositeOverlay(...) = %q, want %q", got, want)
	}
}

// A box straddling both edges clips both dimensions together, so neither clip
// masks the other.
func TestCompositeOverlay_ClipsTopLeftCorner(t *testing.T) {
	base := "aaaaaaaaaa\nbbbbbbbbbb"
	box := "XXX\nYYY"
	got := compositeOverlay(base, box, -1, -1)
	want := "YYaaaaaaaa\nbbbbbbbbbb"
	if got != want {
		t.Errorf("compositeOverlay(...) = %q, want %q", got, want)
	}
}

// Clipping columns off the left of a styled box must not leak its style into
// the plain base text that follows.
func TestCompositeOverlay_NegativeXStyledBoxDoesNotBleed(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	base := "bbbbbbbbbb"
	box := roleStyle(RoleFailed).Render("XXX")

	got := compositeOverlay(base, box, -1, 0)
	want := "\x1b[31mXX\x1b[0mbbbbbbbb"
	if got != want {
		t.Errorf("compositeOverlay(...) = %q, want %q", got, want)
	}
}

// At the left clip boundary ansi.Cut keeps a straddled rune whole, the
// opposite of the right edge, which drops it
// (TestCompositeOverlay_ClippedBoxWideRuneShowsBaseTail). Either way no rune
// is corrupted and the row still lands at baseWidth.
func TestCompositeOverlay_NegativeXWideRuneKeptWhole(t *testing.T) {
	base := "aaaaa" // 5 columns
	box := "永永"     // 2 runes, 4 columns
	got := compositeOverlay(base, box, -1, 0)
	want := "永永a"
	if got != want {
		t.Errorf("compositeOverlay(...) = %q, want %q", got, want)
	}
	if gotWidth := ansi.StringWidth(got); gotWidth != ansi.StringWidth(base) {
		t.Errorf("compositeOverlay(...) width = %d, want %d", gotWidth, ansi.StringWidth(base))
	}
}

// A box that never reaches column 0 leaves the base row unchanged rather than
// compositing an empty remainder.
func TestCompositeOverlay_EntirelyLeftOfBaseLeavesRowUntouched(t *testing.T) {
	base := "aaaaaaaaaa"
	box := "XXX"
	got := compositeOverlay(base, box, -3, 0)
	if got != base {
		t.Errorf("compositeOverlay(...) = %q, want unchanged %q", got, base)
	}
}

// A zero-width box row leaves the covered base row byte for byte as is.
// Re-cutting and rejoining it would re-emit SGR resets around a row the box
// does not visually change.
func TestCompositeOverlay_EmptyBoxLineLeavesRowUntouched(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	styled := roleStyle(RoleFailed).Render("bbbbbbbbbb")
	got := compositeOverlay(styled, "", 2, 0)
	if got != styled {
		t.Errorf("compositeOverlay(...) = %q, want unchanged %q", got, styled)
	}
}

// Under the max clamp the box takes widthPercent/heightPercent of the
// terminal, so it scales with the terminal instead of shrinking by a fixed
// margin (issue #1844, generalizing
// TestDetailModalBoxSize_MidTerminal_SizedToFraction).
func TestModalBoxSize_MidTerminal_SizedToFraction(t *testing.T) {
	spec := modalBoxSpec{WidthPercent: 80, HeightPercent: 80, MinWidth: 10, MinHeight: 5, MaxWidth: 200, MaxHeight: 100}
	width, height := modalBoxSize(60, 30, spec)
	if width != 48 {
		t.Errorf("modalBoxSize(60, 30, %+v) width = %d, want 48 (80%% of 60)", spec, width)
	}
	if height != 24 {
		t.Errorf("modalBoxSize(60, 30, %+v) height = %d, want 24 (80%% of 30)", spec, height)
	}
}

// This test generalizes TestDetailModalBoxSize_WideTerminal_ClampsToMax to the
// shared sizer (issue #1844).
func TestModalBoxSize_WideTerminal_ClampsToMax(t *testing.T) {
	spec := modalBoxSpec{WidthPercent: 80, HeightPercent: 80, MinWidth: 10, MinHeight: 5, MaxWidth: 100, MaxHeight: 30}
	width, height := modalBoxSize(300, 100, spec)
	if width != 100 {
		t.Errorf("modalBoxSize(300, 100, %+v) width = %d, want 100 (clamped to max)", spec, width)
	}
	if height != 30 {
		t.Errorf("modalBoxSize(300, 100, %+v) height = %d, want 30 (clamped to max)", spec, height)
	}
}

// On a terminal just above the floor the sizer clamps up to minWidth/minHeight
// rather than taking the smaller fraction (issue #1844, generalizing
// TestDetailModalBoxSize_NearFloorTerminal_ClampsToMin).
func TestModalBoxSize_NearFloorTerminal_ClampsToMin(t *testing.T) {
	spec := modalBoxSpec{WidthPercent: 80, HeightPercent: 80, MinWidth: 10, MinHeight: 5, MaxWidth: 100, MaxHeight: 30}
	width, height := modalBoxSize(10, 5, spec)
	if width != 10 {
		t.Errorf("modalBoxSize(10, 5, %+v) width = %d, want 10 (clamped to min)", spec, width)
	}
	if height != 5 {
		t.Errorf("modalBoxSize(10, 5, %+v) height = %d, want 5 (clamped to min)", spec, height)
	}
}

// This test generalizes TestDetailModalFits_BelowMinDimension_ReturnsFalse to
// the shared gate (issue #1844).
func TestModalBoxFits_BelowMinDimension_ReturnsFalse(t *testing.T) {
	cases := []struct {
		name          string
		width, height int
		want          bool
	}{
		{"both at floor", 40, 10, true},
		{"width one short", 39, 10, false},
		{"height one short", 40, 9, false},
		{"plenty of room", 80, 20, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := modalBoxFits(c.width, c.height, 40, 10); got != c.want {
				t.Errorf("modalBoxFits(%d, %d, 40, 10) = %v, want %v", c.width, c.height, got, c.want)
			}
		})
	}
}

// detailModalBoxOrigin now delegates to this shared origin helper (issue
// #1844).
func TestModalBoxOrigin_CentersBoxInTerminal(t *testing.T) {
	x, y := modalBoxOrigin(100, 40, 80, 20)
	if x != 10 {
		t.Errorf("modalBoxOrigin(100, 40, 80, 20) x = %d, want 10", x)
	}
	if y != 10 {
		t.Errorf("modalBoxOrigin(100, 40, 80, 20) y = %d, want 10", y)
	}
}

// This test generalizes detailModalInnerSize's border accounting to the shared
// helper (issue #1844).
func TestModalBoxInnerSize_SubtractsBorder(t *testing.T) {
	width, height := modalBoxInnerSize(80, 20)
	if width != 78 {
		t.Errorf("modalBoxInnerSize(80, 20) width = %d, want 78", width)
	}
	if height != 18 {
		t.Errorf("modalBoxInnerSize(80, 20) height = %d, want 18", height)
	}
}

// A box smaller than its own border must not yield a zero or negative
// interior; the inner size floors at 1 per axis (issue #1844, generalizing
// detailModalInnerSize's own floor).
func TestModalBoxInnerSize_FloorsAtOne(t *testing.T) {
	width, height := modalBoxInnerSize(1, 1)
	if width != 1 {
		t.Errorf("modalBoxInnerSize(1, 1) width = %d, want 1 (floored)", width)
	}
	if height != 1 {
		t.Errorf("modalBoxInnerSize(1, 1) height = %d, want 1 (floored)", height)
	}
}
