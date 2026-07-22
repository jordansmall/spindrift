package console

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// dimBase renders base's content in RoleDim's foreground, one line at a time
// — the scrim behind a floating detail modal (issue #1760): a receded
// background reads more clearly as "behind" the box than one still carrying
// its normal per-row coloring. Each line is stripped of its existing SGR
// escapes before RoleDim's style is applied, rather than nesting the dim
// style inside whatever was already there — nesting would leave the old
// style's own reset sequence in the middle of the line, which cuts the dim
// style short right where the old escape closes (the same split-sequence
// failure compositeLine's ansi.Cut usage exists to avoid). Stripping first
// means the only escapes in the result are the ones this function itself
// wrote, so there is nothing left to split. Under NO_COLOR or a non-color
// TERM, roleStyle degrades to a no-op (ADR 0031), so the scrim itself
// disappears there too — an acceptable trade given issue #1760 spells this
// out as droppable polish in the first place.
func dimBase(base string) string {
	lines := strings.Split(base, "\n")
	dim := roleStyle(RoleDim)
	for i, line := range lines {
		lines[i] = dim.Render(ansi.Strip(line))
	}
	return strings.Join(lines, "\n")
}
