package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/driver/driverkit"
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

// timestampedEvent decodes the top-level "type" and "timestamp" fields
// carried by real claude-code stream-json lines (assistant/user/system
// events, timestamp RFC3339 with milliseconds, e.g.
// "2026-08-11T19:01:33.187Z"). Used to derive wall-clock span across every
// session in a log, since sessions can run concurrently (issue #2058) and
// result events' own duration_ms is not additive. Type is required non-empty
// so a line that merely contains the substring "timestamp" somewhere in
// nested, non-driver content (e.g. a tool_result dump) can't widen the span.
type timestampedEvent struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
}

// sumInLog scans the file at path and returns a usage.Usage aggregated
// across every "type":"result" event in the log. Lines larger than the 4
// MiB scan buffer are skipped rather than aborting the scan.
//
// Orchestrator mode invokes the claude-code driver repeatedly, so a single
// Box log can hold several distinct sessions, each emitting exactly one
// result event — summing every result event in the log therefore sums
// every session, with no session-boundary detection required. InputTokens,
// OutputTokens, CacheReadInputTokens, CacheCreationInputTokens,
// TotalCostUSD, DurationApiMs, and NumTurns are all additive this way.
//
// DurationMs (wall time) is NOT additive when a log holds more than one
// session: concurrent sessions would overstate wall time if each session's
// own duration_ms were summed. For that multi-session case, it is instead
// derived from the span between the earliest and latest top-level
// "timestamp" field seen across every driver session line (assistant/user/
// system events; a line without a "type" field is not one, so it can't
// widen the span). A single-session log (at most one result event) always
// reports that session's own duration_ms directly, timestamps or not —
// never the span — matching output from before this aggregation change:
// the assistant/user timestamps bracketing a session don't capture the
// process's full wall time (startup, network, render), so the span
// systematically understates it, and a log with only one timestamped line
// would otherwise collapse to a 0s span.
//
// Returns (usage.Usage{}, false, nil) when no result event is present or the
// file does not exist. Returns (usage.Usage{}, false, err) on I/O errors
// other than file-not-found or oversized lines.
func sumInLog(path string) (usage.Usage, bool, error) {
	var sum usage.Usage
	resultCount := 0
	var durationMsSum int64
	var earliest, latest time.Time
	haveTimestamp := false

	err := driverkit.ScanLog(path, logscan.SkipOversized, func(line string) {
		s := strings.TrimSpace(line)

		if strings.Contains(s, `"timestamp"`) {
			var ts timestampedEvent
			if jsonErr := json.Unmarshal([]byte(s), &ts); jsonErr == nil && ts.Type != "" && ts.Timestamp != "" {
				if t, parseErr := time.Parse(time.RFC3339Nano, ts.Timestamp); parseErr == nil {
					if !haveTimestamp || t.Before(earliest) {
						earliest = t
					}
					if !haveTimestamp || t.After(latest) {
						latest = t
					}
					haveTimestamp = true
				}
			}
		}

		if strings.Contains(s, `"type":"result"`) {
			var ev resultEvent
			if jsonErr := json.Unmarshal([]byte(s), &ev); jsonErr == nil && ev.Type == "result" {
				resultCount++
				sum.InputTokens += ev.UsageData.InputTokens
				sum.OutputTokens += ev.UsageData.OutputTokens
				sum.CacheReadInputTokens += ev.UsageData.CacheReadInputTokens
				sum.CacheCreationInputTokens += ev.UsageData.CacheCreationInputTokens
				sum.TotalCostUSD += ev.TotalCostUSD
				sum.DurationApiMs += ev.DurationApiMs
				sum.NumTurns += ev.NumTurns
				durationMsSum += ev.DurationMs
			}
		}
	})
	if err != nil {
		return usage.Usage{}, false, err
	}

	if resultCount == 0 {
		return usage.Usage{}, false, nil
	}

	if haveTimestamp && resultCount > 1 {
		sum.DurationMs = latest.Sub(earliest).Milliseconds()
	} else {
		sum.DurationMs = durationMsSum
	}
	return sum, true, nil
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

	err := driverkit.ScanLog(path, logscan.SkipOversized, func(line string) {
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
		model := ev.Message.Model
		if model == "" {
			model = usage.UnknownModel
		}
		b := ensure(model)
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
		return nil, err
	}

	// Deterministic order: opus, haiku, sonnet families first (when
	// present), then any remaining families (including "unknown"), each
	// bucket keyed and labeled by its exact model id — not the collapsed
	// family name. ModelFamily is used here for ordering only; within a
	// family, rows sort by raw id for stability.
	familyRank := func(id string) int {
		switch ModelFamily(id) {
		case "opus":
			return 0
		case "haiku":
			return 1
		case "sonnet":
			return 2
		default:
			return 3
		}
	}
	var models []string
	for model := range buckets {
		models = append(models, model)
	}
	sort.Slice(models, func(i, j int) bool {
		if ri, rj := familyRank(models[i]), familyRank(models[j]); ri != rj {
			return ri < rj
		}
		return models[i] < models[j]
	})
	var result []usage.ModelUsage
	for _, model := range models {
		result = append(result, *buckets[model])
	}
	return result, nil
}

// breakdownByModel indirects to breakdownByModelFile so tests can simulate a
// breakdownByModelFile I/O error without a real filesystem race between it
// and the sumInLog scan.
var breakdownByModel = breakdownByModelFile

// ExtractUsage scans logPath for its result event(s) and, separately, its
// per-model breakdown, returning both in one usage.Report — the claude
// Driver's implementation of the Driver interface's ExtractUsage method.
func ExtractUsage(logPath string) (usage.Report, error) {
	u, found, err := sumInLog(logPath)
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
	return usage.Report{Totals: u, Found: true, SummedByModel: models}, nil
}
