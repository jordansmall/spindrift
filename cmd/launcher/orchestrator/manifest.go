package main

import (
	"encoding/json"
	"fmt"
	"os"

	"spindrift.dev/launcher/internal/usage"
)

// PassManifestEntry is one pass's own advisory summary (issue #2983): pass
// identity the host currently has to re-derive by heuristically parsing
// spindrift_op heartbeat lines out of the raw Driver stream. It is
// Box-authored advisory evidence only -- Resolved outcome tier selection and
// every settle decision are computed independently and never consult it
// (the Box advises, the Launcher decides).
type PassManifestEntry struct {
	Pass         int         `json:"pass"`
	Kind         string      `json:"kind"`
	Verdict      string      `json:"verdict,omitempty"`
	OutcomeFound bool        `json:"outcome_found"`
	Usage        usage.Usage `json:"usage"`
}

// writePassManifest overwrites path with manifest JSON-array-encoded,
// best-effort: a write failure is logged to stderr and otherwise ignored --
// the manifest is optional advisory evidence, never a gate on the pass's own
// decision, mirroring runstate.WriteRunState's own caller's degrade-not-error
// contract. Empty path means no manifest artifact is wired for this run
// (e.g. no outbox mounted) and is a silent no-op -- callers must check this
// themselves before constructing anything expensive, though today building
// one PassManifestEntry is cheap enough not to matter.
func writePassManifest(path string, manifest []PassManifestEntry) {
	if path == "" {
		return
	}
	b, err := json.Marshal(manifest)
	if err != nil {
		fmt.Fprintln(os.Stderr, "orchestrator: encode pass manifest:", err)
		return
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "orchestrator: write pass manifest:", err)
	}
}
