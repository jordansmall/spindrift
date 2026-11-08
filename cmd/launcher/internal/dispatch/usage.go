package dispatch

import (
	"fmt"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/usage"
)

// UsageReport returns the Markdown usage-summary comment body for this issue's
// run. It aggregates every attempt log: each pass, and each rotated-aside retry
// within a pass (issues #561, #2575). A caller that never called Run() must call
// EnsureRunLineage first. When no attempt log produced a result event, the body
// says usage is unavailable rather than erroring. A resumed session's cumulative
// result figures are rebased across logs, so each log books only its increment.
func (d *Dispatch) UsageReport() string {
	resolve := d.cfg.ResolveEnv
	if resolve == nil {
		resolve = func(_, name string) string { return os.Getenv(name) }
	}
	model := resolve(d.number, "MODEL")
	if model == "" {
		model = "unknown"
	}

	found := d.attemptReports()
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

// CumulativeUsage sums every usage field across every attempt log this run has
// produced, retried attempts included, so selfHealGate's budget gate reads the
// run's true total spend (issues #561, #2001, #2575). Its DurationMs is the total
// time across attempts, not the wall-clock span aggregatedReport derives.
// internal/butler/settle.go writes this sum whole into the ledger's State.Usage,
// so turns and durations are summed too, not just tokens and cost. A caller
// that never called Run() must call EnsureRunLineage first. An attempt log that
// fails to parse, or has no result event, contributes nothing rather than
// aborting the sum. A resumed session's cumulative result figures are rebased
// across logs, so each log books only its increment.
func (d *Dispatch) CumulativeUsage() usage.Usage {
	var total usage.Usage
	for _, r := range d.attemptReports() {
		total = total.Add(r.Totals)
	}
	return total
}

// attemptReports returns the Found report of every attempt log, chronologically.
// The session baselines thread from log to log, so a session resumed in a later
// log (the implement -> fix resume) books only its own increment there. Work
// retries resume the session (--resume); research and butler retries re-pin the
// same --session-id in initial mode. Threading is still right there: within a Run
// the transcript is cached so the counters continue, and a genuine restart is
// caught by the counter-decrease rule. So the chain never resets within a Run
// (prior-Run logs are quarantined and never listed). A log that errors keeps the
// chain's previous baselines. Keep in step with dispatchrecord.sessionSeed.
func (d *Dispatch) attemptReports() []usage.Report {
	var found []usage.Report
	var base driverkit.SessionBaselines
	for _, pl := range AllAttemptLogPaths(d.pwd, d.number) {
		r, after, err := d.driver.ExtractUsageAfter(pl.Path, base)
		if err != nil {
			continue
		}
		base = after
		if r.Found {
			found = append(found, r)
		}
	}
	return found
}

// aggregatedReport merges a run's per-attempt reports, each of which must have Found
// set, into one for UsageReport (issue #2575). Every total sums except DurationMs,
// which spanDurationMs derives; SummedByModel merges by exact model id in
// first-appearance order, so the result never depends on a driver's own sort; a row is
// OutputUnavailable if any attempt's was, since a partial output sum would understate it. A lone
// report returns unchanged, keeping a one-log run's body byte-for-byte as before.
func aggregatedReport(found []usage.Report) usage.Report {
	if len(found) == 1 {
		return found[0]
	}

	var total usage.Usage
	var models []usage.ModelUsage
	modelIndex := make(map[string]int)

	for _, r := range found {
		total = total.Add(r.Totals)

		for _, m := range r.SummedByModel {
			if i, ok := modelIndex[m.Model]; ok {
				models[i].UncachedInputTokens += m.UncachedInputTokens
				models[i].OutputUnavailable = models[i].OutputUnavailable || m.OutputUnavailable
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

	for i := range models {
		if models[i].OutputUnavailable {
			models[i].MarkOutputUnavailable()
		}
	}
	total.DurationMs = spanDurationMs(found)

	return usage.Report{Totals: total, Found: true, SummedByModel: models}
}

// spanDurationMs returns the wall-time span from the earliest to the latest event
// across found, floored to the largest single report's own DurationMs: timestamped
// log lines miss startup, network and render time, so a span narrower than a report
// that provably ran that long must never win. With no usable span anywhere, the
// floor alone stands. Call it only when found holds more than one report.
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

func modelBreakdownSection(models []usage.ModelUsage) string {
	if len(models) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n\n### Per-model token usage\n\n")
	sb.WriteString("| Model | Uncached input | Output | Cache read | Cache write (5m) | Cache write (1h) |\n")
	sb.WriteString("| --- | --- | --- | --- | --- | --- |\n")
	for _, m := range models {
		output := fmt.Sprint(m.OutputTokens)
		if m.OutputUnavailable {
			output = "n/a"
		}
		fmt.Fprintf(&sb, "| %s | %d | %s | %d | %d | %d |\n",
			m.Model, m.UncachedInputTokens, output,
			m.CacheReadInputTokens, m.CacheWrite5mTokens, m.CacheWrite1hTokens)
	}
	return sb.String()
}
