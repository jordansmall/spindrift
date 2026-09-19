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

// timestampedEvent decodes the top-level "type" and "timestamp" fields of a
// claude-code stream-json line. The decoder reads only top-level keys, so it
// never picks up a "timestamp" nested inside tool_result content. Type must be
// non-empty to skip a line carrying a top-level timestamp but no event type.
type timestampedEvent struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
}

// eventSpan is the earliest and latest top-level event timestamp seen in a
// sumInLog scan. sumInLog derives its DurationMs floor from it, and
// ExtractUsage returns it so a caller can derive a span across multiple logs
// the same way (usage.Report.EarliestEventMs, LatestEventMs, HasEventSpan).
type eventSpan struct {
	earliest, latest time.Time
	have             bool
}

// sumInLog sums every "type":"result" event in the log at path (orchestrator
// mode runs the driver repeatedly, one result event per session) and returns
// the eventSpan of top-level timestamps seen. A missing file or an absent
// result event reports found=false, not an error. Every field is additive
// except DurationMs, which the multi-session branch below derives instead.
func sumInLog(path string) (usage.Usage, eventSpan, bool, error) {
	var sum usage.Usage
	resultCount := 0
	var maxDurationMs int64
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
				if ev.DurationMs > maxDurationMs {
					maxDurationMs = ev.DurationMs
				}
			}
		}
	})
	if err != nil {
		return usage.Usage{}, eventSpan{}, false, err
	}

	if resultCount == 0 {
		return usage.Usage{}, eventSpan{}, false, nil
	}

	// Wall time is not additive across sessions: they can overlap (issue #2058)
	// or idle between runs, so the span between top-level timestamps is the
	// better estimate. The floor covers a log with no timestamps, one whose
	// timestamps share an instant, and a span narrower than a session that
	// provably ran longer. One session always reports its own duration_ms.
	if resultCount > 1 {
		var spanMs int64
		if haveTimestamp && latest.After(earliest) {
			spanMs = latest.Sub(earliest).Milliseconds()
		}
		sum.DurationMs = spanMs
		if maxDurationMs > sum.DurationMs {
			sum.DurationMs = maxDurationMs
		}
	} else {
		sum.DurationMs = maxDurationMs
	}
	span := eventSpan{earliest: earliest, latest: latest, have: haveTimestamp}
	return sum, span, true, nil
}

// assistantEvent decodes line as a claude-code assistant event, returning true
// only when it is one and carries a non-nil Message.
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

// breakdownByModelFile returns per-model token breakdowns from the log at path,
// and (nil, nil) when the file is missing. Each assistant event carries that one
// call's usage, so the sum runs over DISTINCT message.id: claude-code re-emits a
// multi-block message once per block with identical usage. It ignores the result
// event's own usage header, which covers one call only (the ~9x gap in #2078).
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
			// Pre-TTL-split log: only the flat total is populated. The
			// Messages API's cache_control default TTL is 5m and 1h is
			// opt-in, so an un-split total is almost certainly all-5m.
			b.CacheWrite5mTokens += v
		}
	})
	if err != nil {
		return nil, err
	}

	// Deterministic order: opus, haiku, sonnet, then the rest (including
	// "unknown"). Rows stay keyed by exact model id, not the family name, so
	// ModelFamily orders only; within a family, rows sort by raw id.
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

// breakdownByModel indirects to breakdownByModelFile so tests can simulate an
// I/O error without racing the sumInLog scan on the real filesystem.
var breakdownByModel = breakdownByModelFile

