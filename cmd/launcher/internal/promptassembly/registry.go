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
	// Gates (gates.go) returns in its map. Empty means ungated: the row's
	// fragment renders unconditionally.
	Gate string `json:"gate"`
	// Fragment is the basename under prompts/fragments/ rendered when Gate is on.
	// On an axis row it is a basename under prompts/fragments/<backend>/ instead.
	Fragment string `json:"fragment"`
	// Axis is "" for a plain row, or "tracker" / "forge" for a row whose
	// fragment comes from the active backend's own folder: <backend> is the
	// active ISSUE_TRACKER (tracker) or CODE_FORGE (forge) backend name. A
	// backend with no such file renders an empty step, not an error.
	Axis string `json:"axis,omitempty"`
	// Var is the variable the fragment text is assigned to. The assignment
	// still happens when the gate is off, with empty text.
	Var string `json:"var"`
	// ExtraSubstVars lists substitution-allowlist entries the fragment body
	// references beyond Var itself. Empty for most rows; lib/fragments.nix is
	// the authority for which rows set it.
	ExtraSubstVars []string `json:"extraSubstVars,omitempty"`
	// SignalChannel names the markerChannels id ("comment", "pr-intent",
	// "issue-intent") the fragment instructs, set only on the paired
	// _LOG/_SOCKET rows (issue #3726). Unread by Go: mirrored so the drift
	// check keeps comparing whole rows, and consumed by
	// lib/prompt-contract.nix's buildTimeSignalFragmentViolations.
	SignalChannel string `json:"signalChannel,omitempty"`
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
	reg, err := LoadRegistry(f)
	if err != nil {
		return Registry{}, fmt.Errorf("parse fragment registry %s: %w", path, err)
	}
	return reg, nil
}

// resolvedFragment returns the fragment's path under prompts/fragments/. An
// axis row resolves into the active backend's folder; an empty Env value takes
// the same default the gates do.
func (r FragmentRow) resolvedFragment(e Env) (string, error) {
	var backend string
	switch r.Axis {
	case "":
		return r.Fragment, nil
	case "tracker":
		if backend = e.IssueTracker; backend == "" {
			backend = defaultIssueTracker
		}
	case "forge":
		// Prefer the pre-resolved ForgeBackend the forge gates read, so a
		// forge-axis row lands in the folder its gates assume (git and local
		// ride the "GH" arm).
		switch e.ForgeBackend {
		case "GH":
			backend = "github"
		case "FORGEJO":
			backend = "forgejo"
		default:
			if backend = e.CodeForge; backend == "" {
				backend = defaultCodeForge
			}
		}
	default:
		return "", fmt.Errorf("fragment %s (var %s): unknown axis %q", r.Fragment, r.Var, r.Axis)
	}
	return backend + "/" + r.Fragment, nil
}
