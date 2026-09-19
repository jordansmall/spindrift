package console

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// dimBase renders base in RoleDim as the scrim behind a floating detail modal
// (issue #1760). It strips each line's existing SGR escapes first: nesting the
// dim style inside them would leave the old style's reset mid-line, cutting the
// dim short there. Under NO_COLOR or a non-color TERM roleStyle is a no-op
// (ADR 0031), so the scrim goes too, which #1760 accepts as droppable polish.
func dimBase(base string) string {
	lines := strings.Split(base, "\n")
	dim := roleStyle(RoleDim)
	for i, line := range lines {
		lines[i] = dim.Render(ansi.Strip(line))
	}
	return strings.Join(lines, "\n")
}
