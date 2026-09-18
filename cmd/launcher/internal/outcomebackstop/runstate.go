package outcomebackstop

import (
	"encoding/json"
	"os"

	"spindrift.dev/launcher/internal/runstate"
)

// readLastVerdict returns the LastVerdict word from the run-state artifact at
// path, degrading to "" on any failure so the backstop's always-emit invariant
// (#593) survives a missing or malformed artifact. Ignoring the unmarshal error
// is deliberate: runstate.ReadRunState drops a RunState whose sibling field
// failed to parse, and with it a LastVerdict that decoded fine.
func readLastVerdict(path string) string {
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var s runstate.RunState
	_ = json.Unmarshal(data, &s)
	return s.LastVerdict
}
