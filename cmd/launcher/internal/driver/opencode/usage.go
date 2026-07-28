package opencode

import "spindrift.dev/launcher/internal/usage"

// ExtractUsage is a deliberate no-op: opencode's `--format json` NDJSON
// stream does not yet have a documented usage/token-accounting event wired
// into this Driver (unlike claude's type:"result" event), so this always
// reports usage.Report{Found: false} regardless of log content. A future
// slice that wires opencode's own usage-event schema replaces this body —
// see issue #262.
func ExtractUsage(logPath string) (usage.Report, error) {
	return usage.Report{Found: false}, nil
}
