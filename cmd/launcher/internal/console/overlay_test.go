package console

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

// TestCompositeOverlay_InteriorPosition verifies a plain-text overlay box
// replaces only the horizontal span it covers on the rows it covers, leaving
// the rest of each base line — and every uncovered line — intact.
func TestCompositeOverlay_InteriorPosition(t *testing.T) {
	base := "aaaaaaaaaa\nbbbbbbbbbb\ncccccccccc"
	box := "XXX"
	got := compositeOverlay(base, box, 2, 1)
	want := "aaaaaaaaaa\nbbXXXbbbbb\ncccccccccc"
	if got != want {
		t.Errorf("compositeOverlay(...) = %q, want %q", got, want)
	}
}

// TestCompositeOverlay_TopLeftCorner verifies a box positioned at (0, 0)
// replaces the leading span of the first line only, with no off-by-one
// against the base's origin.
func TestCompositeOverlay_TopLeftCorner(t *testing.T) {
	base := "aaaaaaaaaa\nbbbbbbbbbb"
	box := "XX"
	got := compositeOverlay(base, box, 0, 0)
	want := "XXaaaaaaaa\nbbbbbbbbbb"
	if got != want {
		t.Errorf("compositeOverlay(...) = %q, want %q", got, want)
	}
}

// TestCompositeOverlay_ANSIStyledBaseRow verifies a base row carrying ANSI
// SGR escapes (as roleStyle produces) is sliced by display column, not byte
// offset: the box lands on the right visible cells, no escape sequence is
// split, the base's style is closed before the box so it doesn't bleed into
// it, and the base's style reopens on the far side so it doesn't vanish from
// the untouched trailing text either.
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

// TestCompositeOverlay_ClipsRightEdge verifies a box positioned so it would
// extend past the base's right edge is clipped to the base's width instead
// of widening the output line.
func TestCompositeOverlay_ClipsRightEdge(t *testing.T) {
	base := "aaaaaaaaaa"
	box := "XXXXX"
	got := compositeOverlay(base, box, 8, 0)
	want := "aaaaaaaaXX"
	if got != want {
		t.Errorf("compositeOverlay(...) = %q, want %q", got, want)
	}
}

// TestCompositeOverlay_ClipsBottomEdge verifies a box positioned so it would
// extend past the base's last row is clipped to the base's row count instead
// of appending extra lines.
func TestCompositeOverlay_ClipsBottomEdge(t *testing.T) {
	base := "aaaaaaaaaa\nbbbbbbbbbb"
	box := "XXX\nYYY\nZZZ"
	got := compositeOverlay(base, box, 2, 1)
	want := "aaaaaaaaaa\nbbXXXbbbbb"
	if got != want {
		t.Errorf("compositeOverlay(...) = %q, want %q", got, want)
	}
}

// TestCompositeOverlay_WideRunesNoOffByOneDrift verifies a base row made of
// 2-column CJK runes is cut at the right display column, not the right rune
// index — measuring by rune count instead of display width would drift the
// cut position for every wide rune before it.
func TestCompositeOverlay_WideRunesNoOffByOneDrift(t *testing.T) {
	base := "永永永永永"
	box := "XX"
	got := compositeOverlay(base, box, 4, 0)
	want := "永永XX永永"
	if got != want {
		t.Errorf("compositeOverlay(...) = %q, want %q", got, want)
	}
}

// TestCompositeOverlay_StyledBoxOverPlainBaseDoesNotBleed verifies a styled
// overlay box composited onto a plain base doesn't leak its color into the
// untouched base text after it — the box's own render is self-closing, so
// splicing it into plain, untouched before/after spans is enough on its own.
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

// TestCompositeOverlay_MidWideRuneEdgePadsToBaseWidth verifies a box edge
// landing mid-wide-rune — where ansi.Cut drops the straddled rune outright
// rather than split it — doesn't shrink the composited line: it's padded
// back out to the base's display width so the row stays aligned with the
// rest of a fixed-width table.
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