// breakdownByAgentFile returns per-agent token breakdowns from the log at path,
// the main loop separate from each subagent, and (nil, nil) when the file is
// missing. It dedups by message.id like breakdownByModelFile. Every row's
// OutputTokens stays 0 for ExtractUsage to patch. One forward pass suffices: a
// spawn's tool-use block always precedes that subagent's own messages.
func breakdownByAgentFile(path string) ([]usage.AgentUsage, error) {
	buckets := make(map[string]*usage.AgentUsage)
	ensure := func(agent string) *usage.AgentUsage {
		if b, ok := buckets[agent]; ok {
			return b
		}
		b := &usage.AgentUsage{Agent: agent}
		buckets[agent] = b
		return b
	}
	taskRole := make(map[string]string)
	seenIDs := make(map[string]bool)

	err := driverkit.ScanLog(path, logscan.SkipOversized, func(line string) {
		ev, ok := assistantEvent(line)
		if !ok {
			return
		}
		CollectTaskRoles(ev, taskRole)
		if id := ev.Message.ID; id != "" {
			if seenIDs[id] {
				return
			}
			seenIDs[id] = true
		}
		agent := ResolveRole(ev, taskRole, "")
		if ev.ParentToolUseID == "" {
			// ResolveRole defaults a top-level message to ImplementorRole
			// (issue #2092); this breakdown wants the neutral
			// usage.MainLoopAgent label instead.
			agent = usage.MainLoopAgent
		}
		b := ensure(agent)
		b.APICalls++
		b.UncachedInputTokens += ev.Message.Usage.InputTokens
		// No OutputTokens sum; ExtractUsage patches the main-loop row instead.
		b.CacheReadInputTokens += ev.Message.Usage.CacheReadInputTokens
		b.CacheCreationInputTokens += ev.Message.Usage.CacheCreationInputTokens
	})
	if err != nil {
		return nil, err
	}

	var agents []string
	for agent := range buckets {
		agents = append(agents, agent)
	}
	// Deterministic order: usage.MainLoopAgent first, then subagents by
	// descending TotalTokens so the costliest is identifiable, ties by name.
	sort.Slice(agents, func(i, j int) bool {
		ai, aj := agents[i], agents[j]
		if ai == usage.MainLoopAgent {
			return true
		}
		if aj == usage.MainLoopAgent {
			return false
		}
		ti, tj := buckets[ai].TotalTokens(), buckets[aj].TotalTokens()
		if ti != tj {
			return ti > tj
		}
		return ai < aj
	})
	var result []usage.AgentUsage
	for _, agent := range agents {
		result = append(result, *buckets[agent])
	}
	return result, nil
}

// breakdownByAgent indirects to breakdownByAgentFile so tests can simulate an
// I/O error.
var breakdownByAgent = breakdownByAgentFile

// ExtractUsage returns the totals, per-model and per-agent breakdowns of
// logPath as one usage.Report.
func ExtractUsage(logPath string) (usage.Report, error) {
	u, span, found, err := sumInLog(logPath)
	if err != nil {
		return usage.Report{}, err
	}
	if !found {
		return usage.Report{}, nil
	}
	// A breakdownByModel I/O error degrades the per-model section, not the
	// aggregate totals already parsed above (issue #674).
	models, err := breakdownByModel(logPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: breakdown by model failed for %s: %v\n", logPath, err)
		models = nil
	}
	// Same degrade-not-fail contract: a breakdownByAgent I/O error loses only
	// the per-agent section.
	agents, err := breakdownByAgent(logPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: breakdown by agent failed for %s: %v\n", logPath, err)
		agents = nil
	}
	// The result event is the only ground truth for output tokens (see
	// usage.Report.OutputIsMainLoopOnly below). logscan.SkipOversized drops
	// lines over 4 MiB, so a run whose main-loop lines were all oversized
	// arrives with subagent rows only; synthesize the row there, prepended to
	// keep main-loop first. A nil agents slice stays nil, with no row added.
	patched := false
	for i := range agents {
		if agents[i].Agent == usage.MainLoopAgent {
			agents[i].OutputTokens = u.OutputTokens
			patched = true
			break
		}
	}
	if !patched && len(agents) > 0 {
		main := usage.AgentUsage{Agent: usage.MainLoopAgent, OutputTokens: u.OutputTokens}
		agents = append([]usage.AgentUsage{main}, agents...)
	}
	report := usage.Report{Totals: u, Found: true, SummedByModel: models, SummedByAgent: agents, OutputIsMainLoopOnly: true}
	if span.have {
		report.EarliestEventMs = span.earliest.UnixMilli()
		report.LatestEventMs = span.latest.UnixMilli()
		report.HasEventSpan = true
	}
	return report, nil
}
