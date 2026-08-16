package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// realPromptsDirT resolves this repo's own templates/default/prompts
// directory to an absolute path, so a test can point PROMPTS_DIR at it
// (via t.Setenv) and exercise conflictResolveGuidance's real render path --
// resolved before any t.Chdir the calling test performs, since
// filepath.Abs itself depends on the process's current working directory.
func realPromptsDirT(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", "..", "..", "templates", "default", "prompts"))
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

// TestConflictResolveGuidanceRendersRealTemplate verifies
// conflictResolveGuidance (issue #2060 review finding) renders
// conflict-resolve-cherry-pick-prompt.md's own real content -- not a
// hand-written string -- with branch/revRange substituted into the
// template's ${BRANCH}/${REV_RANGE} placeholders.
func TestConflictResolveGuidanceRendersRealTemplate(t *testing.T) {
	t.Setenv("PROMPTS_DIR", realPromptsDirT(t))

	got := conflictResolveGuidance("orchestrator-worker/my-slice", "abc123..def456")

	// A distinctive phrase load-bearing to the template's own identity,
	// mirroring how tests elsewhere in this package pin prose against the
	// real template file (review_prompt_content_test.go).
	if !strings.Contains(got, "Reproduce the cherry-pick, resolve the conflicts") {
		t.Errorf("conflictResolveGuidance() = %q, want it to carry the real template's own prose", got)
	}
	if !strings.Contains(got, "orchestrator-worker/my-slice") {
		t.Errorf("conflictResolveGuidance() = %q, want the branch name substituted in", got)
	}
	if !strings.Contains(got, "abc123..def456") {
		t.Errorf("conflictResolveGuidance() = %q, want the revision range substituted in", got)
	}
	if strings.Contains(got, "${BRANCH}") || strings.Contains(got, "${REV_RANGE}") {
		t.Errorf("conflictResolveGuidance() = %q, want no unsubstituted placeholder left in the rendered text", got)
	}
	if strings.Contains(got, "git rebase") {
		t.Errorf("conflictResolveGuidance() = %q, want cherry-pick guidance, not the rebase-flavored template's own prose", got)
	}
}

// TestConflictResolveGuidanceFallsBackWhenTemplateMissing verifies
// conflictResolveGuidance degrades to a short hand-rolled instruction
// (rather than an empty string, a Go error, or a panic) when
// PROMPTS_DIR names a directory that doesn't carry
// conflict-resolve-cherry-pick-prompt.md -- a missing/unreadable template
// must never abort dispatch itself (dispatchManifestIfPresent's own
// composition of this function's return value is a plain string append).
func TestConflictResolveGuidanceFallsBackWhenTemplateMissing(t *testing.T) {
	t.Setenv("PROMPTS_DIR", t.TempDir())

	got := conflictResolveGuidance("orchestrator-worker/my-slice", "abc123..def456")

	if !strings.Contains(got, "git cherry-pick --no-commit") {
		t.Errorf("conflictResolveGuidance() = %q, want the fallback instruction naming the cherry-pick command", got)
	}
	if !strings.Contains(got, "orchestrator-worker/my-slice") {
		t.Errorf("conflictResolveGuidance() = %q, want the fallback to still name the branch", got)
	}
}

// TestPromptsDirDefaultsWhenUnset verifies promptsDir falls back to
// defaultPromptsDir ("/agent/prompts", mirroring lib/agent-paths.nix's own
// PROMPTS_DIR default) when the PROMPTS_DIR env var isn't set at all.
func TestPromptsDirDefaultsWhenUnset(t *testing.T) {
	os.Unsetenv("PROMPTS_DIR")

	if got := promptsDir(); got != defaultPromptsDir {
		t.Errorf("promptsDir() = %q, want %q", got, defaultPromptsDir)
	}
}
