package promptassembly

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// FragmentRow is the Go mirror of one row in lib/fragments.nix's Conditional
// fragment registry (issue #622, CONTEXT.md "Conditional fragment"). JSON
// tags mirror the nix attrset's field names literally (gate/fragment/
// var/extraSubstVars) so a later slice's nix-rendered JSON (built from the
// same fragments.nix list via builtins.toJSON) decodes into this type
// without any renaming, keeping the two representations checkable for drift
// field-for-field.
type FragmentRow struct {
	// Gate is the bash gate variable name tested for non-emptiness to
	// include this fragment — the same key Gates (gates.go) returns in its
	// map (e.g. "SKILLS_FOUND", "ORCHESTRATOR").
	Gate string `json:"gate"`
	// Fragment is the basename under prompts/fragments/ rendered when Gate
	// is on.
	Fragment string `json:"fragment"`
	// Var is the bash/template variable the rendered (or, when the gate is
	// off, empty) fragment text is assigned to.
	Var string `json:"var"`
	// ExtraSubstVars lists additional substitution-allowlist entries the
	// fragment's own body references, beyond Var itself. Empty for all but
	// two of the 65 rows (skill-preamble.md, ci-failure.md) as of issue
	// #2462 — see fragments.nix's header comment.
	ExtraSubstVars []string `json:"extraSubstVars,omitempty"`
}

// Registry is the full set of FragmentRow entries — the Go-side load of
// lib/fragments.nix's list, in the same order.
type Registry struct {
	Rows []FragmentRow
}

// LoadRegistry reads and parses a fragment registry JSON document (a bare
// JSON array of FragmentRow objects, matching lib/fragments.nix's
// builtins.toJSON shape) from r. Malformed JSON is reported as a wrapped
// error, never a panic.
func LoadRegistry(r io.Reader) (Registry, error) {
	var rows []FragmentRow
	if err := json.NewDecoder(r).Decode(&rows); err != nil {
		return Registry{}, fmt.Errorf("decode fragment registry: %w", err)
	}
	return Registry{Rows: rows}, nil
}

// LoadRegistryFile opens path and loads it via LoadRegistry — a convenience
// wrapper for callers working from a filesystem path (e.g. the nix-baked
// registry file a later slice's CLI verb reads) rather than an already-open
// reader. A missing or unreadable file is reported as a wrapped error, never
// a panic.
func LoadRegistryFile(path string) (Registry, error) {
	f, err := os.Open(path)
	if err != nil {
		return Registry{}, fmt.Errorf("open fragment registry %s: %w", path, err)
	}
	defer f.Close()
	return LoadRegistry(f)
}
