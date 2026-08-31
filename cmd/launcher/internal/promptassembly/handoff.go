package promptassembly

import (
	"encoding/json"
	"fmt"
	"os"
)

// DefaultMaxReviewRounds and DefaultMaxSlices are the orchestrator's shipped
// --max-review-rounds / --max-slices caps (issue #2460), exported here
// rather than as unexported constants in orchestrator/caps.go so
// assemble-prompt's own --max-review-rounds/--max-slices flags (which
// populate Handoff.Caps) can default to the same values: entrypoint.sh has
// never forwarded an explicit override for either knob, so the orchestrator
// running with no operator override has always meant "run with these two
// defaults," not "run with both caps disabled" -- a Handoff.Caps zero value
// must not silently change that (issue #2975). orchestrator/caps.go
// references these same constants for its own coherence test
// (TestValidateCapsAcceptsShippedDefaults), so the two can never drift.
const (
	DefaultMaxReviewRounds = 3
	DefaultMaxSlices       = 9
)

// LoadHandoffFile reads path and JSON-decodes it into a Handoff, for a
// driver-exec/orchestrator invocation to consume the static per-run
// configuration assemble-prompt already wrote to disk (issue #2975).
func LoadHandoffFile(path string) (Handoff, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Handoff{}, fmt.Errorf("read handoff file %s: %w", path, err)
	}

	var h Handoff
	if err := json.Unmarshal(data, &h); err != nil {
		return Handoff{}, fmt.Errorf("parse handoff file %s: %w", path, err)
	}

	return h, nil
}
