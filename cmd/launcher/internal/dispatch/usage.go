package dispatch

import (
	"fmt"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/usage"
)

// UsageReport returns the Markdown usage-summary comment body for this
// issue's run, aggregating across EVERY attempt log AllAttemptLogPaths
// returns -- every pass (initial, each fix pass, conflict-resolve) and,
// within each pass, every rotated-aside retry attempt (issue #561) -- not
// just the initial pass's own current log (issue #2575). If no attempt log
// produced a result event, the body notes that usage is unavailable rather
// than erroring.
func (d *Dispatch) UsageReport() string {
	resolve := d.cfg.ResolveEnv
	if resolve == nil {
		resolve = func(_, name string) string { return os.Getenv(name) }
	}
	model := resolve(d.number, "MODEL")
	if model == "" {
		model = "unknown"
	}

	var found []usage.Report
	for _, pl := range AllAttemptLogPaths(d.pwd, d.number) {
		r, err := d.driver.ExtractUsage(pl.Path)
		if err != nil || !r.Found {
			continue
		}
		found = append(found, r)
	}
	if len(found) == 0 {
		return fmt.Sprintf("## Run usage\n\nModel: `%s`\n\nUsage data unavailable (no result event in log).", model)
	}
	r := aggregatedReport(found)
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

// aggregatedReport merges every found usage.Report across a run's attempt
// logs into one combined usage.Report for UsageReport, applying issue
// #2575's cross-log aggregation rules: InputTokens, OutputTokens,
// CacheReadInputTokens, CacheCreationInputTokens, TotalCostUSD,
// DurationApiMs, and NumTurns all sum across every found report;
// DurationMs (wall time) does not sum -- see spanDurationMs; SummedByModel
// buckets merge by exact model id, summed, preserving each id's
// first-appearance order walking found in AllAttemptLogPaths order. found
// must contain only reports with Found == true.
//
// The single-report case (the common case -- one pass, no retries) returns
// found[0] completely unchanged, bypassing every rule above: this is the
// hard backward-compatibility requirement that keeps a single-log run's
// report byte-for-byte identical to what it reported before this
// aggregation existed.
func aggregatedReport(found []usage.Report) usage.Report {
	if len(found) == 1 {
		return found[0]
	}

	var total usage.Usage
	var models []usage.ModelUsage
	modelIndex := make(map[string]int)

	for _, r := range found {
		total.InputTokens += r.Totals.InputTokens
		total.OutputTokens += r.Totals.OutputTokens
		total.CacheReadInputTokens += r.Totals.CacheReadInputTokens
		total.CacheCreationInputTokens += r.Totals.CacheCreationInputTokens
		total.TotalCostUSD += r.Totals.TotalCostUSD
		total.DurationApiMs += r.Totals.DurationApiMs
		total.NumTurns += r.Totals.NumTurns

		for _, m := range r.SummedByModel {
			if i, ok := modelIndex[m.Model]; ok {
				models[i].UncachedInputTokens += m.UncachedInputTokens
				models[i].OutputTokens += m.OutputTokens
				models[i].CacheReadInputTokens += m.CacheReadInputTokens
				models[i].CacheWrite5mTokens += m.CacheWrite5mTokens
				models[i].CacheWrite1hTokens += m.CacheWrite1hTokens
				continue
			}
			modelIndex[m.Model] = len(models)
			models = append(models, m)
		}
	}

	total.DurationMs = spanDurationMs(found)

	return usage.Report{Totals: total, Found: true, SummedByModel: models}
}

// spanDurationMs derives the combined wall-time span across every report in
// found, generalizing claude driver's sumInLog floor-to-longest-session rule
// (see its doc comment) from sessions within one log to logs within a run:
// the span between the earliest EarliestEventMs and the latest
// LatestEventMs among reports with HasEventSpan (0 if fewer than one report
// has a usable span, or the max isn't after the min), floored to the
// largest single found report's own Totals.DurationMs -- so a span narrower
// than a report that provably ran that long (its own timestamped lines
// don't capture the full wall time: startup, network, render) is never
// reported. Callers with len(found) <= 1 should use aggregatedReport's own
// single-report bypass instead of calling this directly.
func spanDurationMs(found []usage.Report) int64 {
	var earliestMs, latestMs int64
	haveSpan := false
	var maxOwnMs int64

	for _, r := range found {
		if r.Totals.DurationMs > maxOwnMs {
			maxOwnMs = r.Totals.DurationMs
		}
		if !r.HasEventSpan {
			continue
		}
		if !haveSpan || r.EarliestEventMs < earliestMs {
			earliestMs = r.EarliestEventMs
		}
		if !haveSpan || r.LatestEventMs > latestMs {
			latestMs = r.LatestEventMs
		}
		haveSpan = true
	}

	var spanMs int64
	if haveSpan && latestMs > earliestMs {
		spanMs = latestMs - earliestMs
	}
	if maxOwnMs > spanMs {
		return maxOwnMs
	}
	return spanMs
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
