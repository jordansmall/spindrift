package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// RunState is the compact run-state handoff artifact the orchestrator owns
// (issue #1997, parent #1627): a tmp file outside the repo -- the same
// durability convention SCOUT's /tmp/brief.md already relies on to survive
// context compaction -- recording what a fresh implementor pass needs to
// continue without reading a transcript. Anything not written here is lost
// between passes, so these are exactly the fields a later slice can depend
// on; everything else (raw driver output, prompt text, reviewer prose beyond
// the verdict word) is intentionally not carried and must be re-derived or
// re-scoped into a new prompt instead.
type RunState struct {
	// DoneSlices names each implementor slice that has already landed, in
	// completion order.
	DoneSlices []string `json:"done_slices"`
	// RemainingSlices names each implementor slice still to run, in the
	// order the loop intends to take them.
	RemainingSlices []string `json:"remaining_slices"`
	// LastVerdict is the most recent reviewer verdict word (e.g. "APPROVE"
	// or "BLOCK"), empty when no review has run yet.
	LastVerdict string `json:"last_verdict"`
	// ScoutBriefPath is the path to the scout's brief (conventionally
	// /tmp/brief.md) -- referenced by path, not inlined, so this file stays
	// small and the brief stays the single source of truth for scout
	// findings.
	ScoutBriefPath string `json:"scout_brief_path"`
}

// ReadRunState reads and parses the run-state artifact at path. An empty
// path or a path with no file yet (the first pass of a run) returns a zero
// RunState with no error -- there is nothing to hand off yet, not a failure.
func ReadRunState(path string) (RunState, error) {
	if path == "" {
		return RunState{}, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return RunState{}, nil
	}
	if err != nil {
		return RunState{}, fmt.Errorf("read run state %s: %w", path, err)
	}
	var s RunState
	if err := json.Unmarshal(data, &s); err != nil {
		return RunState{}, fmt.Errorf("parse run state %s: %w", path, err)
	}
	return s, nil
}

// WriteRunState writes s to path as indented JSON, for the next implementor
// pass (or a human) to inspect. A no-op when path is empty.
func WriteRunState(path string, s RunState) error {
	if path == "" {
		return nil
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal run state: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write run state %s: %w", path, err)
	}
	return nil
}
