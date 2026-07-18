package console

import (
	"os"

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

// roleStyle returns the lipgloss style for a semantic Role, resolved against
// the current color profile so it renders styled by role on a color-capable
// terminal and degrades to plain text under NO_COLOR or a non-color
// terminal (ADR 0031).
func roleStyle(r Role) lipgloss.Style {
	renderer := lipgloss.NewRenderer(os.Stdout)
	renderer.SetColorProfile(colorProfile())
	return renderer.NewStyle().Foreground(lipgloss.ANSIColor(ansiSlot(r)))
}
