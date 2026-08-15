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
			"| Wall time | %s |\n"+
			"| API time | %s |\n"+
			"| Turns | %d |",
		model,
		usage.FormatDuration(r.Totals.DurationMs),
		usage.FormatDuration(r.Totals.DurationApiMs),
		r.Totals.NumTurns,
	)
	body += modelBreakdownSection(r.SummedByModel)
	return body
}

// CumulativeUsage sums token and cost usage across every attempt log this
// issue's Dispatch has produced so far — the initial run, each fix pass,
// and a conflict-resolve pass if one ran, including any attempt a hold or
// transient-backoff retry rotated aside (issue #561) — via
// AllAttemptLogPaths, so selfHealGate's budget gate (issue #2001) reads the
// run's true total spend, including a retried attempt's, not just the
// spend of whichever attempt is current (issue #2575). An attempt log that
// fails to parse, or has no result event, contributes nothing rather than
// aborting the sum, matching ExtractUsage's own best-effort degrade —
// acceptable for a best-effort spend governor.
func (d *Dispatch) CumulativeUsage() usage.Usage {
	var total usage.Usage
	for _, pl := range AllAttemptLogPaths(d.pwd, d.number) {
		r, err := d.driver.ExtractUsage(pl.Path)
		if err != nil || !r.Found {
			continue
		}
		total.InputTokens += r.Totals.InputTokens
		total.OutputTokens += r.Totals.OutputTokens
		total.CacheReadInputTokens += r.Totals.CacheReadInputTokens
		total.CacheCreationInputTokens += r.Totals.CacheCreationInputTokens
		total.TotalCostUSD += r.Totals.TotalCostUSD
	}
	return total
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
