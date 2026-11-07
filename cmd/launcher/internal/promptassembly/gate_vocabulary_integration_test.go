//go:build integration

package promptassembly

import (
	"encoding/json"
	"os"
	"testing"

	"spindrift.dev/launcher/internal/seamtest"
)

// TestGatesMatchVocabulary pins the Go half of the gate-vocabulary seam: the
// keys Gates() returns must equal the vocabulary lib/gate-vocabulary.nix
// declares, so a gate added on either side without the other fails here rather
// than as a silently unrendered fragment.
func TestGatesMatchVocabulary(t *testing.T) {
	raw, err := os.ReadFile(seamtest.Path(t, "gate-vocabulary.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vocabulary []string
	if err := json.Unmarshal(raw, &vocabulary); err != nil {
		t.Fatalf("decode gate-vocabulary.json: %v", err)
	}

	// Gates() assigns every key unconditionally (only values vary with Env), so
	// the zero Env yields the full key set. SKILLS_FOUND is exempt: the assembler
	// sets it (assemble.go), not Gates().
	undeclared, unreturned := diffGateVocabulary(Gates(Env{}), vocabulary, []string{"SKILLS_FOUND"})
	for _, g := range undeclared {
		t.Errorf("Gates() returns %q, which lib/gate-vocabulary.nix does not declare", g)
	}
	for _, g := range unreturned {
		t.Errorf("lib/gate-vocabulary.nix declares %q, which Gates(Env{}) does not return (missing, or assigned only under some Env)", g)
	}
}
