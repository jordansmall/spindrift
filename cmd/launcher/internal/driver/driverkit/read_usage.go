package driverkit

import (
	"fmt"
	"os"
	"sort"

	"spindrift.dev/launcher/internal/usage"
)

// UsageByModel returns per-model breakdowns from the KindUsage events in the
// log at path, or (nil, nil) when the log is absent. Each event is one call's
// own usage, not a running total, so these sum; a Driver whose CLI re-emits
// lines sharing a MessageID gets the first occurrence only (an empty
// MessageID is never deduplicated). A usage event naming no model lands under
// usage.UnknownModel. Models are sorted by id; there is no family-rank pass
// because the ids need not be a ranked model family.
func UsageByModel(path string, newDecoder func() Decoder) ([]usage.ModelUsage, error) {
	buckets := make(map[string]*usage.ModelUsage)
	seenIDs := make(map[string]bool)
	err := scanEvents(path, newDecoder, func(ev Event) {
		if ev.Kind != KindUsage {
			return
		}
		if id := ev.MessageID; id != "" {
			if seenIDs[id] {
				return
			}
			seenIDs[id] = true
		}
		model := ev.Model
		if model == "" {
			model = usage.UnknownModel
		}
		b, ok := buckets[model]
		if !ok {
			b = &usage.ModelUsage{Model: model}
			buckets[model] = b
		}
		b.UncachedInputTokens += ev.Tokens.Input
		b.OutputTokens += ev.Tokens.Output
		b.CacheReadInputTokens += ev.Tokens.CacheRead
		b.CacheWrite5mTokens += ev.Tokens.CacheWrite5m
		b.CacheWrite1hTokens += ev.Tokens.CacheWrite1h
	})
	if err != nil {
		return nil, err
	}
	models := make([]string, 0, len(buckets))
	for model := range buckets {
		models = append(models, model)
	}
	sort.Strings(models)
	var result []usage.ModelUsage
	for _, model := range models {
		result = append(result, *buckets[model])
	}
	return result, nil
}

// ExtractUsage sums the tallies of every KindUsage event in the log at logPath
// into one usage.Report. Each event is an independent per-call tally, not a
// cumulative snapshot, so a plain sum is correct, and every event counts as
// one turn with no MessageID deduplication (only the per-model breakdown
// dedups). DurationApiMs stays zero. The first KindStart's Timestamp anchors
// the event span; without one the span and DurationMs are left unset rather
// than reporting a raw epoch figure. Found is false when the log has no usage
// event or is absent. SummedByAgent is not populated.
//
// byModel supplies the per-model breakdown, normally UsageByModel over the
// same decoder; a Driver passes its own so a test can fail it. Its error degrades only that section to nil with a stderr
// WARNING naming the path, not the totals already summed (issue #674).
func ExtractUsage(logPath string, newDecoder func() Decoder, byModel func(path string) ([]usage.ModelUsage, error)) (usage.Report, error) {
	var u usage.Usage
	var firstStart, lastUsage int64
	haveStart := false
	err := scanEvents(logPath, newDecoder, func(ev Event) {
		switch ev.Kind {
		case KindStart:
			if !haveStart {
				firstStart = ev.Timestamp
				haveStart = true
			}
		case KindUsage:
			u.InputTokens += ev.Tokens.Input
			u.OutputTokens += ev.Tokens.Output
			u.CacheReadInputTokens += ev.Tokens.CacheRead
			u.CacheCreationInputTokens += ev.Tokens.CacheWrite5m + ev.Tokens.CacheWrite1h
			u.TotalCostUSD += ev.Tokens.CostUSD
			u.NumTurns++
			lastUsage = ev.Timestamp
		}
	})
	if err != nil {
		return usage.Report{}, err
	}
	if u.NumTurns == 0 {
		return usage.Report{Found: false}, nil
	}
	if haveStart {
		u.DurationMs = lastUsage - firstStart
	}
	models, err := byModel(logPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: breakdown by model failed for %s: %v\n", logPath, err)
		models = nil
	}
	report := usage.Report{Totals: u, Found: true, SummedByModel: models}
	if haveStart {
		report.EarliestEventMs = firstStart
		report.LatestEventMs = lastUsage
		report.HasEventSpan = true
	}
	return report, nil
}
