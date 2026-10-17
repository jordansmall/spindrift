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
// usage.UnknownModel. Rows stay keyed by exact model id. They are ordered by
// family rank (ascending) then exact id when family is non-nil, by id alone
// when it is nil: family is the Driver's heartbeat hook, which supplies the
// rank, so a Driver whose ids are not a ranked model family passes nil.
func UsageByModel(path string, newDecoder func() Decoder, family ModelFamily) ([]usage.ModelUsage, error) {
	buckets := make(map[string]*usage.ModelUsage)
	seenIDs := make(map[string]bool)
	err := scanEvents(path, newDecoder, func(ev Event) {
		if ev.Kind != KindUsage {
			return
		}
		if !firstSighting(seenIDs, ev.MessageID) {
			return
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
	if family == nil {
		sort.Strings(models)
	} else {
		ranks := make(map[string]int, len(models))
		for _, model := range models {
			_, ranks[model] = family(model)
		}
		sort.Slice(models, func(i, j int) bool {
			if ri, rj := ranks[models[i]], ranks[models[j]]; ri != rj {
				return ri < rj
			}
			return models[i] < models[j]
		})
	}
	var result []usage.ModelUsage
	for _, model := range models {
		result = append(result, *buckets[model])
	}
	return result, nil
}

// TotalsSource names where a Driver's run totals come from.
type TotalsSource int

const (
	// PerCallTotals sums every KindUsage event: the CLI reports one tally per
	// call and no session summary (opencode).
	PerCallTotals TotalsSource = iota

	// SessionTotals sums the KindResult events: the CLI reports a summary per
	// session, which is the only ground truth for output tokens, cost, turns
	// and durations (claude). Per-call KindUsage events then feed the
	// breakdowns only.
	SessionTotals
)

// EventSpan is the earliest and latest event Timestamp of a scan. Have is
// false when the scan saw none to anchor it.
type EventSpan struct {
	Earliest, Latest int64
	Have             bool
}

// SumTotals returns the run totals of the log at path from src, the event
// span, and whether the log carries any totals at all; a missing log, or one
// with none, reports found=false and no error.
func SumTotals(path string, newDecoder func() Decoder, src TotalsSource) (usage.Usage, EventSpan, bool, error) {
	if src == SessionTotals {
		return sumSession(path, newDecoder)
	}
	return sumPerCall(path, newDecoder)
}

// sumPerCall counts every KindUsage as one turn with no MessageID
// deduplication (only the breakdowns dedup), leaves DurationApiMs zero, and
// spans the first KindStart to the last KindUsage; without a KindStart the
// span and DurationMs stay unset rather than reporting a raw epoch figure.
func sumPerCall(path string, newDecoder func() Decoder) (usage.Usage, EventSpan, bool, error) {
	var u usage.Usage
	var firstStart, lastUsage int64
	haveStart := false
	err := scanEvents(path, newDecoder, func(ev Event) {
		switch ev.Kind {
		case KindStart:
			if !haveStart {
				firstStart = ev.Timestamp
				haveStart = true
			}
		case KindUsage:
			addTokens(&u, ev.Tokens)
			u.NumTurns++
			lastUsage = ev.Timestamp
		}
	})
	if err != nil || u.NumTurns == 0 {
		return usage.Usage{}, EventSpan{}, false, err
	}
	var span EventSpan
	if haveStart {
		u.DurationMs = lastUsage - firstStart
		span = EventSpan{Earliest: firstStart, Latest: lastUsage, Have: true}
	}
	return u, span, true, nil
}

// sumSession reports found only for a log with a KindResult that has totals.
// Wall time is not additive across sessions: they can overlap (issue #2058) or
// idle between runs, so with several results DurationMs is the span of every
// non-zero event Timestamp, floored by the longest session's own DurationMs
// (the floor also covers a log with no timestamps, or a span narrower than a
// session that provably ran longer). One result reports its own DurationMs.
func sumSession(path string, newDecoder func() Decoder) (usage.Usage, EventSpan, bool, error) {
	var u usage.Usage
	var span EventSpan
	var maxDurationMs int64
	results := 0
	err := scanEvents(path, newDecoder, func(ev Event) {
		if ev.Timestamp != 0 {
			if !span.Have || ev.Timestamp < span.Earliest {
				span.Earliest = ev.Timestamp
			}
			if !span.Have || ev.Timestamp > span.Latest {
				span.Latest = ev.Timestamp
			}
			span.Have = true
		}
		if ev.Kind == KindResult && !ev.NoTotals {
			results++
			addTokens(&u, ev.Tokens)
			u.DurationApiMs += ev.DurationAPIMs
			u.NumTurns += ev.NumTurns
			maxDurationMs = max(maxDurationMs, ev.DurationMs)
		}
	})
	if err != nil || results == 0 {
		return usage.Usage{}, EventSpan{}, false, err
	}
	u.DurationMs = maxDurationMs
	if results > 1 && span.Latest > span.Earliest {
		u.DurationMs = max(span.Latest-span.Earliest, maxDurationMs)
	}
	return u, span, true, nil
}

func addTokens(u *usage.Usage, t Tokens) {
	u.InputTokens += t.Input
	u.OutputTokens += t.Output
	u.CacheReadInputTokens += t.CacheRead
	u.CacheCreationInputTokens += t.CacheWrite5m + t.CacheWrite1h
	u.TotalCostUSD += t.CostUSD
}

// UsageByAgent returns per-agent breakdowns from the KindUsage events in the
// log at path, or (nil, nil) when the log is absent. A non-empty MessageID
// counts once. The main loop is labelled usage.MainLoopAgent, the breakdown's
// own name for it, not the Transcript's ImplementorRole default. An InSubagent
// event's row is its Role; any other event is the main loop whatever pass Role
// it carries. Under PerCallTotals OutputTokens are summed; under SessionTotals
// they stay 0 on every row for ExtractUsage to patch from the session totals.
func UsageByAgent(path string, newDecoder func() Decoder, src TotalsSource) ([]usage.AgentUsage, error) {
	agents := make(map[string]*usage.AgentUsage)
	seenIDs := make(map[string]bool)
	err := scanEvents(path, newDecoder, func(ev Event) {
		if ev.Kind != KindUsage || !firstSighting(seenIDs, ev.MessageID) {
			return
		}
		name := usage.MainLoopAgent
		if ev.InSubagent && ev.Role != "" {
			name = ev.Role
		}
		a, ok := agents[name]
		if !ok {
			a = &usage.AgentUsage{Agent: name}
			agents[name] = a
		}
		a.APICalls++
		a.UncachedInputTokens += ev.Tokens.Input
		if src == PerCallTotals {
			a.OutputTokens += ev.Tokens.Output
		}
		a.CacheReadInputTokens += ev.Tokens.CacheRead
		a.CacheCreationInputTokens += ev.Tokens.CacheWrite5m + ev.Tokens.CacheWrite1h
	})
	if err != nil {
		return nil, err
	}
	return sortedAgents(agents), nil
}

// ExtractUsage returns the totals of the log at logPath (see SumTotals) with
// its per-model and per-agent breakdowns as one usage.Report; Found is false
// when the log has no totals or is absent. EarliestEventMs, LatestEventMs and
// HasEventSpan carry the SumTotals span.
//
// byModel and byAgent supply the breakdowns, normally UsageByModel and
// UsageByAgent over the same decoder; a Driver passes its own so a test can
// fail one or reorder it. An error degrades only that section to nil with a
// stderr WARNING naming the path, not the totals already summed (issue #674).
//
// Under SessionTotals the result events are the only ground truth for output
// tokens, so the main-loop row takes them and Report.OutputIsMainLoopOnly is
// set. SkipOversized drops lines over 4 MiB, so a run whose main-loop lines
// were all oversized arrives with subagent rows only: the row is then
// synthesized, prepended to keep main-loop first. A nil agents slice stays
// nil, with no row added.
func ExtractUsage(
	logPath string,
	newDecoder func() Decoder,
	src TotalsSource,
	byModel func(path string) ([]usage.ModelUsage, error),
	byAgent func(path string) ([]usage.AgentUsage, error),
) (usage.Report, error) {
	u, span, found, err := SumTotals(logPath, newDecoder, src)
	if err != nil {
		return usage.Report{}, err
	}
	if !found {
		return usage.Report{}, nil
	}
	models, err := byModel(logPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: breakdown by model failed for %s: %v\n", logPath, err)
		models = nil
	}
	agents, err := byAgent(logPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: breakdown by agent failed for %s: %v\n", logPath, err)
		agents = nil
	}
	if src == SessionTotals {
		agents = withMainLoopOutput(agents, u.OutputTokens)
	}
	report := usage.Report{
		Totals: u, Found: true, SummedByModel: models, SummedByAgent: agents,
		OutputIsMainLoopOnly: src == SessionTotals,
	}
	if span.Have {
		report.EarliestEventMs = span.Earliest
		report.LatestEventMs = span.Latest
		report.HasEventSpan = true
	}
	return report, nil
}

func withMainLoopOutput(agents []usage.AgentUsage, output int) []usage.AgentUsage {
	for i := range agents {
		if agents[i].Agent == usage.MainLoopAgent {
			agents[i].OutputTokens = output
			return agents
		}
	}
	if len(agents) == 0 {
		return agents
	}
	return append([]usage.AgentUsage{{Agent: usage.MainLoopAgent, OutputTokens: output}}, agents...)
}

// sortedAgents orders usage.MainLoopAgent first, then the rest by descending
// TotalTokens (so the costliest is identifiable), ties by name.
func sortedAgents(buckets map[string]*usage.AgentUsage) []usage.AgentUsage {
	names := make([]string, 0, len(buckets))
	for name := range buckets {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		ni, nj := names[i], names[j]
		if ni == usage.MainLoopAgent || nj == usage.MainLoopAgent {
			return ni == usage.MainLoopAgent
		}
		if ti, tj := buckets[ni].TotalTokens(), buckets[nj].TotalTokens(); ti != tj {
			return ti > tj
		}
		return ni < nj
	})
	var result []usage.AgentUsage
	for _, name := range names {
		result = append(result, *buckets[name])
	}
	return result
}

// firstSighting reports whether id is new to seen, recording it. An empty id is
// never a duplicate.
func firstSighting(seen map[string]bool, id string) bool {
	if id == "" {
		return true
	}
	if seen[id] {
		return false
	}
	seen[id] = true
	return true
}
