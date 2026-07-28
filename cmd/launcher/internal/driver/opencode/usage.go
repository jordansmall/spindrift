package opencode

import "spindrift.dev/launcher/internal/usage"

// ExtractUsage is a deliberate no-op: opencode's `--format json` NDJSON
// stream does not yet have a documented usage/token-accounting event wired
// into this Driver (unlike claude's type:"result" event), so this always
// reports usage.Report{Found: false} regardless of log content. A future
// slice that wires opencode's own usage-event schema replaces this body —
// see issue #262.
//
// Because this sums no per-message usage at all, there is no per-event
// over-count to guard against here: the claude driver's per-message.id
// dedup (issue #2109, guarding claude-code's habit of re-emitting a
// multi-content-block assistant message once per block, each line
// repeating the same byte-identical per-call usage) has no counterpart
// in this file.
// When the future slice above wires opencode's own usage-event schema, it
// must check whether opencode's stream re-emits multi-block messages the
// same way and, if so, apply the same distinct-message.id dedup.
func ExtractUsage(logPath string) (usage.Report, error) {
	return usage.Report{Found: false}, nil
}
