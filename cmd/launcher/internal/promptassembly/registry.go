package promptassembly

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/backend"
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
	// LegacyFragment is a root-level basename under prompts/fragments/ read
	// only when Fragment's file is missing: a deprecation-window fallback for
	// an override directory still on the old layout (issue #5646).
	LegacyFragment string `json:"legacyFragment,omitempty"`
	// Axis is "" for a plain row, or "tracker" / "filer" / "forge" for a row
	// whose fragment comes from the folder of that axis's signal: the tracker
	// read arm, the tracker Filer arm, or the CODE_FORGE arm. These are the
	// signals the axis's gates test, so jira (on github's read arm) reads
	// github/'s file. A folder with no such file renders an empty step, not
	// an error, unless Shared is set.
	Axis string `json:"axis,omitempty"`
	// Shared (axis rows only) names a root-level fragment under
	// prompts/fragments/ read when the axis folder has no file of its own:
	// one body for every backend, naming the active one through
	// ${FORGE_DISPLAY_NAME}. A backend folder's file still wins.
	Shared bool `json:"shared,omitempty"`
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

// signalFolder maps an axis signal ("GH", "GITHUB", "LOCAL", "FORGEJO") to the
// fragments/ folder holding that arm's files. "GH" is the forge/filer spelling
// of the github arm, and an empty signal takes it too, as trackerGates does
// for an empty itFiler, so a folder and its gate cannot disagree.
func signalFolder(signal string) string {
	if signal == "GH" || signal == "" {
		return "github"
	}
	return strings.ToLower(signal)
}

// forgeSignal is Env's pre-resolved CODE_FORGE backend suffix, re-derived from
// CodeForge for a host launcher older than #2533 that never sets ForgeBackend
// (git and local ride the "GH" arm).
func forgeSignal(e Env) string {
	if e.ForgeBackend != "" {
		return e.ForgeBackend
	}
	return backend.ForgeBackendSignal(e.CodeForge)
}

// trackerReadFilerSignals is Env's tracker read and filer axis signals,
// re-derived from IssueTracker for a host launcher that never sets them.
func trackerReadFilerSignals(e Env) (read, filer string) {
	read, filer = e.TrackerAxisRead, e.TrackerAxisFiler
	if read == "" {
		read, _, filer = issueTrackerAxisFallback(e.IssueTracker)
	}
	return read, filer
}

// axisFolder is the fragments/ folder an axis row reads from: the folder of
// the same signal the axis's gates test, not of the backend's name, so a
// backend riding another's arm (jira on github's, a local tracker's Filer on
// GH) reads that arm's file. A plain row, with no axis, has no folder: "".
func (r FragmentRow) axisFolder(e Env) (string, error) {
	switch r.Axis {
	case "":
		return "", nil
	case "tracker", "filer":
		read, filer := trackerReadFilerSignals(e)
		if r.Axis == "filer" {
			return signalFolder(filer), nil
		}
		return signalFolder(read), nil
	case "forge":
		return signalFolder(forgeSignal(e)), nil
	default:
		return "", fmt.Errorf("fragment %s (var %s): unknown axis %q", r.Fragment, r.Var, r.Axis)
	}
}

// resolvedFragment returns the fragment's path under prompts/fragments/. An
// axis row resolves into its axis signal's folder; an empty Env value takes
// the same default the gates do.
func (r FragmentRow) resolvedFragment(e Env) (string, error) {
	folder, err := r.axisFolder(e)
	if err != nil {
		return "", err
	}
	if folder == "" {
		return r.Fragment, nil
	}
	return folder + "/" + r.Fragment, nil
}
