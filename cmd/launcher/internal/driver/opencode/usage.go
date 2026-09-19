package opencode

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/logscan"
	"spindrift.dev/launcher/internal/usage"
)

// stepEvent decodes only the opencode NDJSON fields this file reads.
type stepEvent struct {
	Type      string   `json:"type"`
	Timestamp int64    `json:"timestamp"`
	Part      stepPart `json:"part"`
}

type stepPart struct {
	MessageID string     `json:"messageID"`
	ModelID   string     `json:"modelID"`
	Tokens    stepTokens `json:"tokens"`
	Cost      float64    `json:"cost"`
}

type stepTokens struct {
	Input     int            `json:"input"`
	Output    int            `json:"output"`
	Reasoning int            `json:"reasoning"`
	Cache     stepTokenCache `json:"cache"`
}

type stepTokenCache struct {
	Write int `json:"write"`
	Read  int `json:"read"`
}

// breakdownByModelFile returns per-model breakdowns from the step_finish events
// in the file at path, or (nil, nil) when the file is absent. Each event is one
// call's own usage, not a running total, so these sum; opencode re-emits lines
// sharing a messageID, so the first occurrence wins. tokens.cache.write has no
// TTL split, so it all lands in the 5m bucket, the Anthropic default TTL.
func breakdownByModelFile(path string) ([]usage.ModelUsage, error) {
	buckets := make(map[string]*usage.ModelUsage)
	ensure := func(model string) *usage.ModelUsage {
		if b, ok := buckets[model]; ok {
			return b
		}
		b := &usage.ModelUsage{Model: model}
		buckets[model] = b
		return b
	}
	seenIDs := make(map[string]bool)

	err := logscan.ForEachLine(path, logscan.SkipOversized, func(line string) {
		s := strings.TrimSpace(line)
		if s == "" {
			return
		}
		var ev stepEvent
		if err := json.Unmarshal([]byte(s), &ev); err != nil || ev.Type != "step_finish" {
			return
		}
		if id := ev.Part.MessageID; id != "" {
			if seenIDs[id] {
				return
			}
			seenIDs[id] = true
		}
		model := ev.Part.ModelID
		if model == "" {
			model = usage.UnknownModel
		}
		b := ensure(model)
		b.UncachedInputTokens += ev.Part.Tokens.Input
		b.OutputTokens += ev.Part.Tokens.Output + ev.Part.Tokens.Reasoning
		b.CacheReadInputTokens += ev.Part.Tokens.Cache.Read
		b.CacheWrite5mTokens += ev.Part.Tokens.Cache.Write
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	// opencode models are not claude families, so there is no family-rank
	// pass; "unknown" sorts by its literal string like any other id.
	var models []string
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

// breakdownByModel indirects so tests can simulate an I/O error without a real
// filesystem race against the step_finish aggregation scan.
var breakdownByModel = breakdownByModelFile

// ExtractUsage sums the per-turn tallies of every step_finish in the opencode
// NDJSON log at logPath into one usage.Report. Each step_finish is an
// independent per-turn tally, not a cumulative snapshot, so a plain sum is
// correct here. DurationApiMs stays zero because opencode logs no api-only
// timing. Found is false when the log has no step_finish event or is absent.
func ExtractUsage(logPath string) (usage.Report, error) {
	var u usage.Usage
	var firstStart, lastFinish int64
	haveStart := false

	err := driverkit.ScanLog(logPath, logscan.SkipOversized, func(line string) {
		s := strings.TrimSpace(line)
		if s == "" {
			return
		}
		var ev stepEvent
		if err := json.Unmarshal([]byte(s), &ev); err != nil {
			return
		}
		switch ev.Type {
		case "step_start":
			if !haveStart {
				firstStart = ev.Timestamp
				haveStart = true
			}
		case "step_finish":
			u.InputTokens += ev.Part.Tokens.Input
			u.OutputTokens += ev.Part.Tokens.Output + ev.Part.Tokens.Reasoning
			u.CacheReadInputTokens += ev.Part.Tokens.Cache.Read
			u.CacheCreationInputTokens += ev.Part.Tokens.Cache.Write
			u.TotalCostUSD += ev.Part.Cost
			u.NumTurns++
			lastFinish = ev.Timestamp
		}
	})
	if err != nil {
		return usage.Report{}, err
	}

	if u.NumTurns == 0 {
		return usage.Report{Found: false}, nil
	}

	// Without a step_start to anchor the window, subtracting a zero firstStart
	// would report a raw epoch-ms figure as the duration.
	if haveStart {
		u.DurationMs = lastFinish - firstStart
	}

	// An I/O error here degrades only the per-model section, not the totals
	// already parsed above, mirroring claude's ExtractUsage (issue #674).
	models, err := breakdownByModel(logPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: breakdown by model failed for %s: %v\n", logPath, err)
		models = nil
	}
	report := usage.Report{Totals: u, Found: true, SummedByModel: models}
	// Same guard as DurationMs: the event span means nothing unless a
	// step_start anchored the window.
	if haveStart {
		report.EarliestEventMs = firstStart
		report.LatestEventMs = lastFinish
		report.HasEventSpan = true
	}
	return report, nil
}
