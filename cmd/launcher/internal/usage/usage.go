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

// TotalTokens sums Usage's four billable token categories -- the single
// source of this sum, shared by every budget-cap comparison that needs a
// caller-summed token count (settle's own budgetExceeded, and the
// orchestrator's pass-machine budget cap, issue #2694) instead of each
// repeating the same four-field addition.
func (u Usage) TotalTokens() int {
	return u.InputTokens + u.OutputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
}

// UnknownModel is the Model value used when a driver's log carried no model
// field.
const UnknownModel = "unknown"

// ModelUsage holds token usage aggregated across every turn and subagent for
// one model, split into the five billable categories. Tokens only, never
// dollars — counts are exact from the API, whereas any cost figure needs
// pricing spindrift does not own (issue #2085).
type ModelUsage struct {
	Model                string // exact model id, e.g. "claude-opus-4-8"; UnknownModel if the log carried no model field
	UncachedInputTokens  int
	OutputTokens         int
	CacheReadInputTokens int
	CacheWrite5mTokens   int // cache_creation.ephemeral_5m_input_tokens, summed
	CacheWrite1hTokens   int // cache_creation.ephemeral_1h_input_tokens, summed
}

// MainLoopAgent is the Agent label SummedByAgent uses for messages with no
// parent_tool_use_id — the pass's own top-level loop, as opposed to a spawned
// subagent. It is deliberately not driverkit.ImplementorRole: a review pass's
// main loop is the reviewer, not the implementor, so a role-neutral label is
// the honest one at this layer. The pass's own role (implement/review/fix/
// land) is carried separately by the orchestrator's spindrift_op, not by
// this per-agent breakdown.
const MainLoopAgent = "main"

// AgentUsage holds token usage aggregated across every turn for one agent —
// the main loop (MainLoopAgent) or one spawned subagent, keyed by its
// subagent_type — split into the four billable token categories, plus a
// count of the distinct API calls (deduplicated messages) that produced
// them.
type AgentUsage struct {
	Agent                    string // MainLoopAgent, or a subagent_type (e.g. "scout"); driverkit.DefaultRole ("subagent") when a Task carried none
	APICalls                 int    // count of distinct (deduplicated) messages attributed to this agent
	UncachedInputTokens      int
	OutputTokens             int
	CacheReadInputTokens     int
	CacheCreationInputTokens int
}

// TotalTokens sums AgentUsage's four billable token fields. APICalls is a
// call count, not a token count, so it is deliberately excluded.
func (a AgentUsage) TotalTokens() int {
	return a.UncachedInputTokens + a.OutputTokens + a.CacheReadInputTokens + a.CacheCreationInputTokens
}

// Report combines a Box run's aggregate usage totals with its per-model and
// per-agent breakdowns, as extracted by a Driver's ExtractUsage from one pass
// over a Box log. Found is false when the log contains no result event (or
// does not exist), in which case Totals is zero-valued.
//
// Totals and SummedByModel/SummedByAgent obey different aggregation rules
// and deliberately do not reconcile numerically — that is a documented fact,
// not a bug:
//
//   - Totals is the run's totals summed across every session in the log:
//     every result event for claude, a plain sum over step_finish events for
//     opencode. It is not a per-message sum.
//   - SummedByModel is the per-call sums keyed by model, deduplicated by
//     message id — a different rule from Totals.
//   - SummedByAgent is the per-call sums keyed by agent (the main loop vs
//     each spawned subagent), deduplicated by message id the same way as
//     SummedByModel — so a single expensive worker is identifiable
//     separately from the main loop that spawned it.
type Report struct {
	Totals        Usage
	Found         bool
	SummedByModel []ModelUsage
	SummedByAgent []AgentUsage

	// EarliestEventMs and LatestEventMs are the earliest and latest
	// top-level event timestamps seen in the log, in unix milliseconds --
	// only meaningful when HasEventSpan is true. A driver populates these
	// from whatever per-event timestamp field its own log format carries,
	// letting a caller derive a wall-time span across MULTIPLE logs the
	// same way a driver already derives one across multiple sessions
	// within one log.
	EarliestEventMs, LatestEventMs int64
	// HasEventSpan is true when the log carried at least one usable event
	// timestamp -- false for a log with no timestamped lines at all (e.g.
	// too short, or a format that never emits one), in which case
	// EarliestEventMs/LatestEventMs are zero and meaningless.
	HasEventSpan bool
}
