package console

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// compositeOverlay draws box on top of base at display-column (x, y): for each
// row box covers, it replaces base's horizontal span [x, x+boxWidth) with box's
// content for that row, leaving the rest of the line — and every row box
// doesn't cover — untouched.
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
// boxLine, leaving everything outside that span untouched. A negative x clips
// boxLine's leading -x columns rather than dropping the row, mirroring how an
// over-wide boxLine clips its trailing columns — neither edge bails out. Cuts
// go by display column via ansi.Cut, which steps over SGR escapes rather than
// splitting them and closes an open style at the cut point and reopens it on
// the far side, so a styled baseLine can't bleed its color past boxLine.
//
// The two edges do not clip identically at a mid-wide-rune boundary. The right
// edge drops the straddled rune outright, which can leave the line short of
// baseWidth and the box a column left of the requested x -- the trailing pad
// below restores the width so the row stays aligned with a fixed-width table,
// though the position drift itself is inherent to not splitting a rune, not
// something the pad hides. At the left edge TruncateLeft keeps the straddled
// rune whole instead, so a negative x can render the box a column right of the
// requested origin.
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
		// Re-measure rather than assume available: a wide rune straddling the
		// clip boundary inside boxLine makes ansi.Cut drop it, so the true
		// clipped width can land under available -- using the too-large width
		// would blank out base content that should show through the gap.
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

// modalBoxSpec holds a floating modal box's target geometry. Passed as a
// single named-field struct rather than six positional ints to close the
// transposition foot-gun a list of same-typed int params invites.
type modalBoxSpec struct {
	WidthPercent, HeightPercent int
	MinWidth, MinHeight         int
	MaxWidth, MaxHeight         int
}

// modalBoxSize returns a floating modal box's outer width and height for a
// termWidth x termHeight terminal: spec.WidthPercent/HeightPercent of the
// terminal's own dimensions, clamped to spec's Min/Max — the modal-agnostic
// geometry each box's own sizing delegates to.
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
// for a floating modal box at least minWidth x minHeight — the modal-agnostic
// gate each box's own fits predicate delegates to.
func modalBoxFits(termWidth, termHeight, minWidth, minHeight int) bool {
	return termWidth >= minWidth && termHeight >= minHeight
}

// modalBoxOrigin centers a boxWidth x boxHeight box within a termWidth x
// termHeight terminal: the (x, y) compositeOverlay places it at.
func modalBoxOrigin(termWidth, termHeight, boxWidth, boxHeight int) (x, y int) {
	return (termWidth - boxWidth) / 2, (termHeight - boxHeight) / 2
}

// modalBoxInnerSize returns a boxWidth x boxHeight modal box's interior
// width/height once its boxBorderCols/boxBorderRows border is subtracted --
// deliberately the same rounded-border constants every other bordered panel in
// this package pays, not a modal-specific width -- floored at 1x1 so a box
// smaller than its own border never yields a non-positive interior.
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
