package forgejo_test

import "spindrift.dev/launcher/internal/forge"

// testLabels is the conventional lifecycle-label set, mirrored from
// lib/env-schema.nix (issue #460).
var testLabels = forge.DispatchLabels{
	Dispatchable: "ready-for-agent",
	InProgress:   "agent-in-progress",
	Complete:     "agent-complete",
	Failed:       "agent-failed",
}
