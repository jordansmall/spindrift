package driverkit

import (
	"math"
	"reflect"
	"testing"
)

func sessResult(session string, cost float64, apiMs int64, out map[string]int) Event {
	return Event{Kind: KindResult, SessionID: session, DurationAPIMs: apiMs, ModelOutput: out, Tokens: Tokens{CostUSD: cost}}
}

// rebased runs evs through RebaseResults and returns the results it yields.
func rebased(t *testing.T, base SessionBaselines, evs ...Event) []Event {
	t.Helper()
	path, nd := evLines(t, evs...)
	var got []Event
	if err := scanEvents(path, RebaseResults(nd, base), func(ev Event) { got = append(got, ev) }); err != nil {
		t.Fatal(err)
	}
	return got
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestRebaseResultsSameSessionBecomesDelta(t *testing.T) {
	got := rebased(t, nil,
		sessResult("s1", 1.0, 100, map[string]int{"main": 50, "sub": 10}),
		sessResult("s1", 1.5, 130, map[string]int{"main": 80, "sub": 10, "new": 5}),
	)
	if got[0].Tokens.CostUSD != 1.0 || got[0].DurationAPIMs != 100 || got[0].ModelOutput["main"] != 50 {
		t.Errorf("first result changed: %+v", got[0])
	}
	want := map[string]int{"main": 30, "sub": 0, "new": 5}
	if !near(got[1].Tokens.CostUSD, 0.5) || got[1].DurationAPIMs != 30 || !reflect.DeepEqual(got[1].ModelOutput, want) {
		t.Errorf("second result = cost %v api %d out %v, want 0.5 30 %v", got[1].Tokens.CostUSD, got[1].DurationAPIMs, got[1].ModelOutput, want)
	}
}

func TestRebaseResultsDistinctSessionsUntouched(t *testing.T) {
	got := rebased(t, nil,
		sessResult("s1", 1.0, 100, map[string]int{"m": 50}),
		sessResult("s2", 0.2, 20, map[string]int{"m": 5}),
		Event{Kind: KindResult, Tokens: Tokens{CostUSD: 3}, DurationAPIMs: 9},
		Event{Kind: KindResult, Tokens: Tokens{CostUSD: 3}, DurationAPIMs: 9},
	)
	if got[1].Tokens.CostUSD != 0.2 || got[1].DurationAPIMs != 20 || got[1].ModelOutput["m"] != 5 {
		t.Errorf("distinct session rebased: %+v", got[1])
	}
	if got[3].Tokens.CostUSD != 3 || got[3].DurationAPIMs != 9 {
		t.Errorf("session-less result rebased: %+v", got[3])
	}
}

func TestRebaseResultsSeededFromBase(t *testing.T) {
	base := SessionBaselines{"s1": {CostUSD: 1, DurationAPIMs: 100, ModelOutput: map[string]int{"m": 40}}}
	got := rebased(t, base, sessResult("s1", 1.25, 160, map[string]int{"m": 70}))
	if !near(got[0].Tokens.CostUSD, 0.25) || got[0].DurationAPIMs != 60 || got[0].ModelOutput["m"] != 30 {
		t.Errorf("resumed result = %+v", got[0])
	}
	if base["s1"].CostUSD != 1 || base["s1"].ModelOutput["m"] != 40 {
		t.Errorf("base mutated: %+v", base)
	}
}

func TestRebaseResultsCounterResetTakesFigures(t *testing.T) {
	for name, second := range map[string]Event{
		"cost":   sessResult("s1", 0.5, 300, map[string]int{"m": 90}),
		"api":    sessResult("s1", 2, 50, map[string]int{"m": 90}),
		"output": sessResult("s1", 2, 300, map[string]int{"m": 10}),
	} {
		got := rebased(t, nil, sessResult("s1", 1, 100, map[string]int{"m": 50}), second)
		if got[1].Tokens.CostUSD != second.Tokens.CostUSD || got[1].DurationAPIMs != second.DurationAPIMs || got[1].ModelOutput["m"] != second.ModelOutput["m"] {
			t.Errorf("%s reset: second result = %+v, want as-is %+v", name, got[1], second)
		}
	}
}

func TestRebaseResultsNilModelOutputStaysNilAndKeepsBaseline(t *testing.T) {
	got := rebased(t, nil,
		sessResult("s1", 1, 100, map[string]int{"m": 50}),
		sessResult("s1", 2, 200, nil),
		sessResult("s1", 3, 300, map[string]int{"m": 80}),
	)
	if got[1].ModelOutput != nil {
		t.Errorf("nil ModelOutput became %v", got[1].ModelOutput)
	}
	if got[2].ModelOutput["m"] != 30 {
		t.Errorf("baseline lost across nil result: %v", got[2].ModelOutput)
	}
}

func TestRebaseResultsSkipsNoTotals(t *testing.T) {
	bad := sessResult("s1", 9, 9, nil)
	bad.NoTotals = true
	got := rebased(t, nil, bad, sessResult("s1", 1, 10, nil))
	if got[0].Tokens.CostUSD != 9 || got[1].Tokens.CostUSD != 1 {
		t.Errorf("NoTotals result touched the baseline: %+v", got)
	}
}

func TestRebaseResultsFactoryClonesBase(t *testing.T) {
	base := SessionBaselines{"s1": {CostUSD: 1}}
	nd := RebaseResults(func() Decoder {
		return func(string) []Event { return []Event{sessResult("s1", 2, 0, nil)} }
	}, base)
	for range 2 {
		if got := nd()("x")[0].Tokens.CostUSD; got != 1 {
			t.Errorf("scan saw cost %v, want 1 (each scan restarts from base)", got)
		}
	}
	if base["s1"].CostUSD != 1 {
		t.Errorf("base mutated: %+v", base)
	}
}

func TestSessionBaselinesAfterRecordsLastRawFigures(t *testing.T) {
	path, nd := evLines(t,
		sessResult("s1", 1, 100, map[string]int{"m": 50}),
		sessResult("s1", 1.5, 130, map[string]int{"m": 80}),
		sessResult("s2", 0.2, 20, nil),
	)
	base := SessionBaselines{"s0": {CostUSD: 7}}
	got, err := SessionBaselinesAfter(path, nd, base)
	if err != nil {
		t.Fatal(err)
	}
	want := SessionBaselines{
		"s0": {CostUSD: 7},
		"s1": {CostUSD: 1.5, DurationAPIMs: 130, ModelOutput: map[string]int{"m": 80}},
		"s2": {CostUSD: 0.2, DurationAPIMs: 20},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("baselines = %+v, want %+v", got, want)
	}
	if len(base) != 1 {
		t.Errorf("base mutated: %+v", base)
	}
}

func TestSessionBaselinesAfterMissingLogClonesBase(t *testing.T) {
	base := SessionBaselines{"s1": {CostUSD: 1, ModelOutput: map[string]int{"m": 2}}}
	got, err := SessionBaselinesAfter(missingLog(t), func() Decoder { return nil }, base)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, base) {
		t.Errorf("got %+v, want %+v", got, base)
	}
	got["s1"].ModelOutput["m"] = 99
	if base["s1"].ModelOutput["m"] != 2 {
		t.Error("returned baselines alias base")
	}
}

// A model one result leaves out keeps its earlier baseline, so it is not
// booked at its full cumulative figure when a later result lists it again.
func TestRebaseResultsKeepsBaselineOfModelAbsentFromAResult(t *testing.T) {
	got := rebased(t, nil,
		sessResult("s1", 1.0, 100, map[string]int{"opus": 50, "haiku": 20}),
		sessResult("s1", 1.5, 130, map[string]int{"opus": 80}),
		sessResult("s1", 2.0, 160, map[string]int{"opus": 90, "haiku": 35}),
	)
	want := map[string]int{"opus": 10, "haiku": 15}
	if !reflect.DeepEqual(got[2].ModelOutput, want) {
		t.Errorf("third result ModelOutput = %v, want %v", got[2].ModelOutput, want)
	}
}
