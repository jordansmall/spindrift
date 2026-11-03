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
// MessageID is never deduplicated), so the sum runs over DISTINCT message.id:
// claude-code re-emits a multi-block message once per block with identical
// usage. The result event's own usage header covers one call only and is
// ignored (issue #2078). A usage event naming no model lands under
// usage.UnknownModel. Rows stay keyed by exact model id. They are ordered by
// family rank (ascending) then exact id when family is non-nil, by id alone
// when it is nil: family is the Driver's heartbeat hook, which supplies the
// rank, so a Driver whose ids are not a ranked model family passes nil.
//
// Under PerCallTotals OutputTokens are summed per call. Under SessionTotals
// the per-call output is a placeholder (#3183) and is never used: a row's
// OutputTokens is instead the sum of its model's per-model figure over the
// KindResult events (issue #6269). That holds only when the log has a result
// and every result carries per-model figures; otherwise every row is
// OutputUnavailable. A row whose model no result names is OutputUnavailable
// too (such as `<synthetic>` or unknown, which no result names), and a model
// only a result names gets a row of its own. A NoTotals result is skipped
// entirely, as sumTotals does.
func UsageByModel(path string, newDecoder func() Decoder, family ModelFamily, src TotalsSource) ([]usage.ModelUsage, error) {
	buckets := make(map[string]*usage.ModelUsage)
	seenIDs := make(map[string]bool)
	resultOutput := make(map[string]int)
	results, complete := 0, true
	err := scanEvents(path, newDecoder, func(ev Event) {
		if ev.Kind == KindResult && !ev.NoTotals && src == SessionTotals {
			results++
			if len(ev.ModelOutput) == 0 {
				complete = false
			}
			for model, out := range ev.ModelOutput {
				resultOutput[model] += out
			}
		}
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
		b := modelBucket(buckets, model)
		b.UncachedInputTokens += ev.Tokens.Input
		if src == PerCallTotals {
			b.OutputTokens += ev.Tokens.Output
		}
		b.CacheReadInputTokens += ev.Tokens.CacheRead
		b.CacheWrite5mTokens += ev.Tokens.CacheWrite5m
		b.CacheWrite1hTokens += ev.Tokens.CacheWrite1h
	})
	if err != nil {
		return nil, err
	}
	if src == SessionTotals {
		outputKnown := complete && results > 0
		if outputKnown {
			for model := range resultOutput {
				modelBucket(buckets, model)
			}
		}
		for model, b := range buckets {
			if out, named := resultOutput[model]; outputKnown && named {
				b.OutputTokens = out
			} else {
				b.MarkOutputUnavailable()
			}
		}
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

// DurationMs is the span length in milliseconds.
func (s EventSpan) DurationMs() int64 { return s.Latest - s.Earliest }

// sumTotals returns the run totals of the log at path from src, the event
// span, and whether the log carries any totals at all; a missing log, or one
// with none, reports found=false and no error. It also returns the subagent
// residual already folded into u.OutputTokens (always 0 under PerCallTotals),
// so ExtractUsage can tell it from the main loop's output.
func sumTotals(path string, newDecoder func() Decoder, src TotalsSource) (usage.Usage, EventSpan, bool, int, error) {
	if src == SessionTotals {
		return sumSession(path, newDecoder)
	}
	u, span, found, err := sumPerCall(path, newDecoder)
	return u, span, found, 0, err
}

// spanAnchor tracks the earliest non-zero event Timestamp of any kind. A
// PerCallTotals span anchors on it, not on KindStart: a run whose attempts all
// errored emits no start event yet still has a measurable span.
type spanAnchor struct {
	first int64
	have  bool
}

func (a *spanAnchor) see(ts int64) {
	if ts != 0 && (!a.have || ts < a.first) {
		a.first, a.have = ts, true
	}
}

// span closes the anchor at the last usage Timestamp; ok is false without an
// anchor or a stamped usage, so a raw epoch figure is never reported: an
// unstamped last usage yields no span even when earlier events were stamped.
func (a spanAnchor) span(lastUsage int64) (EventSpan, bool) {
	if !a.have || lastUsage < a.first {
		return EventSpan{}, false
	}
	return EventSpan{Earliest: a.first, Latest: lastUsage, Have: true}, true
}

// sumPerCall counts every KindUsage as one turn with no MessageID
// deduplication (only the breakdowns dedup), leaves DurationApiMs zero, and
// spans the earliest event Timestamp to the last KindUsage; with no timestamp
// the span and DurationMs stay unset.
func sumPerCall(path string, newDecoder func() Decoder) (usage.Usage, EventSpan, bool, error) {
	var u usage.Usage
	var anchor spanAnchor
	var lastUsage int64
	err := scanEvents(path, newDecoder, func(ev Event) {
		anchor.see(ev.Timestamp)
		if ev.Kind == KindUsage {
			addTokens(&u, ev.Tokens)
			u.NumTurns++
			lastUsage = ev.Timestamp
		}
	})
	if err != nil || u.NumTurns == 0 {
		return usage.Usage{}, EventSpan{}, false, err
	}
	span, ok := anchor.span(lastUsage)
	if ok {
		u.DurationMs = span.DurationMs()
	}
	return u, span, true, nil
}

// sumSession reports found only for a log with a KindResult that has totals.
// Each result's subagent residual (resultResidual) is added to OutputTokens
// and returned apart.
//
// Wall time is not additive across sessions: they can overlap (issue #2058) or
// idle between runs, so with several results DurationMs is the span of every
// non-zero event Timestamp, floored by the longest session's own DurationMs
// (the floor also covers a log with no timestamps, or a span narrower than a
// session that provably ran longer). One result reports its own DurationMs.
func sumSession(path string, newDecoder func() Decoder) (usage.Usage, EventSpan, bool, int, error) {
	var u usage.Usage
	var span EventSpan
	var maxDurationMs int64
	results, residual := 0, 0
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
			residual += addResultTokens(&u, ev)
			u.DurationApiMs += ev.DurationAPIMs
			u.NumTurns += ev.NumTurns
			maxDurationMs = max(maxDurationMs, ev.DurationMs)
		}
	})
	if err != nil || results == 0 {
		return usage.Usage{}, EventSpan{}, false, 0, err
	}
	u.DurationMs = maxDurationMs
	if results > 1 && span.DurationMs() > 0 {
		u.DurationMs = max(span.DurationMs(), maxDurationMs)
	}
	return u, span, true, residual, nil
}

