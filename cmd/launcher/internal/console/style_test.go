package console

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
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

// TestRenderHeaderWith_PlainText_MatchesStyledStripped verifies plainText is
// a faithful unstyled twin of styledText rather than a second, independently
// drifting implementation: renderHeaderWith(m, plainText) must equal
// renderHeader(m) (== renderHeaderWith(m, styledText)) with every ANSI escape
// stripped. The model sets every alert line renderHeader can emit so each
// roleStyle call site in the function is exercised.
func TestRenderHeaderWith_PlainText_MatchesStyledStripped(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	m := Update(NewModel(), StaleStatusMsg{RebuildStatus: RebuildStatus{
		Stale:              true,
		Message:            "rebuild needed",
		Rebuilding:         true,
		Err:                "nix build failed",
		BranchSwitchNotice: "switched off-branch tree from feature to main",
		StaleDrainSummary:  "==> drained 3 stale entries",
	}})
	m.OrphanRecoveryErr = "failed to adopt orphan #42: boom"
	m.DogfoodLive = true

	styled := renderHeader(m)
	plain := renderHeaderWith(m, plainText)

	if want := ansi.Strip(styled); plain != want {
		t.Errorf("renderHeaderWith(m, plainText) = %q, want ansi.Strip(renderHeader(m)) = %q", plain, want)
	}
}

// TestRenderHeaderWith_PlainText_EmitsNoEscapes verifies plainText never
// reaches roleStyle/colorProfile/rendererFor: even with TERM set to a
// color-capable value (so the styled path really would emit escapes), the
// plain output carries no ESC byte at all.
func TestRenderHeaderWith_PlainText_EmitsNoEscapes(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	m := Update(NewModel(), StaleStatusMsg{RebuildStatus: RebuildStatus{
		Stale:      true,
		Message:    "rebuild needed",
		Rebuilding: true,
		Err:        "nix build failed",
	}})

	plain := renderHeaderWith(m, plainText)
	if strings.Contains(plain, "\x1b") {
		t.Errorf("renderHeaderWith(m, plainText) = %q, want no ESC byte", plain)
	}
}

// TestAnsiSlot_RoleRecoverable_ResolvesToCyanDistinctFromHeld verifies
// RoleRecoverable resolves to ANSI slot 6 (cyan), distinct from RoleHeld's
// slot 3 (yellow) — the previously-unused cyan slot ADR 0031 reserves for a
// recoverable-state role.
func TestAnsiSlot_RoleRecoverable_ResolvesToCyanDistinctFromHeld(t *testing.T) {
	if got := ansiSlot(RoleRecoverable); got != 6 {
		t.Errorf("ansiSlot(RoleRecoverable) = %d, want 6 (cyan)", got)
	}
	if ansiSlot(RoleRecoverable) == ansiSlot(RoleHeld) {
		t.Errorf("ansiSlot(RoleRecoverable) = %d, want it distinct from ansiSlot(RoleHeld) = %d", ansiSlot(RoleRecoverable), ansiSlot(RoleHeld))
	}
}
