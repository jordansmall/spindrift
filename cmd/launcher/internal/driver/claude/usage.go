package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"spindrift.dev/launcher/internal/logscan"
	"spindrift.dev/launcher/internal/usage"
)

type resultEvent struct {
	Type          string    `json:"type"`
	NumTurns      int       `json:"num_turns"`
	TotalCostUSD  float64   `json:"total_cost_usd"`
	DurationMs    int64     `json:"duration_ms"`
	DurationApiMs int64     `json:"duration_api_ms"`
	UsageData     usageData `json:"usage"`
}

type usageData struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// lastInLog scans the file at path and returns the last result event parsed
// as a usage.Usage. Lines larger than the 4 MiB scan buffer are skipped
// rather than aborting the scan; the last result event wins.
//
// Returns (usage.Usage{}, false, nil) when no result event is present or the
// file does not exist. Returns (usage.Usage{}, false, err) on I/O errors
// other than file-not-found or oversized lines.
func lastInLog(path string) (usage.Usage, bool, error) {
	var last *usage.Usage
	err := logscan.ForEachLine(path, logscan.SkipOversized, func(line string) {
		s := strings.TrimSpace(line)
		if strings.Contains(s, `"type":"result"`) {
			var ev resultEvent
			if jsonErr := json.Unmarshal([]byte(s), &ev); jsonErr == nil && ev.Type == "result" {
				u := usage.Usage{
					InputTokens:              ev.UsageData.InputTokens,
					OutputTokens:             ev.UsageData.OutputTokens,
					CacheReadInputTokens:     ev.UsageData.CacheReadInputTokens,
					CacheCreationInputTokens: ev.UsageData.CacheCreationInputTokens,
					TotalCostUSD:             ev.TotalCostUSD,
					DurationMs:               ev.DurationMs,
					DurationApiMs:            ev.DurationApiMs,
					NumTurns:                 ev.NumTurns,
				}
				last = &u
			}
		}
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return usage.Usage{}, false, nil
		}
		return usage.Usage{}, false, err
	}

	if last == nil {
		return usage.Usage{}, false, nil
	}
	return *last, true, nil
}

// assistantEvent decodes line as a claude-code stream-json assistant message
// event, returning the parsed Event and true only when line is an assistant
// event carrying a non-nil Message. It is the shared decode preamble of
// breakdownByModelFile's usage pass.
func assistantEvent(line string) (Event, bool) {
	if !strings.Contains(line, `"type":"assistant"`) {
		return Event{}, false
	}
	var ev Event
	if err := json.Unmarshal([]byte(line), &ev); err != nil || ev.Type != "assistant" {
		return Event{}, false
	}
	if ev.Message == nil {
		return Event{}, false
	}
	return ev, true
}

// breakdownByModelFile scans the file at path and returns per-model-family
// token breakdowns, split into the five billable categories, by parsing
// assistant message events.
//
// Per-message usage in claude-code stream-json is PER-CALL: each assistant
// event is one API response, so its input_tokens is already that call's
// uncached input, and its cache_read/cache_creation figures are that call's
// own cache tokens — none of it is cumulative across a turn or a run.
// Aggregation is therefore a SUM across every DISTINCT message.id, across
// every turn and every subagent, keyed by ModelFamily(message.model) — not a
// sum over every event. A multi-content-block assistant message is
// re-emitted by claude-code once per content block, each line carrying the
// SAME message.id and byte-identical usage; summing every such line would
// double- or triple-count that one call's usage. The first occurrence of a
// non-empty message.id wins and every later line sharing that id is
// skipped; a line with an empty or missing id (older stream-json, or any
// shape that doesn't carry the field) is always counted, since there is
// nothing to dedup it against. This is deliberately not a read of the
// result event's own "usage" header: that header is a non-cumulative
// snapshot of only its own call and does not reconcile against a sum over
// the transcript. The per-call vs cumulative determination is settled by
// evidence — the Messages API per-request usage contract and the real #2078
// dispatch figures — in TestBreakdownByModel_Fixture; the ~9x #2078
// discrepancy is the header snapshot vs the transcript sum, not a summing
// bug.
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
		ev, ok := assistantEvent(line)
		if !ok {
			return
		}
		if id := ev.Message.ID; id != "" {
			if seenIDs[id] {
				return
			}
			seenIDs[id] = true
		}
		family := ModelFamily(ev.Message.Model)
		if family == "" {
			family = "unknown"
		}
		b := ensure(family)
		b.UncachedInputTokens += ev.Message.Usage.InputTokens
		b.OutputTokens += ev.Message.Usage.OutputTokens
		b.CacheReadInputTokens += ev.Message.Usage.CacheReadInputTokens
		if cc := ev.Message.Usage.CacheCreation; cc != nil {
			b.CacheWrite5mTokens += cc.Ephemeral5mInputTokens
			b.CacheWrite1hTokens += cc.Ephemeral1hInputTokens
		} else if v := ev.Message.Usage.CacheCreationInputTokens; v > 0 {
			// Pre-TTL-split stream-json log: the nested cache_creation
			// object is absent, but the flat cache_creation_input_tokens
			// total is populated. Attribute it to the 5-minute bucket,
			// since the Messages API's cache_control default TTL is 5m
			// (ephemeral 1h caching is opt-in), so an un-split total is
			// overwhelmingly likely to be all-5m rather than all-1h.
			b.CacheWrite5mTokens += v
		}
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	// Deterministic order: opus, haiku, sonnet first (when present), then
	// any remaining families (including "unknown") sorted for stability.
	priority := []string{"opus", "haiku", "sonnet"}
	seen := make(map[string]bool, len(priority))
	var result []usage.ModelUsage
	for _, model := range priority {
		if b, ok := buckets[model]; ok {
			result = append(result, *b)
			seen[model] = true
		}
	}
	var rest []string
	for model := range buckets {
		if !seen[model] {
			rest = append(rest, model)
		}
	}
	sort.Strings(rest)
	for _, model := range rest {
		result = append(result, *buckets[model])
	}
	return result, nil
}

// breakdownByModel indirects to breakdownByModelFile so tests can simulate a
// breakdownByModelFile I/O error without a real filesystem race between it
// and the lastInLog scan.
var breakdownByModel = breakdownByModelFile

// ExtractUsage scans logPath for its result event and, separately, its
// per-model breakdown, returning both in one usage.Report — the claude
// Driver's implementation of the Driver interface's ExtractUsage method.
func ExtractUsage(logPath string) (usage.Report, error) {
	u, found, err := lastInLog(logPath)
	if err != nil {
		return usage.Report{}, err
	}
	if !found {
		return usage.Report{}, nil
	}
	// A breakdownByModel I/O error degrades the per-model section, not the
	// aggregate totals already parsed above — see issue #674.
	models, err := breakdownByModel(logPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: breakdown by model failed for %s: %v\n", logPath, err)
		models = nil
	}
	return usage.Report{Usage: u, Found: true, Models: models}, nil
}
