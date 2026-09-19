// Package usage holds Driver-agnostic usage-report types and formatting.
// Each Driver parses a Box log into a Report behind its ExtractUsage method
// (ADR 0009); this package never reads a log itself.
package usage

import "fmt"

// FormatDuration renders a millisecond count as "Xh Ym Zs", "Xm Ys", or "Xs".
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

// TotalTokens sums Usage's four billable token categories. Budget-cap
// comparisons share this sum rather than repeat the addition (issue #2694).
func (u Usage) TotalTokens() int {
	return u.InputTokens + u.OutputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
}

// UnknownModel is the Model value for a log that carried no model field.
const UnknownModel = "unknown"

// ModelUsage holds token usage aggregated across every turn and subagent for
// one model. Tokens only, never dollars: the API's counts are exact, whereas
// a cost figure needs pricing spindrift does not own (issue #2085).
type ModelUsage struct {
	Model                string // exact model id, e.g. "claude-opus-4-8"; UnknownModel if the log carried none
	UncachedInputTokens  int
	OutputTokens         int
	CacheReadInputTokens int
	CacheWrite5mTokens   int // cache_creation.ephemeral_5m_input_tokens, summed
	CacheWrite1hTokens   int // cache_creation.ephemeral_1h_input_tokens, summed
}

// MainLoopAgent is the Agent label SummedByAgent uses for messages with no
// parent_tool_use_id. It is deliberately not driverkit.ImplementorRole: a
// review pass's main loop is the reviewer, so only a role-neutral label is
// accurate here. The orchestrator's spindrift_op carries the pass's role.
const MainLoopAgent = "main"

// AgentUsage holds token usage aggregated across every turn for one agent,
// either the main loop (MainLoopAgent) or one spawned subagent keyed by its
// subagent_type.
type AgentUsage struct {
	Agent               string // MainLoopAgent, or a subagent_type (e.g. "scout"); driverkit.DefaultRole ("subagent") when a Task carried none
	APICalls            int    // count of distinct (deduplicated) messages attributed to this agent
	UncachedInputTokens int
	// OutputTokens breaks its sibling fields' per-message-sum rule when
	// the report sets Report.OutputIsMainLoopOnly; see that field.
	OutputTokens             int
	CacheReadInputTokens     int
	CacheCreationInputTokens int
}

// TotalTokens sums AgentUsage's four billable token fields, excluding
// APICalls, which counts calls rather than tokens.
func (a AgentUsage) TotalTokens() int {
	return a.UncachedInputTokens + a.OutputTokens + a.CacheReadInputTokens + a.CacheCreationInputTokens
}

// Report combines a Box run's aggregate usage totals with its per-model and
// per-agent breakdowns. Found is false when the log has no result event or
// does not exist, and Totals is then zero-valued. Totals sums whole sessions,
// whereas SummedByModel and SummedByAgent sum per call and deduplicate by
// message id, so they deliberately do not reconcile numerically.
type Report struct {
	Totals        Usage
	Found         bool
	SummedByModel []ModelUsage
	SummedByAgent []AgentUsage

	// OutputIsMainLoopOnly marks SummedByAgent's OutputTokens column as the
	// main loop's alone (issue #3213): claude-code's per-message
	// output_tokens is a message_start placeholder around 100x too low
	// (#3183), and only the main loop gets a result event, so its row
	// carries that figure and every subagent row carries 0.
	OutputIsMainLoopOnly bool

	// EarliestEventMs and LatestEventMs are the earliest and latest
	// top-level event timestamps in the log, in unix milliseconds, and are
	// meaningful only when HasEventSpan is true. A caller derives a
	// wall-time span across several logs from them.
	EarliestEventMs, LatestEventMs int64
	// HasEventSpan is false when the log carried no usable event timestamp
	// at all, in which case EarliestEventMs and LatestEventMs are zero.
	HasEventSpan bool
}
