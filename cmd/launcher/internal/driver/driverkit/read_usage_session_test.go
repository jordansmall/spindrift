package driverkit

import (
	"io"
	"reflect"
	"testing"

	"spindrift.dev/launcher/internal/usage"
)

func byAgentOf(nd func() Decoder) func(string) ([]usage.AgentUsage, error) {
	return func(path string) ([]usage.AgentUsage, error) { return UsageByAgent(path, nd, PerCallTotals) }
}

func sessionByAgentOf(nd func() Decoder) func(string) ([]usage.AgentUsage, error) {
	return func(path string) ([]usage.AgentUsage, error) { return UsageByAgent(path, nd, SessionTotals) }
}

func resultEv(ts int64, durationMs int64, tok Tokens) Event {
	return Event{Kind: KindResult, Timestamp: ts, DurationMs: durationMs, DurationAPIMs: 7, NumTurns: 2, Tokens: tok}
}

func sessionReport(t *testing.T, evs ...Event) usage.Report {
	t.Helper()
	path, nd := evLines(t, evs...)
	r, err := ExtractUsage(path, nd, SessionTotals, byModelOf(nd), sessionByAgentOf(nd))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSessionTotalsSumResultsOnly(t *testing.T) {
	r := sessionReport(t,
		usageEv("m1", "a", 100, Tokens{Input: 1000, Output: 1000}),
		resultEv(200, 50, Tokens{Input: 1, Output: 2, CacheRead: 3, CacheWrite5m: 4, CostUSD: 0.5}),
	)
	want := usage.Usage{InputTokens: 1, OutputTokens: 2, CacheReadInputTokens: 3, CacheCreationInputTokens: 4, TotalCostUSD: 0.5, DurationMs: 50, DurationApiMs: 7, NumTurns: 2}
	if !r.Found || r.Totals != want {
		t.Errorf("totals = %+v, want %+v", r.Totals, want)
	}
	if !r.OutputIsMainLoopOnly {
		t.Error("OutputIsMainLoopOnly = false, want true")
	}
	if !r.HasEventSpan || r.EarliestEventMs != 100 || r.LatestEventMs != 200 {
		t.Errorf("span = %v %d %d", r.HasEventSpan, r.EarliestEventMs, r.LatestEventMs)
	}
}

func TestSessionTotalsNotFoundWithoutResult(t *testing.T) {
	r := sessionReport(t, usageEv("m1", "a", 100, Tokens{Input: 5}), Event{Kind: KindStart, Timestamp: 1})
	if r.Found || r.Totals != (usage.Usage{}) {
		t.Errorf("report = %+v, want not found", r)
	}
}

func TestSessionTotalsMultiResultDuration(t *testing.T) {
	cases := []struct {
		name string
		evs  []Event
		want int64
	}{
		{"span wins", []Event{resultEv(1000, 10, Tokens{}), resultEv(5000, 20, Tokens{})}, 4000},
		{"max duration floors a narrow span", []Event{resultEv(1000, 10, Tokens{}), resultEv(1100, 900, Tokens{})}, 900},
		{"no timestamps falls back to max", []Event{resultEv(0, 10, Tokens{}), resultEv(0, 30, Tokens{})}, 30},
		{"shared instant falls back to max", []Event{resultEv(1000, 10, Tokens{}), resultEv(1000, 30, Tokens{})}, 30},
		{"single result ignores span", []Event{{Kind: KindStart, Timestamp: 1}, resultEv(9000, 40, Tokens{})}, 40},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sessionReport(t, tc.evs...).Totals.DurationMs; got != tc.want {
				t.Errorf("DurationMs = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestSessionTotalsSpanCoversEveryTimestampedEvent(t *testing.T) {
	r := sessionReport(t,
		Event{Kind: KindHostOp, Timestamp: 500},
		Event{Kind: KindError},
		resultEv(800, 1, Tokens{}),
		Event{Kind: KindError, Timestamp: 900},
	)
	if !r.HasEventSpan || r.EarliestEventMs != 500 || r.LatestEventMs != 900 {
		t.Errorf("span = %v %d %d", r.HasEventSpan, r.EarliestEventMs, r.LatestEventMs)
	}
	if r := sessionReport(t, resultEv(0, 1, Tokens{})); r.HasEventSpan {
		t.Errorf("no timestamps must leave the span unset: %+v", r)
	}
}

func TestSessionTotalsAgentRows(t *testing.T) {
	ev := func(id, role string, sub bool, tok Tokens) Event {
		e := usageEv(id, "m", 1, tok)
		e.Role, e.InSubagent = role, sub
		return e
	}
	r := sessionReport(t,
		ev("a", "implementor", false, Tokens{Input: 1, Output: 9, CacheWrite5m: 2, CacheWrite1h: 3}),
		ev("a", "implementor", false, Tokens{Input: 100}), // re-emitted id
		ev("b", "reviewer", false, Tokens{Input: 1}),      // pass role, still the main loop
		ev("c", "scout", true, Tokens{Input: 50, Output: 50}),
		resultEv(10, 5, Tokens{Output: 77}),
	)
	want := []usage.AgentUsage{
		{Agent: usage.MainLoopAgent, APICalls: 2, UncachedInputTokens: 2, OutputTokens: 77, CacheCreationInputTokens: 5},
		{Agent: "scout", APICalls: 1, UncachedInputTokens: 50},
	}
	if !reflect.DeepEqual(r.SummedByAgent, want) {
		t.Errorf("SummedByAgent = %+v, want %+v", r.SummedByAgent, want)
	}
}

func TestSessionTotalsSynthesizesMainLoopRow(t *testing.T) {
	sub := usageEv("c", "m", 1, Tokens{Input: 5})
	sub.Role, sub.InSubagent = "scout", true
	r := sessionReport(t, sub, resultEv(10, 5, Tokens{Output: 77}))
	want := []usage.AgentUsage{
		{Agent: usage.MainLoopAgent, OutputTokens: 77},
		{Agent: "scout", APICalls: 1, UncachedInputTokens: 5},
	}
	if !reflect.DeepEqual(r.SummedByAgent, want) {
		t.Errorf("SummedByAgent = %+v, want %+v", r.SummedByAgent, want)
	}
	if r := sessionReport(t, resultEv(10, 5, Tokens{Output: 77})); r.SummedByAgent != nil {
		t.Errorf("no usage events must leave the rows nil, got %+v", r.SummedByAgent)
	}
}

func TestExtractUsageByAgentErrorDegrades(t *testing.T) {
	path, nd := evLines(t, usageEv("m", "a", 1, Tokens{Input: 7}))
	r, err := ExtractUsage(path, nd, PerCallTotals, byModelOf(nd), func(string) ([]usage.AgentUsage, error) { return nil, io.ErrUnexpectedEOF })
	if err != nil || !r.Found || r.Totals.InputTokens != 7 || r.SummedByAgent != nil || r.SummedByModel == nil {
		t.Errorf("report = %+v, err = %v", r, err)
	}
}

func TestSessionTotalsSkipNoTotalsResult(t *testing.T) {
	skipped := resultEv(300, 999, Tokens{Input: 99})
	skipped.NoTotals = true
	r := sessionReport(t, resultEv(100, 50, Tokens{Input: 1}), skipped)
	if !r.Found || r.Totals.InputTokens != 1 || r.Totals.NumTurns != 2 || r.Totals.DurationMs != 50 {
		t.Errorf("totals = %+v, want only the well-formed result", r.Totals)
	}
	if r := sessionReport(t, skipped); r.Found {
		t.Errorf("report = %+v, want not found", r)
	}
}
