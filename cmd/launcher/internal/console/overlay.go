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
// boxLine, leaving everything before x untouched. Cuts are made by display
// column via ansi.Cut, which steps over SGR escapes rather than splitting
// them and, on a styled line, closes the open style at the cut point and
// reopens it on the far side — so a styled baseLine can't bleed its color
// into or past boxLine without this function inserting a reset itself.
//
// A box edge landing mid-wide-rune makes ansi.Cut drop the straddled rune
// outright rather than split it, which can leave the composited line short
// of baseWidth's column count and the box up to one column left of the
// requested x; the trailing pad below restores the width so the row stays
// aligned with the rest of a fixed-width table (the position drift itself
// is inherent to not splitting a rune in half, not a bug this pad hides).
func compositeLine(baseLine, boxLine string, x int) string {
	baseWidth := ansi.StringWidth(baseLine)
	if x < 0 || x >= baseWidth {
		return baseLine
	}
	boxWidth := ansi.StringWidth(boxLine)
	if boxWidth == 0 {
		return baseLine
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
