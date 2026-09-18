package console

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// ADR 0031 requires a palette resolver that keys color off a semantic Role
// rather than a hardcoded hex value.
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

// NO_COLOR must leave no escape sequence at all (ADR 0031, issue #1499).
func TestRoleStyle_Render_PlainUnderNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "xterm-256color")

	out := roleStyle(RoleFailed).Render("failed 1")
	if out != "failed 1" {
		t.Errorf("roleStyle(RoleFailed).Render(...) under NO_COLOR = %q, want plain %q", out, "failed 1")
	}
}

// TERM=dumb covers the second half of the degradation issue #1499 requires,
// alongside NO_COLOR.
func TestRoleStyle_Render_PlainOnDumbTerminal(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "dumb")

	out := roleStyle(RoleFailed).Render("failed 1")
	if out != "failed 1" {
		t.Errorf("roleStyle(RoleFailed).Render(...) on TERM=dumb = %q, want plain %q", out, "failed 1")
	}
}

// plainText must stay an unstyled twin of styledText, not a second
// implementation that drifts from it. The model sets every alert line
// renderHeader can emit, so each roleStyle call site runs.
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

// TERM is color-capable here so the styled path really would emit escapes.
// plainText must still never reach roleStyle, colorProfile, or rendererFor.
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

// ADR 0031 reserves the previously unused cyan slot 6 for the recoverable role,
// so it must not collide with RoleHeld's yellow slot 3.
func TestAnsiSlot_RoleRecoverable_ResolvesToCyanDistinctFromHeld(t *testing.T) {
	if got := ansiSlot(RoleRecoverable); got != 6 {
		t.Errorf("ansiSlot(RoleRecoverable) = %d, want 6 (cyan)", got)
	}
	if ansiSlot(RoleRecoverable) == ansiSlot(RoleHeld) {
		t.Errorf("ansiSlot(RoleRecoverable) = %d, want it distinct from ansiSlot(RoleHeld) = %d", ansiSlot(RoleRecoverable), ansiSlot(RoleHeld))
	}
}
