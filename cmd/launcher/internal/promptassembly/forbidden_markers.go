package promptassembly

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// ForbiddenMarkerRow is the Go mirror of one row in
// lib/prompt-contract.nix's forbiddenMarkers registry (issue #2464), sharing
// ValidateMarkerRow's six fields/JSON tags -- id/marker/carrier/severity/
// when/message -- plus two more of its own, kind and enforce (issue #2499),
// decoded from nix-rendered JSON built via the same builtins.toJSON
// convention. Unlike validateMarkers, which asserts a
// marker is present under an active gate, forbiddenMarkers asserts a
// marker is absent -- specifically, never rendered as an imperative
// telling a read-only Box to perform the operation -- under an active
// gate. Issue #2513 deleted this package's own Validate loop over
// forbiddenMarkers; the sole remaining consumer of this type is
// cmd/launcher/internal/readonlyguards, which renders "git-hook"/
// "command-shim" rows into runtime guards.
type ForbiddenMarkerRow struct {
	// ID names the row, e.g. "forbidden-git-push".
	ID string `json:"id"`
	// Marker is the literal marker text a Box's rendered prompt must not
	// carry as an imperative.
	Marker string `json:"marker"`
	// Carrier documents (informationally only) where Marker would appear
	// if it were (wrongly) present, e.g. "fragment-body".
	Carrier string `json:"carrier"`
	// Severity is "reject" or "warn", same vocabulary as
	// ValidateMarkerRow.Severity.
	Severity string `json:"severity"`
	// When names which gate condition activates this row, same vocabulary
	// as ValidateMarkerRow.When.
	When string `json:"when"`
	// Kind is "substring" (the default meaning) for a row whose Marker is
	// checked as literal rendered text (lib/prompt-contract.nix's
	// buildTimeForbiddenMarkerViolations), or "gh-api-mutation" for the one
	// row backed by readonlyguards.go's `gh api` argument-scan shim, whose
	// Marker is display-only (not scanned as a prompt substring).
	Kind string `json:"kind"`
	// Enforce names which runtime mechanism actually stops this
	// operation: "command-shim" (a PATH-shadowing wrapper, e.g. the
	// read-only `gh` shim), "git-hook" (a pre-push/pre-receive hook), or
	// "prompt-only" (no runtime guard exists -- enforcement is solely the
	// build-time corpus scan, since a runtime guard would collide with a
	// legitimate in-box use of the same operation).
	Enforce string `json:"enforce"`
	// Message is the row's fully pre-rendered diagnostic prose, marker
	// already interpolated by the nix registry.
	Message string `json:"message"`
	// RuntimeMessage is the distinct, runtime-facing wording rendered into
	// the installed shim/hook script by
	// cmd/launcher/internal/readonlyguards when Enforce is "git-hook" or
	// "command-shim" (issue #2509). It is deliberately not Message: Message
	// stays written for a rendered-prompt-facing diagnostic ("the rendered
	// prompt orders...", "Refusing to invoke the Driver"), which is
	// nonsensical printed by a runtime shim after the agent typed the
	// offending command itself mid-run -- only that one command is
	// rejected, the run itself continues. A "prompt-only" row carries no
	// RuntimeMessage, so it is empty for such rows.
	RuntimeMessage string `json:"runtimeMessage"`
}

// LoadForbiddenMarkers reads and parses a forbiddenMarkers registry JSON
// document (a bare JSON array of ForbiddenMarkerRow objects, matching
// lib/prompt-contract.nix's builtins.toJSON shape) from r. Malformed JSON is
// reported as a wrapped error, never a panic.
func LoadForbiddenMarkers(r io.Reader) ([]ForbiddenMarkerRow, error) {
	var rows []ForbiddenMarkerRow
	if err := json.NewDecoder(r).Decode(&rows); err != nil {
		return nil, fmt.Errorf("decode forbidden markers registry: %w", err)
	}
	return rows, nil
}

// LoadForbiddenMarkersFile opens path and loads it via LoadForbiddenMarkers
// -- a convenience wrapper for callers working from a filesystem path (e.g.
// the nix-baked registry file readonlyguards' CLI verb reads) rather than an
// already-open reader. A missing or unreadable file is reported as a wrapped
// error, never a panic.
func LoadForbiddenMarkersFile(path string) ([]ForbiddenMarkerRow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open forbidden markers registry %s: %w", path, err)
	}
	defer f.Close()
	return LoadForbiddenMarkers(f)
}
