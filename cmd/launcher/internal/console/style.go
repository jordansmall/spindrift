package console

import (
	"io"
	"os"
	"sync"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// Role names a semantic element the Console styles by meaning, never by a
// hardcoded hex value, so the terminal supplies the actual color (ADR 0031).
type Role int

const (
	RoleRunning Role = iota
	RoleHeld
	RoleRecoverable
	RoleSettled
	RoleFailed
	RoleAccent
	RoleDim
)

// ansiSlot maps a Role to one of the 16 standard ANSI palette slots. ADR 0031
// reserves it as the palette-resolver seam: callers only ever ask for a Role's
// style, so a base16 hex table can replace this body without touching them.
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
	case RoleRecoverable:
		return 6 // cyan
	default:
		return 7 // white
	}
}

// colorProfile reads the environment directly rather than using termenv's
// isatty-gated detection, so it degrades to Ascii under NO_COLOR and dumb
// terminals without requiring a real TTY.
func colorProfile() termenv.Profile {
	if os.Getenv("NO_COLOR") != "" {
		return termenv.Ascii
	}
	if term := os.Getenv("TERM"); term == "" || term == "dumb" {
		return termenv.Ascii
	}
	return termenv.ANSI
}

// Glyphs tagging the header's alert lines by kind, paired with role coloring
// (ADR 0031). Plain Unicode only: the terminal may have no nerd font.
const (
	glyphWarning    = "⚠"
	glyphRebuilding = "↻"
	glyphNotice     = "ℹ"
)

// researchMarker distinguishes a research pick's row from a work pick's, which
// carries no marker (issue #1710). It stays unstyled like the rest of a row's
// extras: renderWorkSection measures and clips them as one plain string, so a
// Role-styled marker's ANSI escape bytes would count as display columns.
const researchMarker = "[research]"

// renderers caches one lipgloss.Renderer per termenv.Profile, so a header
// doesn't re-detect a renderer per styled segment per frame. Keyed by profile
// rather than built once because colorProfile() changes value when a test sets
// NO_COLOR or TERM (t.Setenv), which would leave a single instance stale.
var renderers sync.Map // termenv.Profile -> *lipgloss.Renderer

func rendererFor(p termenv.Profile) *lipgloss.Renderer {
	if r, ok := renderers.Load(p); ok {
		return r.(*lipgloss.Renderer)
	}
	// SetColorProfile pins the profile, so lipgloss never probes a terminal and
	// the renderer never writes to this writer.
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(p)
	actual, _ := renderers.LoadOrStore(p, r)
	return actual.(*lipgloss.Renderer)
}

// roleStyle resolves a Role against the current color profile, degrading to
// plain text under NO_COLOR or a non-color terminal (ADR 0031).
func roleStyle(r Role) lipgloss.Style {
	return rendererFor(colorProfile()).NewStyle().Foreground(lipgloss.ANSIColor(ansiSlot(r)))
}

// styleFunc lets renderHeaderWith run against either a real terminal renderer
// (styledText) or a pure counter that must never touch colorProfile or
// rendererFor (plainText, issue #3019), so a plain and a styled renderer
// cannot drift apart.
type styleFunc func(Role, string) string

func styledText(r Role, s string) string {
	return roleStyle(r).Render(s)
}

// plainText calls neither roleStyle, colorProfile, nor rendererFor, so a
// renderer driven through it is pure (issue #3019). The wrap algorithms treat
// ANSI escapes as zero-width, so plain and styled text wrap to the same number
// of lines.
func plainText(_ Role, s string) string {
	return s
}
