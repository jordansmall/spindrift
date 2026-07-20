package console

import (
	"io"
	"os"
	"sync"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// Role names a semantic element the Console styles by meaning — never by a
// hardcoded hex value — so the terminal (and Stylix, transitively) supplies
// the actual color (ADR 0031).
type Role int

const (
	RoleRunning Role = iota
	RoleHeld
	RoleSettled
	RoleFailed
	RoleAccent
	RoleDim
)

// ansiSlot maps a Role to one of the 16 standard ANSI palette slots. This is
// the palette-resolver seam ADR 0031 reserves for a future explicit base16
// override: swapping this function's body to consult a base16 hex table
// needs no call-site change, since callers only ever ask for a Role's style.
func ansiSlot(r Role) int {
	switch r {
	case RoleRunning:
		return 4 // blue
	case RoleHeld:
		return 3 // yellow
	case RoleSettled:
		return 2 // green
	case RoleFailed:
		return 1 // red
	case RoleAccent:
		return 5 // magenta
	case RoleDim:
		return 8 // bright black
	default:
		return 7 // white
	}
}

// colorProfile reports the ANSI color profile the header should render
// against: Ascii (plain text) when NO_COLOR is set or the terminal is a
// known non-color terminal (TERM unset or "dumb"), ANSI otherwise. It is
// computed from the environment directly rather than through termenv's own
// isatty-gated detection, so it degrades correctly under NO_COLOR and dumb
// terminals alike without requiring a real TTY.
func colorProfile() termenv.Profile {
	if os.Getenv("NO_COLOR") != "" {
		return termenv.Ascii
	}
	if term := os.Getenv("TERM"); term == "" || term == "dumb" {
		return termenv.Ascii
	}
	return termenv.ANSI
}

// Plain-Unicode glyphs (no nerd-fonts) tagging the header's alert lines by
// kind, paired with role coloring (ADR 0031): glyphWarning marks a condition
// that needs attention (stale image, a failure), glyphRebuilding marks work
// in progress, and glyphNotice marks an informational notice.
const (
	glyphWarning    = "⚠"
	glyphRebuilding = "↻"
	glyphNotice     = "ℹ"
)

// researchMarker tags a research-kind pick's row in the work Sections
// (renderWorkSection) — the visible distinction issue #1710 asks for between
// a research pick and a work pick, which carries no marker at all. Left
// unstyled, like the rest of a row's extras (held-by badge, reason,
// heartbeat): renderWorkSection measures and clips extras as one plain
// string (the same clip-before-style discipline renderSectionTabs documents)
// before any styling would apply, so a Role-styled marker mixed into it
// would have its ANSI escape bytes miscounted as display columns on a color
// terminal.
const researchMarker = "[research]"

// renderers caches one lipgloss.Renderer per termenv.Profile, so a header
// with several styled segments (the status line alone styles five) doesn't
// allocate and re-detect a renderer per segment per frame. Keyed by profile
// rather than built once: colorProfile() can change value across a test run
// (t.Setenv) and, in principle, across a NO_COLOR toggle mid-process, so a
// single cached instance would go stale where a small per-profile cache
// does not. The writer passed to NewRenderer is never used for output —
// SetColorProfile forces the profile from the environment, not from probing
// an actual terminal — so io.Discard documents that plainly.
var renderers sync.Map // termenv.Profile -> *lipgloss.Renderer

func rendererFor(p termenv.Profile) *lipgloss.Renderer {
	if r, ok := renderers.Load(p); ok {
		return r.(*lipgloss.Renderer)
	}
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(p)
	actual, _ := renderers.LoadOrStore(p, r)
	return actual.(*lipgloss.Renderer)
}

// roleStyle returns the lipgloss style for a semantic Role, resolved against
// the current color profile so it renders styled by role on a color-capable
// terminal and degrades to plain text under NO_COLOR or a non-color
// terminal (ADR 0031).
func roleStyle(r Role) lipgloss.Style {
	return rendererFor(colorProfile()).NewStyle().Foreground(lipgloss.ANSIColor(ansiSlot(r)))
}