// resultResidual is the output a session result reports beyond its main loop:
// the modelUsage outputTokens sum (subagents included) less usage.output_tokens.
// A result without a per-model figure has none, so it counts only its own.
// The max floor keeps the main-loop figure when the modelUsage sum falls below
// the result's own usage.output_tokens, so the main row never goes negative
// and the agent rows still sum to Totals.
func resultResidual(ev Event) int {
	if len(ev.ModelOutput) == 0 {
		return 0
	}
	sum := 0
	for _, n := range ev.ModelOutput {
		sum += n
	}
	return max(0, sum-ev.Tokens.Output)
}

// addResultTokens adds a session result's tokens to u, its subagent residual
// included in OutputTokens, and returns that residual.
func addResultTokens(u *usage.Usage, ev Event) int {
	addTokens(u, ev.Tokens)
	r := resultResidual(ev)
	u.OutputTokens += r
	return r
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
// they stay 0 on every row for ExtractUsage to patch from the session totals,
// split between the main-loop row and a usage.SubagentsAgent row.
func UsageByAgent(path string, newDecoder func() Decoder, src TotalsSource) ([]usage.AgentUsage, error) {
	agents := make(map[string]*usage.AgentUsage)
	seenIDs := make(map[string]bool)
	err := scanEvents(path, newDecoder, func(ev Event) {
		if ev.Kind != KindUsage || !firstSighting(seenIDs, ev.MessageID) {
			return
		}
		addAgentCall(agents, ev, src)
	})
	if err != nil {
		return nil, err
	}
	return sortedAgents(agents), nil
}

// addAgentCall books one already-deduped KindUsage event onto its agent's row
// in agents, creating it: the keying and per-call arithmetic UsageByAgent and
// ReadPasses share.
func addAgentCall(agents map[string]*usage.AgentUsage, ev Event, src TotalsSource) {
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
}

// ExtractUsage returns the totals of the log at logPath (see sumTotals) with
// its per-model and per-agent breakdowns as one usage.Report; Found is false
// when the log has no totals or is absent. EarliestEventMs, LatestEventMs and
// HasEventSpan carry the sumTotals span.
//
// byModel and byAgent supply the breakdowns, normally UsageByModel and
// UsageByAgent over the same decoder; a Driver passes its own so a test can
// fail one or reorder it. An error degrades only that section to nil with a
// stderr WARNING naming the path, not the totals already summed (issue #674).
//
// Under SessionTotals the result events are the only ground truth for output
// tokens, so the main-loop row takes their usage.output_tokens; subagent
// output beyond it lands on a usage.SubagentsAgent row. SkipOversized drops
// lines over 4 MiB, so a run whose main-loop lines were all oversized arrives
// with subagent rows only: the row is then synthesized, prepended to keep
// main-loop first. A nil agents slice stays nil, with no row added.
func ExtractUsage(
	logPath string,
	newDecoder func() Decoder,
	src TotalsSource,
	byModel func(path string) ([]usage.ModelUsage, error),
	byAgent func(path string) ([]usage.AgentUsage, error),
) (usage.Report, error) {
	u, span, found, residual, err := sumTotals(logPath, newDecoder, src)
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
		agents = withSessionOutput(agents, u.OutputTokens, residual)
	}
	report := usage.Report{
		Totals: u, Found: true, SummedByModel: models, SummedByAgent: agents,
	}
	if span.Have {
		report.EarliestEventMs = span.Earliest
		report.LatestEventMs = span.Latest
		report.HasEventSpan = true
	}
	return report, nil
}

// withSessionOutput patches the main-loop row to total less residual,
// synthesizing it when absent, and appends the usage.SubagentsAgent row for the
// residual. A nil agents slice stays nil, so its output, residual included,
// reaches no row.
func withSessionOutput(agents []usage.AgentUsage, total, residual int) []usage.AgentUsage {
	if len(agents) == 0 {
		return agents
	}
	mainOut := total - residual
	patched := false
	for i := range agents {
		if agents[i].Agent == usage.MainLoopAgent {
			agents[i].OutputTokens = mainOut
			patched = true
			break
		}
	}
	if !patched {
		agents = append([]usage.AgentUsage{{Agent: usage.MainLoopAgent, OutputTokens: mainOut}}, agents...)
	}
	if residual > 0 {
		agents = append(agents, usage.AgentUsage{Agent: usage.SubagentsAgent, OutputTokens: residual})
	}
	return agents
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

func modelBucket(buckets map[string]*usage.ModelUsage, model string) *usage.ModelUsage {
	b, ok := buckets[model]
	if !ok {
		b = &usage.ModelUsage{Model: model}
		buckets[model] = b
	}
	return b
}
