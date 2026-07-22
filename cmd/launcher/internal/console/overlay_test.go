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
