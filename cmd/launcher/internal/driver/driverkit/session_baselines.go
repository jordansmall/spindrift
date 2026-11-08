package driverkit

import "maps"

// SessionCumulative is the last raw cumulative figures a session's KindResult
// reported: the baseline the session's next result is rebased against. It is
// persisted between runs so a resumed session continues from its predecessor.
type SessionCumulative struct {
	CostUSD       float64        `json:"cost_usd"`
	DurationAPIMs int64          `json:"duration_api_ms"`
	ModelOutput   map[string]int `json:"model_output,omitempty"`
}

// SessionBaselines maps a session id to its SessionCumulative.
type SessionBaselines map[string]SessionCumulative

func (b SessionBaselines) clone() SessionBaselines {
	out := make(SessionBaselines, len(b))
	for id, c := range b {
		c.ModelOutput = maps.Clone(c.ModelOutput)
		out[id] = c
	}
	return out
}

// restartedBy reports whether cur is below c on any figure. The figures only
// grow within a session, so a drop means the counters restarted (a fresh
// session reusing the pinned id after its transcript was evicted).
func (c SessionCumulative) restartedBy(cur SessionCumulative) bool {
	if cur.CostUSD < c.CostUSD || cur.DurationAPIMs < c.DurationAPIMs {
		return true
	}
	for model, n := range cur.ModelOutput {
		if prev, ok := c.ModelOutput[model]; ok && n < prev {
			return true
		}
	}
	return false
}

// observe rebases ev in place and records its raw figures as the session's new
// baseline. It is the one place the reset and nil-ModelOutput rules live, shared
// by RebaseResults and SessionBaselinesAfter.
func (b SessionBaselines) observe(ev *Event) {
	if ev.Kind != KindResult || ev.SessionID == "" || ev.NoTotals {
		return
	}
	cur := SessionCumulative{CostUSD: ev.Tokens.CostUSD, DurationAPIMs: ev.DurationAPIMs, ModelOutput: maps.Clone(ev.ModelOutput)}
	prev, seen := b[ev.SessionID]
	if seen && !prev.restartedBy(cur) {
		ev.Tokens.CostUSD -= prev.CostUSD
		ev.DurationAPIMs -= prev.DurationAPIMs
		if ev.ModelOutput != nil {
			delta := make(map[string]int, len(ev.ModelOutput))
			for model, n := range ev.ModelOutput {
				delta[model] = n - prev.ModelOutput[model]
			}
			ev.ModelOutput = delta
		}
		// A model a result leaves out (or a result with no per-model figures
		// at all) keeps its earlier figure, so a later result listing it
		// diffs against that rather than against zero.
		if len(prev.ModelOutput) > 0 {
			merged := maps.Clone(prev.ModelOutput)
			maps.Copy(merged, cur.ModelOutput)
			cur.ModelOutput = merged
		}
	}
	b[ev.SessionID] = cur
}

// RebaseResults wraps the decoder factory so every KindResult with a SessionID
// reports its session-cumulative cost, API time and per-model output as the
// increment since the session's previous result, in event order (issue #6706).
// Each decoder starts from its own clone of base, which seeds the sessions
// earlier logs already reported; base itself is never written. A result with no
// SessionID or with NoTotals passes through untouched, as does a session's
// first result when base has none of it.
func RebaseResults(newDecoder func() Decoder, base SessionBaselines) func() Decoder {
	return func() Decoder {
		decode := newDecoder()
		seen := base.clone()
		return func(line string) []Event {
			evs := decode(line)
			for i := range evs {
				seen.observe(&evs[i])
			}
			return evs
		}
	}
}

// SessionBaselinesAfter returns base advanced by the log at path: a clone of
// base updated with the last raw cumulative figures of each session the log's
// results report. A missing log is a clone of base.
func SessionBaselinesAfter(path string, newDecoder func() Decoder, base SessionBaselines) (SessionBaselines, error) {
	after := base.clone()
	err := scanEvents(path, newDecoder, func(ev Event) { after.observe(&ev) })
	if err != nil {
		return nil, err
	}
	return after, nil
}
