package settle

import (
	"fmt"
	"strings"

	"spindrift.dev/launcher/internal/usage"
)

// budgetExceeded reports whether usage u has reached either of Config's budget
// caps, naming the cap or caps that tripped. A zero cap means no cap on that
// dimension (issue #2001), matching MaxFixAttempts' 0-disables convention. The
// two dimensions are independent, so either can trip first.
func budgetExceeded(cfg Config, u usage.Usage) (bool, string) {
	tokens := u.TotalTokens()
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
