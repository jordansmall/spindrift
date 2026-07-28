package dispatch

import (
	"fmt"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/usage"
)

// UsageReport returns the Markdown usage-summary comment body for this
// issue's initial run, reading the log Run wrote through the Driver's
// ExtractUsage. If no result event is found the body notes that usage is
// unavailable rather than erroring.
func (d *Dispatch) UsageReport() string {
	resolve := d.cfg.ResolveEnv
	if resolve == nil {
		resolve = func(_, name string) string { return os.Getenv(name) }
	}
	model := resolve(d.number, "MODEL")
	if model == "" {
		model = "unknown"
	}
	r, err := d.driver.ExtractUsage(d.logPath())
	if err != nil || !r.Found {
		return fmt.Sprintf("## Run usage\n\nModel: `%s`\n\nUsage data unavailable (no result event in log).", model)
	}
	body := fmt.Sprintf(
		"## Run usage\n\n"+
			"| Field | Value |\n"+
			"| --- | --- |\n"+
			"| Model | `%s` |\n"+
			"| Cost | $%.4f |\n"+
			"| Input tokens | %d |\n"+
			"| Output tokens | %d |\n"+
			"| Cache read tokens | %d |\n"+
			"| Cache creation tokens | %d |\n"+
			"| Wall time | %s |\n"+
			"| API time | %s |\n"+
			"| Turns | %d |",
		model,
		r.TotalCostUSD,
		r.InputTokens,
		r.OutputTokens,
		r.CacheReadInputTokens,
		r.CacheCreationInputTokens,
		usage.FormatDuration(r.DurationMs),
		usage.FormatDuration(r.DurationApiMs),
		r.NumTurns,
	)
	body += breakdownSection(r.Roles)
	body += modelBreakdownSection(r.Models)
	return body
}

// CumulativeUsage sums token and cost usage across every pass log this
// issue's Dispatch has produced so far — the initial run, each fix pass,
// and (via LogPaths) a conflict-resolve pass if one ran — so selfHealGate's
// budget gate (issue #2001) reads the run's total spend, not just its
// initial pass. A pass log that fails to parse, or has no result event,
// contributes nothing rather than aborting the sum, matching ExtractUsage's
// own best-effort degrade — the same as a log LogPaths omits outright
// because it was rotated aside (issue #561): the sum simply undercounts
// rather than erroring, acceptable for a best-effort spend governor.
func (d *Dispatch) CumulativeUsage() usage.Usage {
	var total usage.Usage
	for _, pl := range LogPaths(d.pwd, d.number) {
		r, err := d.driver.ExtractUsage(pl.Path)
		if err != nil || !r.Found {
			continue
		}
		total.InputTokens += r.InputTokens
		total.OutputTokens += r.OutputTokens
		total.CacheReadInputTokens += r.CacheReadInputTokens
		total.CacheCreationInputTokens += r.CacheCreationInputTokens
		total.TotalCostUSD += r.TotalCostUSD
	}
	return total
}

// breakdownSection returns a Markdown per-role breakdown section, or empty
// string if roles is empty.
func breakdownSection(roles []usage.RoleUsage) string {
	if len(roles) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n\n### Per-role breakdown\n\n")
	sb.WriteString("| Role | Input tokens | Output tokens | Cache read | Cache creation |\n")
	sb.WriteString("| --- | --- | --- | --- | --- |\n")
	for _, r := range roles {
		fmt.Fprintf(&sb, "| %s | %d | %d | %d | %d |\n",
			r.Role, r.InputTokens, r.OutputTokens,
			r.CacheReadInputTokens, r.CacheCreationInputTokens)
	}
	return sb.String()
}

// modelBreakdownSection returns a Markdown per-model token breakdown
// section, or empty string if models is empty.
func modelBreakdownSection(models []usage.ModelUsage) string {
	if len(models) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n\n### Per-model token usage\n\n")
	sb.WriteString("| Model | Uncached input | Output | Cache read | Cache write (5m) | Cache write (1h) |\n")
	sb.WriteString("| --- | --- | --- | --- | --- | --- |\n")
	for _, m := range models {
		fmt.Fprintf(&sb, "| %s | %d | %d | %d | %d | %d |\n",
			m.Model, m.UncachedInputTokens, m.OutputTokens,
			m.CacheReadInputTokens, m.CacheWrite5mTokens, m.CacheWrite1hTokens)
	}
	return sb.String()
}
