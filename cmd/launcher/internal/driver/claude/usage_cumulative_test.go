package claude_test

import (
	"math"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/usage"
)

// The fixture's sess-main reports two results whose total_cost_usd,
// duration_api_ms and modelUsage are cumulative for the session (issue #6706);
// sess-review is a distinct session that still sums on top.
var cumulativeFixture = filepath.Join("testdata", "cumulative-session-6706.jsonl")

func TestExtractUsage_CumulativeSessionResultsCountOnce(t *testing.T) {
	r, err := newDriver().ExtractUsage(cumulativeFixture)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(r.Totals.TotalCostUSD-2.25) > 1e-9 {
		t.Errorf("TotalCostUSD = %v, want 2.25", r.Totals.TotalCostUSD)
	}
	if r.Totals.DurationApiMs != 2000 {
		t.Errorf("DurationApiMs = %d, want 2000", r.Totals.DurationApiMs)
	}
	wantOut := map[string]int{"claude-opus-4-8": 195, "claude-haiku-4-5-20251001": 50}
	for _, m := range r.SummedByModel {
		if m.OutputUnavailable || m.OutputTokens != wantOut[m.Model] {
			t.Errorf("model %s output = %d (unavailable=%v), want %d", m.Model, m.OutputTokens, m.OutputUnavailable, wantOut[m.Model])
		}
	}
	if len(r.SummedByModel) != len(wantOut) {
		t.Errorf("model rows = %+v", r.SummedByModel)
	}
	if r.Totals.OutputTokens != 245 {
		t.Errorf("OutputTokens = %d, want 245", r.Totals.OutputTokens)
	}
	for _, a := range r.SummedByAgent {
		if a.Agent == usage.SubagentsAgent && a.OutputTokens != 80 {
			t.Errorf("subagents row = %d, want 80 (counted once)", a.OutputTokens)
		}
	}
}

func TestReadPasses_CumulativeSessionResultsHaveNoPhantomResidual(t *testing.T) {
	got, err := newDriver().ReadPasses(cumulativeFixture, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("tallies = %d, want 4", len(got))
	}
	type want struct {
		out      int
		api      int64
		cost     float64
		subagent int
	}
	for i, w := range []want{{150, 1000, 1.00, 50}, {70, 800, 0.75, 30}, {25, 200, 0.50, 0}} {
		u := got[i+1].Usage
		if u.OutputTokens != w.out || u.DurationApiMs != w.api || math.Abs(u.TotalCostUSD-w.cost) > 1e-9 {
			t.Errorf("pass %d usage = out %d api %d cost %v, want %+v", i+1, u.OutputTokens, u.DurationApiMs, u.TotalCostUSD, w)
		}
		sub := 0
		for _, a := range got[i+1].Agents {
			if a.Agent == usage.SubagentsAgent {
				sub = a.OutputTokens
			}
		}
		if sub != w.subagent {
			t.Errorf("pass %d subagents row = %d, want %d", i+1, sub, w.subagent)
		}
	}
}
