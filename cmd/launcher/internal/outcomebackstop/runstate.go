package outcomebackstop

import (
	"encoding/json"
	"os"
)

// runState mirrors the subset of the orchestrator's own RunState
// (cmd/launcher/orchestrator/runstate.go) this package needs: just the bare
// last verdict word (e.g. "BLOCK" or "APPROVE"). Not imported directly --
// orchestrator is package main -- so this is an intentionally narrow local
// echo of that JSON shape, not a shared type.
type runState struct {
	LastVerdict string `json:"last_verdict"`
}

// readLastVerdict reads the run-state handoff artifact at path and returns
// its bare LastVerdict word. Any failure to resolve a verdict -- an empty
// path, a missing or unreadable file, or invalid JSON -- quietly degrades to
// "" (no verdict known) rather than propagating an error: the backstop's
// always-emit invariant (#593) must never be put at risk by a malformed or
// absent hand-off artifact.
func readLastVerdict(path string) string {
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var s runState
	if json.Unmarshal(data, &s) != nil {
		return ""
	}
	return s.LastVerdict
}
