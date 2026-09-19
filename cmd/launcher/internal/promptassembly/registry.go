package promptassembly

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// FragmentRow is the Go mirror of one row in lib/fragments.nix's conditional
// fragment registry (issue #622). The JSON tags copy the nix attrset's field
// names literally so nix-rendered JSON decodes without renaming, which keeps
// the two sides checkable for drift.
type FragmentRow struct {
	// Gate is the bash gate variable tested for non-emptiness, the same key
	// Gates (gates.go) returns in its map.
	Gate string `json:"gate"`
	// Fragment is the basename under prompts/fragments/ rendered when Gate is on.
	Fragment string `json:"fragment"`
	// Var is the variable the fragment text is assigned to. The assignment
	// still happens when the gate is off, with empty text.
	Var string `json:"var"`
	// ExtraSubstVars lists substitution-allowlist entries the fragment body
	// references beyond Var itself. Empty for all but skill-preamble.md and
	// ci-failure.md as of issue #2462.
	ExtraSubstVars []string `json:"extraSubstVars,omitempty"`
	// InverseOf names the gate this row's Gate is the exact complement of;
	// lib/fragment-pairs.nix validates the pair's shape at eval time. The pair
	// holds only if Gates computes the two as complements, so Go carries the
	// claim over to check it against the real gate computation.
	InverseOf string `json:"inverseOf,omitempty"`
}

// Registry is the Go-side load of lib/fragments.nix's row list, in the same order.
type Registry struct {
	Rows []FragmentRow
}

// LoadRegistry parses a bare JSON array of FragmentRow objects from r, matching
// lib/fragments.nix's builtins.toJSON shape.
func LoadRegistry(r io.Reader) (Registry, error) {
	var rows []FragmentRow
	if err := json.NewDecoder(r).Decode(&rows); err != nil {
		return Registry{}, fmt.Errorf("decode fragment registry: %w", err)
	}
	return Registry{Rows: rows}, nil
}

// LoadRegistryFile opens path and loads it via LoadRegistry.
func LoadRegistryFile(path string) (Registry, error) {
	f, err := os.Open(path)
	if err != nil {
		return Registry{}, fmt.Errorf("open fragment registry %s: %w", path, err)
	}
	defer f.Close()
	return LoadRegistry(f)
}
