package outcomebackstop

import (
	"encoding/json"
	"os"

	"spindrift.dev/launcher/internal/runstate"
)

// readLastVerdict reads the run-state handoff artifact at path and returns
// its bare LastVerdict word. Any failure to resolve a verdict -- an empty
// path, a missing or unreadable file, or invalid JSON -- quietly degrades to
// "" (no verdict known) rather than propagating an error: the backstop's
// always-emit invariant (#593) must never be put at risk by a malformed or
// absent hand-off artifact. This deliberately reimplements the file read and
// degrade-to-empty here rather than calling runstate.ReadRunState directly:
// that helper discards its partially-decoded RunState on any parse error,
// but this backstop wants to still recover LastVerdict from an artifact
// that only partially conforms (e.g. a sibling field with a JSON type
// mismatch) rather than lose it.
func readLastVerdict(path string) string {
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var s runstate.RunState
	json.Unmarshal(data, &s)
	return s.LastVerdict
}
