package console

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// compositeOverlay draws box on top of base at display-column (x, y): for
// each row box covers, it replaces base's horizontal span [x, x+boxWidth)
// with box's content for that row, leaving the rest of the line — and every
// row box doesn't cover — untouched. This is the missing overlay primitive a
// floating modal needs to render on top of the list instead of replacing it
// (issue #1757); it ships unwired here.
func compositeOverlay(base, box string, x, y int) string {
	baseLines := strings.Split(base, "\n")
	boxLines := strings.Split(box, "\n")

	for i, boxLine := range boxLines {
		row := y + i
		if row < 0 || row >= len(baseLines) {
			continue
		}
		baseLines[row] = compositeLine(baseLines[row], boxLine, x)
	}

	return strings.Join(baseLines, "\n")
}

// compositeLine replaces baseLine's span starting at display column x with
// boxLine, leaving everything outside that span untouched. A negative x
// clips boxLine's leading -x columns instead of dropping the row outright,
// mirroring how a boxLine wider than the remaining space clips its trailing
// columns at the right edge — both edges clip rather than drop the whole
// row. The two directions don't clip identically at a mid-wide-rune
// boundary (see below), just symmetrically in the sense that neither one
// bails out. Cuts are made by display column via ansi.Cut, which steps over
// SGR escapes rather than splitting them and, on a styled line, closes the
// open style at the cut point and reopens it on the far side — so a styled
// baseLine can't bleed its color into or past boxLine without this function
// inserting a reset itself.
//
// A box edge landing mid-wide-rune makes ansi.Cut drop the straddled rune
// outright at the right/far edge rather than split it, which can leave the
// composited line short of baseWidth's column count and the box up to one
// column left of the requested x; the trailing pad below restores the width
// so the row stays aligned with the rest of a fixed-width table (the
// position drift itself is inherent to not splitting a rune in half, not a
// bug this pad hides). At the left/near edge ansi.Cut does the opposite:
// TruncateLeft keeps a straddled rune whole rather than dropping it, so a
// negative x can render the box up to one column right of the requested
// origin instead of short — the same "never split a rune" rule, applied by
// the library in the direction that favors keeping content over the
// direction that favors trimming it.
func compositeLine(baseLine, boxLine string, x int) string {
	baseWidth := ansi.StringWidth(baseLine)
	if x >= baseWidth {
		return baseLine
	}
	boxWidth := ansi.StringWidth(boxLine)
	if boxWidth == 0 {
		return baseLine
	}
	if x < 0 {
		if -x >= boxWidth {
			return baseLine
		}
		boxLine = ansi.Cut(boxLine, -x, boxWidth)
		boxWidth = ansi.StringWidth(boxLine)
		x = 0
	}
	if available := baseWidth - x; boxWidth > available {
		boxLine = ansi.Cut(boxLine, 0, available)
		// Re-measure rather than assume available: a wide rune straddling
		// the clip boundary inside boxLine itself makes ansi.Cut drop it,
		// so the true clipped width can land under available — using the
		// wrong (too-large) width here would blank out base content that
		// should show through the gap instead of showing it.
		boxWidth = ansi.StringWidth(boxLine)
	}

	before := ansi.Cut(baseLine, 0, x)
	after := ansi.Cut(baseLine, x+boxWidth, baseWidth)
	line := before + boxLine + after

	if gap := baseWidth - ansi.StringWidth(line); gap > 0 {
		line += strings.Repeat(" ", gap)
	}
	return line
}

// modalBoxSpec holds a floating modal box's target geometry — the
// width/height-percent-then-min/max-clamp fields modalBoxSize consumes.
// Passing this as a single named-field struct instead of six positional
// ints closes the transposition foot-gun a growing list of same-typed
// int params invites at the call site (issue #1858).
type modalBoxSpec struct {
	WidthPercent, HeightPercent int
	MinWidth, MinHeight         int
	MaxWidth, MaxHeight         int
}

// modalBoxSize returns a floating modal box's outer width and height for a
// termWidth x termHeight terminal: spec.WidthPercent/HeightPercent of the
// terminal's own dimensions, clamped down to spec.MinWidth/MinHeight and up
// to spec.MaxWidth/MaxHeight — the modal-agnostic geometry a floating box's
// own sizing (e.g. the detail modal's detailModalBoxSize) delegates to
// (issue #1844).
func modalBoxSize(termWidth, termHeight int, spec modalBoxSpec) (width, height int) {
	width = termWidth * spec.WidthPercent / 100
	if width < spec.MinWidth {
		width = spec.MinWidth
	}
	if width > spec.MaxWidth {
		width = spec.MaxWidth
	}
	height = termHeight * spec.HeightPercent / 100
	if height < spec.MinHeight {
		height = spec.MinHeight
	}
	if height > spec.MaxHeight {
		height = spec.MaxHeight
	}
	return width, height
}

// modalBoxFits reports whether a termWidth x termHeight terminal leaves room
// for a floating modal box at least minWidth x minHeight — the
// modal-agnostic gate a floating box's own fits predicate (e.g. the detail
// modal's detailModalFits) delegates to (issue #1844).
func modalBoxFits(termWidth, termHeight, minWidth, minHeight int) bool {
	return termWidth >= minWidth && termHeight >= minHeight
}

// modalBoxOrigin centers a boxWidth x boxHeight box within a termWidth x
// termHeight terminal, the (x, y) compositeOverlay places it at — the
// modal-agnostic centering a floating box's own origin (e.g. the detail
// modal's detailModalBoxOrigin) delegates to (issue #1844).
func modalBoxOrigin(termWidth, termHeight, boxWidth, boxHeight int) (x, y int) {
	return (termWidth - boxWidth) / 2, (termHeight - boxHeight) / 2
}

// modalBoxInnerSize returns a boxWidth x boxHeight modal box's interior
// width/height once its boxBorderCols/boxBorderRows border is subtracted —
// deliberately reusing the same rounded-border constants every other
// bordered panel in this package pays, not a modal-specific border width —
// floored at 1x1 so a box smaller than its own border never yields a
// non-positive interior. The modal-agnostic interior sizing a floating
// box's own inner size (e.g. the detail modal's detailModalInnerSize)
// delegates to (issue #1844).
func modalBoxInnerSize(boxWidth, boxHeight int) (width, height int) {
	width, height = boxWidth-boxBorderCols, boxHeight-boxBorderRows
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	return width, height
}
