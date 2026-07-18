package console

import (
	"strings"
	"testing"
)

// TestRoleStyle_Render_AppliesColorByDefault verifies roleStyle renders text
// wrapped in an ANSI color escape sequence on a color-capable terminal — the
// palette-resolver seam ADR 0031 requires, keyed off a semantic Role rather
// than a hardcoded hex value.
func TestRoleStyle_Render_AppliesColorByDefault(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	out := roleStyle(RoleFailed).Render("failed 1")
	if !strings.Contains(out, "failed 1") {
		t.Errorf("roleStyle(RoleFailed).Render(...) = %q, want it to contain %q", out, "failed 1")
	}
	if !strings.Contains(out, "\x1b[") {
		t.Errorf("roleStyle(RoleFailed).Render(...) = %q, want an ANSI escape sequence", out)
	}
}

// TestRoleStyle_Render_PlainUnderNoColor verifies roleStyle degrades to
// readable plain text — no ANSI escape sequences at all — when NO_COLOR is
// set (ADR 0031, issue #1499 AC).
func TestRoleStyle_Render_PlainUnderNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "xterm-256color")

	out := roleStyle(RoleFailed).Render("failed 1")
	if out != "failed 1" {
		t.Errorf("roleStyle(RoleFailed).Render(...) under NO_COLOR = %q, want plain %q", out, "failed 1")
	}
}

// TestRoleStyle_Render_PlainOnDumbTerminal verifies roleStyle degrades to
// plain text on a non-color terminal (TERM=dumb), the other half of the AC's
// "NO_COLOR or a non-color terminal" degradation requirement.
func TestRoleStyle_Render_PlainOnDumbTerminal(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "dumb")

	out := roleStyle(RoleFailed).Render("failed 1")
	if out != "failed 1" {
		t.Errorf("roleStyle(RoleFailed).Render(...) on TERM=dumb = %q, want plain %q", out, "failed 1")
	}
}
