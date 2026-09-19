package promptassembly

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

// DefaultMaxReviewRounds and DefaultMaxSlices are the orchestrator's shipped
// --max-review-rounds / --max-slices caps (issue #2460). assemble-prompt's own
// flags default to these values so a zero Handoff.Caps cannot silently turn
// both caps off (issue #2975). orchestrator/caps.go asserts the same constants
// in TestValidateCapsAcceptsShippedDefaults, so the two cannot drift.
const (
	DefaultMaxReviewRounds = 3
	DefaultMaxSlices       = 9
)

// LoadHandoffFile reads path and JSON-decodes it into a Handoff (issue #2975).
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

// ParseNonnegBudgetTokens parses s as a non-negative budget cap; ok is false
// when a negative or malformed value degraded to 0 (disabled) rather than
// erroring (issue #2694). The -max-budget-tokens flag must stay a flag.String:
// as a flag.Int, a malformed MAX_BUDGET_TOKENS failed fs.Parse and killed the
// box run under entrypoint.sh's set -euo pipefail (issue #2975).
func ParseNonnegBudgetTokens(s string) (n int, ok bool) {
	if v, err := strconv.Atoi(s); err == nil && v >= 0 {
		return v, true
	}
	return 0, false
}

// ParseNonnegBudgetUSD is ParseNonnegBudgetTokens' -max-budget-usd counterpart.
func ParseNonnegBudgetUSD(s string) (n float64, ok bool) {
	if v, err := strconv.ParseFloat(s, 64); err == nil && v >= 0 {
		return v, true
	}
	return 0, false
}
