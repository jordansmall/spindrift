package main

import (
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/deltareview"
)

// GateWorkDeclared greps a land pass's decisions.md for
// deltareview.GateWorkPhrase (issue #3246), which only works if the fragment
// telling the land pass to write "gate-discovered work" (issue #3245) still
// carries that phrase. A reword on either side must fail here instead of
// silently decoupling the gate from the prose it reads.
func TestLandPassOrderOrchestratorFragmentDeclaresGateWorkPhrase(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	content := readPromptFile(t, repoRoot, "fragments/land-pass-order-orchestrator.md")

	if !strings.Contains(strings.ToLower(content), deltareview.GateWorkPhrase) {
		t.Errorf("land-pass-order-orchestrator.md missing %q (case-insensitive), the phrase deltareview.GateWorkDeclared matches against", deltareview.GateWorkPhrase)
	}
}
