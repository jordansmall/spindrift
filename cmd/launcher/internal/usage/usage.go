// Package usage holds Driver-agnostic usage-report types and formatting.
// Parsing a Box log into a Report is each Driver's own job, behind the
// Driver interface's ExtractUsage method (ADR 0009) — this package never
// reads a log itself.
package usage

import "fmt"

// FormatDuration converts a millisecond count to a human-readable string.
// Outputs "Xh Ym Zs", "Xm Ys", or "Xs" depending on magnitude.
func FormatDuration(ms int64) string {
	s := ms / 1000
	h := s / 3600
	m := (s % 3600) / 60
	sec := s % 60
	if h > 0 {
		return fmt.Sprintf("%dh %dm %ds", h, m, sec)
	}
	if m > 0 {
		return fmt.Sprintf("%dm %ds", m, sec)
	}
	return fmt.Sprintf("%ds", sec)
}

// Usage holds the aggregate statistics from a result event.
type Usage struct {
	InputTokens              int     `json:"input_tokens"`
	OutputTokens             int     `json:"output_tokens"`
	CacheReadInputTokens     int     `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int     `json:"cache_creation_input_tokens"`
	TotalCostUSD             float64 `json:"total_cost_usd"`
	DurationMs               int64   `json:"duration_ms"`
	DurationApiMs            int64   `json:"duration_api_ms"`
	NumTurns                 int     `json:"num_turns"`
}

// ModelUsage holds token usage aggregated across every turn and subagent for
// one model, split into the five billable categories. Tokens only, never
// dollars — counts are exact from the API, whereas any cost figure needs
// pricing spindrift does not own (issue #2085).
type ModelUsage struct {
	Model                string // exact model id, e.g. "claude-opus-4-8"; "unknown" if the log carried no model field
	UncachedInputTokens  int
	OutputTokens         int
	CacheReadInputTokens int
	CacheWrite5mTokens   int // cache_creation.ephemeral_5m_input_tokens, summed
	CacheWrite1hTokens   int // cache_creation.ephemeral_1h_input_tokens, summed
}

// Report combines a Box run's aggregate usage snapshot with its per-model
// breakdown, as extracted by a Driver's ExtractUsage from one pass over a Box
// log. Found is false when the log contains no result event (or does not
// exist), in which case FinalSnapshot is zero-valued.
//
// FinalSnapshot and SummedByModel obey different aggregation rules and
// deliberately do not reconcile numerically — that is a documented fact, not
// a bug:
//
//   - FinalSnapshot is the Driver's own last-reported totals: last-wins for
//     claude (its final result-event snapshot), a plain sum over
//     step_finish events for opencode. It is not a per-message sum.
//   - SummedByModel is the per-call sums keyed by model, deduplicated by
//     message id — a different rule from FinalSnapshot.
type Report struct {
	FinalSnapshot Usage
	Found         bool
	SummedByModel []ModelUsage
}
