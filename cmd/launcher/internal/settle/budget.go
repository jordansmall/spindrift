package settle

import (
	"fmt"
	"strings"

	"spindrift.dev/launcher/internal/usage"
)

// budgetExceeded reports whether cumulative usage u has reached or passed
// either of Config's budget caps, and if so, a human-readable reason naming
// which cap(s) tripped — for the console status line and the issue comment.
// A zero cap on either dimension means "no cap" there (issue #2001),
// matching MaxFixAttempts' own 0-disables convention rather than a second
// sentinel; the two dimensions are independent, so either can trip first.
func budgetExceeded(cfg Config, u usage.Usage) (bool, string) {
	tokens := u.InputTokens + u.OutputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
	var reasons []string
	if cfg.MaxBudgetTokens > 0 && tokens >= cfg.MaxBudgetTokens {
		reasons = append(reasons, fmt.Sprintf("%d tokens >= cap %d", tokens, cfg.MaxBudgetTokens))
	}
	if cfg.MaxBudgetUSD > 0 && u.TotalCostUSD >= cfg.MaxBudgetUSD {
		reasons = append(reasons, fmt.Sprintf("$%.4f >= cap $%.4f", u.TotalCostUSD, cfg.MaxBudgetUSD))
	}
	if len(reasons) == 0 {
		return false, ""
	}
	return true, strings.Join(reasons, "; ")
}
