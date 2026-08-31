package promptassembly

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
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

// ParseNonnegBudgetTokens parses s (assemble-prompt's own -max-budget-tokens
// flag value) as a non-negative integer budget cap, degrading a negative or
// malformed value to 0 (disabled) rather than erroring (issue #2694 review
// finding). Exported here, alongside DefaultMaxReviewRounds/DefaultMaxSlices,
// for the same "single source shared by driver-exec and orchestrator" reason
// (issue #2975 review finding #1): assemble-prompt's flag.Int for
// -max-budget-tokens made a malformed operator-forwarded MAX_BUDGET_TOKENS
// fail fs.Parse and kill the whole box run under entrypoint.sh's
// set -euo pipefail -- exactly the fatal outcome this degrade contract
// exists to prevent. The flag must instead be a flag.String, parsed leniently
// here after fs.Parse succeeds. This mirrors the host launcher's own
// atoiNonneg (cmd/launcher/main.go) tolerance for the identical
// MAX_BUDGET_TOKENS env var on the same bad input, though not the same
// mechanism: atoiNonneg falls back to a caller-supplied schema default,
// where this always falls back to the literal 0 (this flag's own default
// already is 0, so the two coincide for this specific knob). Unlike
// -max-parallel-workers, there is no meaningful "reject outright" case for a
// budget cap: 0 is already its own legitimate "disabled" sentinel, so a
// negative value simply collapses into that same sentinel instead of a
// distinct error state. ok is false when s needed degrading (negative or
// unparseable) -- callers that want to warn an operator about a mistyped
// value check it; nothing about this parse itself is fatal.
func ParseNonnegBudgetTokens(s string) (n int, ok bool) {
	if v, err := strconv.Atoi(s); err == nil && v >= 0 {
		return v, true
	}
	return 0, false
}

// ParseNonnegBudgetUSD is ParseNonnegBudgetTokens' -max-budget-usd
// counterpart, mirroring the host launcher's own floatNonnegSchema/
// floatNonneg the same way.
func ParseNonnegBudgetUSD(s string) (n float64, ok bool) {
	if v, err := strconv.ParseFloat(s, 64); err == nil && v >= 0 {
		return v, true
	}
	return 0, false
}
