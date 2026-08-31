package console

import (
	"fmt"
	"path/filepath"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/passmanifest"
)

// RunningPassState returns the live per-pass status a running pick's queue
// row shows (issue #2983): the last entry of number's pass manifest,
// formatted as "pass <N> (<kind>)" or "pass <N> (<kind>: <verdict>)" when a
// verdict was recorded. Kind can be empty on a stale or malformed entry
// (manifest.json is Box-authored, untrusted input); RunningPassState then
// drops the kind from the parenthetical rather than emitting "()" or
// "(: <verdict>)", falling back to "pass <N>" or "pass <N> (<verdict>)".
// Returns "" when no manifest file exists yet (the
// common case: no outbox mounted, or a legacy/non-orchestrator box, or one
// that simply hasn't written its first pass yet) or the file is empty or
// malformed — degrading silently, the same "no heartbeat yet" contract
// RunningHeartbeat's own callers already rely on. Unlike RunningHeartbeat,
// this does no incremental tailing or caching: passmanifest.Read is a single
// small ReadFile+Unmarshal against a handful of JSON entries, cheap enough
// to redo whole on every refresh.
func RunningPassState(pwd, number string) string {
	entries, err := passmanifest.Read(passManifestPath(pwd, number))
	if err != nil || len(entries) == 0 {
		return ""
	}
	last := entries[len(entries)-1]
	switch {
	case last.Kind != "" && last.Verdict != "":
		return fmt.Sprintf("pass %d (%s: %s)", last.Pass, last.Kind, last.Verdict)
	case last.Kind != "":
		return fmt.Sprintf("pass %d (%s)", last.Pass, last.Kind)
	case last.Verdict != "":
		return fmt.Sprintf("pass %d (%s)", last.Pass, last.Verdict)
	default:
		return fmt.Sprintf("pass %d", last.Pass)
	}
}

// passManifestPath returns number's pass-manifest path under pwd's outbox —
// the same path a mounted-outbox Box's orchestrator writes to (wired by
// entrypoint.sh's -manifest-path flag); this reads whatever's there, or
// nothing at all.
func passManifestPath(pwd, number string) string {
	return filepath.Join(dispatch.OutboxDirFor(pwd, number), passmanifest.FileName)
}
