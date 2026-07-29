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

// stepEvent decodes the fields of one opencode NDJSON line that this file
// cares about: a step_start's timestamp (the run's start-of-turn marker) or a
// step_finish's timestamp, per-turn token/cost tallies, and cache figures.
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

// breakdownByModelFile scans the file at path and returns per-model token
// breakdowns, split into the five billable categories, by parsing
// step_finish events.
//
// Each step_finish in opencode's NDJSON stream is one API call's own
// per-call usage — like claude-code's per-message usage, not a cumulative
// running total — so aggregation here is a SUM across every DISTINCT
// part.messageID, keyed by the exact part.modelID. opencode can re-emit
// multiple lines for one message's part (mirroring claude-code's
// multi-content-block re-emit), each carrying the SAME messageID; summing
// every such line would double-count that one call's usage. The first
// occurrence of a non-empty messageID wins and every later line sharing
// that id is skipped; a line with an empty messageID is always counted,
// since there is nothing to dedup it against. Reasoning tokens fold into
// OutputTokens, matching FinalSnapshot's aggregate. opencode's tokens.cache
// carries a single collapsed write total with no TTL split (unlike
// claude-code's ephemeral_5m/1h split), so the whole write total is
// attributed to the 5-minute bucket — the Anthropic-backed default TTL is
// 5m, mirroring claude's own collapsed-total fallback rule.
//
// Returns (nil, nil) when the file does not exist.
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
			model = "unknown"
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

	// Deterministic order: sort by exact model id ascending. opencode
	// models are not claude families, so there is no family-rank pass —
	// "unknown" sorts by its literal string like any other id.
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

// breakdownByModel indirects to breakdownByModelFile so tests can simulate a
// breakdownByModelFile I/O error without a real filesystem race between it
// and the step_finish aggregation scan.
var breakdownByModel = breakdownByModelFile

// ExtractUsage scans logPath — an opencode NDJSON run log — and sums the
// per-turn token/cost tallies carried by every step_finish event into one
// aggregate usage.Report.
//
// Each step_finish is an independent per-turn tally, not a cumulative
// snapshot (unlike claude-code's single result event), so aggregation here
// is a plain sum over every step_finish line — no dedup or "last one wins"
// logic is needed. Reasoning tokens are folded into OutputTokens since
// opencode reports them as a separate token class but spindrift's
// usage.Usage has no distinct reasoning field. DurationMs is wall-clock: the
// last step_finish timestamp minus the first step_start timestamp seen in
// the log. DurationApiMs is left zero — opencode's NDJSON stream carries no
// separate api-only timing. usage.Report.SummedByModel is populated
// separately by breakdownByModel, which sums per-call usage across distinct
// messageIDs (dedup collapses multi-part re-emits of one message) keyed by
// exact modelID; opencode's single tokens.cache.write total maps to the
// 5-minute cache-write bucket since opencode reports no TTL split. A
// breakdownByModel I/O error degrades only the per-model section (Models is
// set to nil with a stderr warning), not the aggregate FinalSnapshot already
// summed above.
//
// Returns usage.Report{Found: false} when the log contains no step_finish
// event or does not exist. Returns (usage.Report{}, err) on other I/O
// errors.
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

	// Only report wall-clock when a step_start anchored the window; a
	// step_finish with no preceding step_start would otherwise subtract a
	// zero firstStart and yield a raw epoch-ms figure.
	if haveStart {
		u.DurationMs = lastFinish - firstStart
	}

	// A breakdownByModel I/O error degrades the per-model section, not the
	// aggregate totals already parsed above — mirrors claude's ExtractUsage
	// (issue #674).
	models, err := breakdownByModel(logPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: breakdown by model failed for %s: %v\n", logPath, err)
		models = nil
	}
	return usage.Report{FinalSnapshot: u, Found: true, SummedByModel: models}, nil
}
