package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// Guards issue #2698: on an ORCHESTRATOR_ENABLED rework pass the COMMIT section must
// tell the agent to fold each fix into the commit it belongs to, while a first-slice
// pass keeps "several small focused commits". No env knob marks that difference, so
// the fragment reads the handoff's `Last reviewer verdict:` line; bare handoff
// presence is too coarse, since a continuation pass can carry one before any review.
func TestCommitReworkOrchestratorFragmentInstructsFolding(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	content := readPromptFile(t, repoRoot, "fragments/commit-rework-orchestrator.md")

	for _, want := range []string{
		"Run-state handoff",
		"Last reviewer verdict:",
		"git commit --amend",
		"autosquash fixup",
		"force-pushes",
		"small focused commits",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("commit-rework-orchestrator.md missing %q", want)
		}
	}
}
