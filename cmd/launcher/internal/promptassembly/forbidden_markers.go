package promptassembly

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// ForbiddenMarkerRow mirrors one row of lib/prompt-contract.nix's
// forbiddenMarkers registry (issue #2464, plus kind and enforce from issue
// #2499), decoded from its builtins.toJSON output. Where validateMarkers
// asserts a marker is present under an active gate, this asserts it is
// absent. Issue #2513 left readonlyguards the only consumer.
type ForbiddenMarkerRow struct {
	ID string `json:"id"`
	// Marker is the literal text a rendered prompt must not carry as an
	// imperative telling a read-only Box to perform the operation.
	Marker string `json:"marker"`
	// Carrier names where Marker would appear if it were wrongly present,
	// e.g. "fragment-body". No code reads it.
	Carrier string `json:"carrier"`
	// Severity is "reject" or "warn", the same vocabulary as
	// ValidateMarkerRow.Severity.
	Severity string `json:"severity"`
	// When names the gate condition that activates this row, the same
	// vocabulary as ValidateMarkerRow.When.
	When string `json:"when"`
	// Kind is "substring" for a row whose Marker is scanned as literal
	// rendered text, or "gh-api-mutation" for the one row backed by
	// readonlyguards.go's `gh api` argument scan, whose Marker is
	// display-only and never scanned as a prompt substring.
	Kind string `json:"kind"`
	// Enforce names the runtime mechanism that stops the operation:
	// "command-shim" (a PATH-shadowing wrapper), "git-hook", or "prompt-only"
	// when only the build-time corpus scan enforces the row, because a
	// runtime guard would collide with a legitimate in-box use of the same
	// operation.
	Enforce string `json:"enforce"`
	// Message is pre-rendered by the nix registry with the marker already
	// interpolated.
	Message string `json:"message"`
	// RuntimeMessage is the wording readonlyguards renders into the installed
	// shim or hook script when Enforce is "git-hook" or "command-shim" (issue
	// #2509). Message is written for a prompt-facing diagnostic and reads as
	// nonsense from a shim that rejected one command mid-run while the run
	// continues. A "prompt-only" row leaves this empty.
	RuntimeMessage string `json:"runtimeMessage"`
}

// LoadForbiddenMarkers parses a forbiddenMarkers registry document from r: a
// bare JSON array of ForbiddenMarkerRow objects.
func LoadForbiddenMarkers(r io.Reader) ([]ForbiddenMarkerRow, error) {
	var rows []ForbiddenMarkerRow
	if err := json.NewDecoder(r).Decode(&rows); err != nil {
		return nil, fmt.Errorf("decode forbidden markers registry: %w", err)
	}
	return rows, nil
}

// LoadForbiddenMarkersFile opens path and loads it via LoadForbiddenMarkers.
func LoadForbiddenMarkersFile(path string) ([]ForbiddenMarkerRow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open forbidden markers registry %s: %w", path, err)
	}
	defer f.Close()
	return LoadForbiddenMarkers(f)
}
