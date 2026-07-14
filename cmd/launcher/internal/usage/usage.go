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

// RoleUsage holds the aggregated token usage attributed to one subagent role.
type RoleUsage struct {
	Role                     string
	InputTokens              int
	OutputTokens             int
	CacheReadInputTokens     int
	CacheCreationInputTokens int
}

// Usage holds the aggregate statistics from a result event.
type Usage struct {
	InputTokens              int
	OutputTokens             int
	CacheReadInputTokens     int
	CacheCreationInputTokens int
	TotalCostUSD             float64
	DurationMs               int64
	DurationApiMs            int64
	NumTurns                 int
}

// Report combines a Box run's aggregate Usage with its per-role breakdown, as
// extracted by a Driver's ExtractUsage from one pass over a Box log. Found is
// false when the log contains no result event (or does not exist), in which
// case Usage and Roles are both zero-valued.
type Report struct {
	Usage
	Found bool
	Roles []RoleUsage
}