// TestCompositeOverlay_ClippedBoxWideRuneShowsBaseTail verifies that when
// clipping a box at the right edge lands mid-wide-rune inside the box
// itself, the leftover column shows the base's own trailing content rather
// than getting blanked out — the clipped box's true (re-measured) width,
// not the untruncated available width, decides where the base's tail cut
// begins.
func TestCompositeOverlay_ClippedBoxWideRuneShowsBaseTail(t *testing.T) {
	base := "aaaaa" // 5 columns
	box := "永永"     // 4 columns; clipped to available=3, dropping the second rune
	got := compositeOverlay(base, box, 2, 0)
	want := "aa永a"
	if got != want {
		t.Errorf("compositeOverlay(...) = %q, want %q", got, want)
	}
}

// TestCompositeOverlay_ClipsLeftEdge verifies a box positioned so it starts
// before the base's left edge is clipped to the base's origin instead of
// dropping the entire row, mirroring how a box overflowing the right edge
// clips rather than disappearing.
func TestCompositeOverlay_ClipsLeftEdge(t *testing.T) {
	base := "aaaaaaaaaa"
	box := "XXXXX"
	got := compositeOverlay(base, box, -2, 0)
	want := "XXXaaaaaaa"
	if got != want {
		t.Errorf("compositeOverlay(...) = %q, want %q", got, want)
	}
}

// TestCompositeOverlay_ClipsTopEdge verifies a box positioned so it starts
// above the base's top row drops only the box rows that land above row 0,
// compositing the rest normally.
func TestCompositeOverlay_ClipsTopEdge(t *testing.T) {
	base := "aaaaaaaaaa\nbbbbbbbbbb"
	box := "XXX\nYYY"
	got := compositeOverlay(base, box, 2, -1)
	want := "aaYYYaaaaa\nbbbbbbbbbb"
	if got != want {
		t.Errorf("compositeOverlay(...) = %q, want %q", got, want)
	}
}

// TestCompositeOverlay_ClipsTopLeftCorner verifies a box straddling both the
// left and top edges at once clips both dimensions together instead of one
// masking the other.
func TestCompositeOverlay_ClipsTopLeftCorner(t *testing.T) {
	base := "aaaaaaaaaa\nbbbbbbbbbb"
	box := "XXX\nYYY"
	got := compositeOverlay(base, box, -1, -1)
	want := "YYaaaaaaaa\nbbbbbbbbbb"
	if got != want {
		t.Errorf("compositeOverlay(...) = %q, want %q", got, want)
	}
}

// TestCompositeOverlay_NegativeXStyledBoxDoesNotBleed verifies a styled
// overlay box clipped at the left edge stays self-contained: the columns
// dropped off its left side don't leak style into the plain base text that
// follows it, mirroring StyledBoxOverPlainBaseDoesNotBleed for the near edge.
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

// TestCompositeOverlay_NegativeXWideRuneKeptWhole verifies that when the
// left clip boundary lands mid-wide-rune, ansi.Cut keeps that rune whole
// rather than splitting it — the opposite of the right edge, where a
// straddled rune is dropped outright (TestCompositeOverlay_ClippedBoxWideRuneShowsBaseTail).
// Either way no rune is corrupted, and the row still lands at baseWidth.
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

// TestCompositeOverlay_EntirelyLeftOfBaseLeavesRowUntouched verifies a box
// positioned so far left that none of it reaches column 0 leaves the base
// row unchanged, rather than compositing an empty remainder.
func TestCompositeOverlay_EntirelyLeftOfBaseLeavesRowUntouched(t *testing.T) {
	base := "aaaaaaaaaa"
	box := "XXX"
	got := compositeOverlay(base, box, -3, 0)
	if got != base {
		t.Errorf("compositeOverlay(...) = %q, want unchanged %q", got, base)
	}
}

