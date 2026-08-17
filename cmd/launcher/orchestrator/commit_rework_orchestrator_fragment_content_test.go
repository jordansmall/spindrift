package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestCommitReworkOrchestratorFragmentInstructsFolding is a content-invariant
// guard for issue #2698: on an ORCHESTRATOR_ENABLED pass reworking a prior
// review round's findings, the COMMIT section must tell the agent to fold
// each fix into the commit it logically belongs to instead of stacking a new
// commit, while a pass authoring the first slice keeps today's "several small
// focused commits" behavior. Since there is no separate boolean env knob for
// "reworking findings" vs "authoring the first slice" (and adding one is out
// of scope), the fragment must read that distinction from run-state at
// runtime by checking for a seeded "## Run-state handoff" section carrying a
// `Last reviewer verdict:` line, the same line review-loop-orchestrator.md's
// own pattern reads — bare handoff presence alone is too coarse, since a
// worker-dispatch continuation pass can carry a handoff before any review
// round has run.
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
