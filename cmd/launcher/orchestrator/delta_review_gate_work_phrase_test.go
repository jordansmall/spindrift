package main

import (
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/deltareview"
)

// TestLandPassOrderOrchestratorFragmentDeclaresGateWorkPhrase pins the
// prompt<->code coupling markers.go's own doc comment names the idiom for:
// deltareview.GateWorkPhrase is what GateWorkDeclared greps a land pass's
// decisions.md for (issue #3246), and that only works if the fragment
// telling the land pass to write "gate-discovered work" (issue #3245) still
// carries the same phrase. A reword on either side must fail this test
// instead of silently decoupling the gate from the prose that feeds it.
func TestLandPassOrderOrchestratorFragmentDeclaresGateWorkPhrase(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	content := readPromptFile(t, repoRoot, "fragments/land-pass-order-orchestrator.md")

	if !strings.Contains(strings.ToLower(content), deltareview.GateWorkPhrase) {
		t.Errorf("land-pass-order-orchestrator.md missing %q (case-insensitive), the phrase deltareview.GateWorkDeclared matches against", deltareview.GateWorkPhrase)
	}
}
