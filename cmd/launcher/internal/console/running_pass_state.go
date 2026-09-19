package console

import (
	"fmt"
	"path/filepath"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/passmanifest"
)

// RunningPassState returns a running pick's live pass status (issue #2983) as
// "pass <N> (<kind>: <verdict>)", dropping either part the last manifest entry
// leaves empty: manifest.json is Box-authored, untrusted input. Returns "" when
// no manifest exists yet or it is empty or malformed. Unlike RunningHeartbeat it
// neither tails nor caches; the file is small enough to re-read on every refresh.
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

// passManifestPath mirrors the path entrypoint.sh hands the Box orchestrator as
// -manifest-path; this side reads whatever is there, or nothing at all.
func passManifestPath(pwd, number string) string {
	return filepath.Join(dispatch.OutboxDirFor(pwd, number), passmanifest.FileName)
}
