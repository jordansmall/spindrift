package console

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// compositeOverlay draws box on top of base at display column (x, y),
// replacing base's span [x, x+boxWidth) on each row box covers and leaving
// every other row and column untouched. No caller wires it up yet (issue
// #1757).
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
// boxLine, leaving everything outside that span untouched. Both edges clip
// rather than drop the row: a negative x cuts boxLine's leading -x columns.
// ansi.Cut steps over SGR escapes and closes then reopens the open style at
// the cut, so a styled baseLine cannot bleed color into or past boxLine.
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
		// TruncateLeft keeps a straddled wide rune whole instead of dropping
		// it, so a negative x can place the box one column right of x.
		boxLine = ansi.Cut(boxLine, -x, boxWidth)
		boxWidth = ansi.StringWidth(boxLine)
		x = 0
	}
	if available := baseWidth - x; boxWidth > available {
		boxLine = ansi.Cut(boxLine, 0, available)
		// Re-measure rather than assume available: a wide rune straddling the
		// clip boundary is dropped, so the clipped width can land under
		// available, and the too-large value would blank out base content that
		// should show through the gap.
		boxWidth = ansi.StringWidth(boxLine)
	}

	before := ansi.Cut(baseLine, 0, x)
	after := ansi.Cut(baseLine, x+boxWidth, baseWidth)
	line := before + boxLine + after

	// A box edge landing mid-wide-rune makes ansi.Cut drop the straddled rune
	// rather than split it, leaving the line short of baseWidth. Pad it back so
	// the row stays aligned with the rest of a fixed-width table.
	if gap := baseWidth - ansi.StringWidth(line); gap > 0 {
		line += strings.Repeat(" ", gap)
	}
	return line
}

// modalBoxSpec holds a floating modal box's target geometry. A named-field
// struct keeps callers from transposing what would otherwise be six positional
// int arguments (issue #1858).
type modalBoxSpec struct {
	WidthPercent, HeightPercent int
	MinWidth, MinHeight         int
	MaxWidth, MaxHeight         int
}

// modalBoxSize returns a modal box's outer width and height: spec's
// percentages of the terminal, clamped by spec's min and max (issue #1844).
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

// modalBoxFits reports whether the terminal leaves room for a modal box of at
// least minWidth x minHeight (issue #1844).
func modalBoxFits(termWidth, termHeight, minWidth, minHeight int) bool {
	return termWidth >= minWidth && termHeight >= minHeight
}

// modalBoxOrigin centers a boxWidth x boxHeight box in the terminal and
// returns the (x, y) compositeOverlay places it at (issue #1844).
func modalBoxOrigin(termWidth, termHeight, boxWidth, boxHeight int) (x, y int) {
	return (termWidth - boxWidth) / 2, (termHeight - boxHeight) / 2
}

// modalBoxInnerSize returns a modal box's interior after subtracting
// boxBorderCols/boxBorderRows, the same border every other bordered panel in
// this package uses. It floors the result at 1x1 so a box smaller than its own
// border never yields a non-positive interior (issue #1844).
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