// TestCompositeOverlay_EmptyBoxLineLeavesRowUntouched verifies a
// zero-width box row (e.g. a blank line inside a multi-line box) leaves the
// covered base row byte-for-byte as-is, rather than re-cutting and
// rejoining it — which would needlessly re-emit SGR resets around a row the
// box visually doesn't change.
func TestCompositeOverlay_EmptyBoxLineLeavesRowUntouched(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	styled := roleStyle(RoleFailed).Render("bbbbbbbbbb")
	got := compositeOverlay(styled, "", 2, 0)
	if got != styled {
		t.Errorf("compositeOverlay(...) = %q, want unchanged %q", got, styled)
	}
}

// TestModalBoxSize_MidTerminal_SizedToFraction verifies the generic modal
// box sizer scales with the terminal rather than shrinking by a fixed
// margin: on a terminal well under the max clamp, the box width/height are
// widthPercent/heightPercent of the terminal's own dimensions (issue #1844,
// generalizing detailModalBoxSize's own TestDetailModalBoxSize_MidTerminal_
// SizedToFraction).
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

// TestModalBoxSize_WideTerminal_ClampsToMax verifies the generic modal box
// sizer never grows past maxWidth/maxHeight, whatever the terminal's own
// size (issue #1844, generalizing detailModalBoxSize's own
// TestDetailModalBoxSize_WideTerminal_ClampsToMax).
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

// TestModalBoxSize_NearFloorTerminal_ClampsToMin verifies the generic modal
// box sizer clamps up to minWidth/minHeight rather than the smaller
// fraction on a terminal just above that floor (issue #1844, generalizing
// detailModalBoxSize's own
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

// TestModalBoxFits_BelowMinDimension_ReturnsFalse verifies the generic
// modal-fits gate rejects a terminal narrower or shorter than minWidth/
// minHeight, and accepts one that meets both floors (issue #1844,
// generalizing detailModalFits' own
// TestDetailModalFits_BelowMinDimension_ReturnsFalse).
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

// TestModalBoxOrigin_CentersBoxInTerminal verifies the generic modal box
// origin centers a boxWidth x boxHeight box within a termWidth x termHeight
// terminal (issue #1844, generalizing detailModalBoxOrigin, which now
// delegates here).
func TestModalBoxOrigin_CentersBoxInTerminal(t *testing.T) {
	x, y := modalBoxOrigin(100, 40, 80, 20)
	if x != 10 {
		t.Errorf("modalBoxOrigin(100, 40, 80, 20) x = %d, want 10", x)
	}
	if y != 10 {
		t.Errorf("modalBoxOrigin(100, 40, 80, 20) y = %d, want 10", y)
	}
}

// TestModalBoxInnerSize_SubtractsBorder verifies the generic modal box
// inner-size helper returns the box's interior width/height once its
// boxBorderCols/boxBorderRows border is subtracted (issue #1844,
// generalizing detailModalInnerSize's own border accounting).
func TestModalBoxInnerSize_SubtractsBorder(t *testing.T) {
	width, height := modalBoxInnerSize(80, 20)
	if width != 78 {
		t.Errorf("modalBoxInnerSize(80, 20) width = %d, want 78", width)
	}
	if height != 18 {
		t.Errorf("modalBoxInnerSize(80, 20) height = %d, want 18", height)
	}
}

// TestModalBoxInnerSize_FloorsAtOne verifies a box smaller than its own
// border never yields a non-positive interior — the inner size floors at 1
// on each axis instead of going to zero or negative (issue #1844,
// generalizing detailModalInnerSize's own floor).
func TestModalBoxInnerSize_FloorsAtOne(t *testing.T) {
	width, height := modalBoxInnerSize(1, 1)
	if width != 1 {
		t.Errorf("modalBoxInnerSize(1, 1) width = %d, want 1 (floored)", width)
	}
	if height != 1 {
		t.Errorf("modalBoxInnerSize(1, 1) height = %d, want 1 (floored)", height)
	}
}
