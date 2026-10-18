package recordstats

import (
	"math"
	"slices"
	"testing"

	"spindrift.dev/launcher/internal/dispatchrecord"
)

func keysOf(gs []Group) []string {
	var keys []string
	for _, g := range gs {
		keys = append(keys, g.Key)
	}
	return keys
}

func TestGroupRecordsFirstAppearanceNoneLast(t *testing.T) {
	recs := []dispatchrecord.Record{
		{DispatchKey: "1"}, {DispatchKey: "2", Revision: "b"},
		{DispatchKey: "3", Revision: "a"}, {DispatchKey: "4", Revision: "b"},
	}
	gs := GroupRecords(recs, RevisionKey)
	if got, want := keysOf(gs), []string{"b", "a", None}; !slices.Equal(got, want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
	if len(gs[0].Records) != 2 || gs[0].Records[1].DispatchKey != "4" {
		t.Fatalf("group b = %+v", gs[0].Records)
	}
}

func TestGroupPassesSplitsRecordsByPass(t *testing.T) {
	recs := []dispatchrecord.Record{
		{DispatchKey: "1", Passes: []dispatchrecord.Pass{
			{Role: "review", Models: []string{"y", "x"}},
			{Role: "implement", Models: []string{"x", "y"}},
			{Role: "fix"},
		}},
		{DispatchKey: "2"},
	}
	gs := GroupPasses(recs, ModelKey)
	if got, want := keysOf(gs), []string{"x+y", None}; !slices.Equal(got, want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
	if n := len(gs[0].Records); n != 1 || len(gs[0].Records[0].Passes) != 2 {
		t.Fatalf("x+y group = %+v", gs[0].Records)
	}
	if n := len(gs[1].Records); n != 2 {
		t.Fatalf("(none) group has %d records, want the pass-less one and the unmodelled pass", n)
	}
	if len(recs[0].Passes) != 3 {
		t.Fatal("grouping mutated the input Record")
	}
}

func TestGroupPassesByRoleThenSortRoleGroups(t *testing.T) {
	recs := []dispatchrecord.Record{{Passes: []dispatchrecord.Pass{
		{}, {Role: "zeta"}, {Role: "review"}, {Role: "implement"},
	}}}
	gs := GroupPasses(recs, RoleKey)
	SortRoleGroups(gs)
	if got, want := keysOf(gs), []string{"implement", "review", "zeta", None}; !slices.Equal(got, want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
}

func TestPromptKeyAndPromptRoles(t *testing.T) {
	r := dispatchrecord.Record{PromptHashes: map[string]string{"implement": "h1"}}
	if got := PromptKey("implement")(r); got != "h1" {
		t.Fatalf("PromptKey = %q, want h1", got)
	}
	if got := PromptKey("review")(r); got != None {
		t.Fatalf("PromptKey for absent role = %q, want %q", got, None)
	}
	if roles := PromptRoles(); !slices.Contains(roles, "implement") || !slices.Contains(roles, "butler") {
		t.Fatalf("PromptRoles = %v, want pass shapes and base-prompt kinds", roles)
	}
}

func TestRevertedAndChurnPercent(t *testing.T) {
	yes, no := true, false
	c1, c2 := 0.1, 0.3
	recs := []dispatchrecord.Record{
		{Reverted: &yes, Churn14d: &c1}, {Reverted: &no, Churn14d: &c2},
		{Reverted: &no}, {},
	}
	if pct, n := RevertedPercent(recs); n != 3 || pct < 33.3 || pct > 33.4 {
		t.Fatalf("RevertedPercent = %v of %d, want 33.3 of 3", pct, n)
	}
	if pct, n := MeanChurnPercent(recs); n != 2 || math.Abs(pct-20) > 1e-9 {
		t.Fatalf("MeanChurnPercent = %v of %d, want 20 of 2", pct, n)
	}
	if _, n := RevertedPercent(nil); n != 0 {
		t.Fatalf("RevertedPercent(nil) filled = %d, want 0", n)
	}
	if _, n := MeanChurnPercent([]dispatchrecord.Record{{}}); n != 0 {
		t.Fatalf("MeanChurnPercent unfilled filled = %d, want 0", n)
	}
}

func agentRecord() dispatchrecord.Record {
	return dispatchrecord.Record{DispatchKey: "1", Passes: []dispatchrecord.Pass{
		{
			Ordinal: 1, Role: "implement", Models: []string{"m"}, USD: 2, Turns: 3, DurationMs: 4000, APIDurationMs: 500,
			InputTokens: 30, OutputTokens: 40, CacheReadInputTokens: 50, CacheCreationInputTokens: 60, APICalls: 7,
			Verdict: "block", VerdictText: "no", Dispositions: "d", Log: "l.log",
			Agents: []dispatchrecord.PassAgent{
				{Agent: "main", APICalls: 3, InputTokens: 30, OutputTokens: 40, CacheReadInputTokens: 50, CacheCreationInputTokens: 60},
				{Agent: "worker", APICalls: 4, InputTokens: 20, CacheReadInputTokens: 30, CacheCreationInputTokens: 30},
			},
		},
		{
			Ordinal: 2, Role: "review", USD: 1, InputTokens: 1, OutputTokens: 2, APICalls: 1,
			Agents: []dispatchrecord.PassAgent{{Agent: "main", APICalls: 1, InputTokens: 1, OutputTokens: 2}},
		},
	}}
}

func TestGroupAgentsProjectsEachPassOntoItsAgent(t *testing.T) {
	rec := agentRecord()
	gs := GroupAgents([]dispatchrecord.Record{rec})
	if got, want := keysOf(gs), []string{"main", "worker"}; !slices.Equal(got, want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
	main, worker := gs[0].Records[0], gs[1].Records[0]
	if len(main.Passes) != 2 || len(worker.Passes) != 1 {
		t.Fatalf("passes: main %d, worker %d", len(main.Passes), len(worker.Passes))
	}
	if p := main.Passes[0]; p.USD != 2 || p.Verdict != "block" || p.Dispositions != "d" || p.Turns != 3 ||
		p.DurationMs != 4000 || p.APICalls != 3 || p.InputTokens != 30 || p.OutputTokens != 40 ||
		len(p.Agents) != 1 || p.Agents[0].Agent != "main" {
		t.Errorf("main projection = %+v", p)
	}
	w := worker.Passes[0]
	if w.USD != 0 || w.Verdict != "" || w.VerdictText != "" || w.Dispositions != "" ||
		w.Turns != 0 || w.DurationMs != 0 || w.APIDurationMs != 0 {
		t.Errorf("worker projection kept an unsplittable figure: %+v", w)
	}
	if w.APICalls != 4 || w.InputTokens != 20 || w.OutputTokens != 0 || w.CacheReadInputTokens != 30 ||
		w.CacheCreationInputTokens != 30 || len(w.Agents) != 1 || w.Agents[0].Agent != "worker" {
		t.Errorf("worker projection tokens = %+v", w)
	}
	if w.Role != "implement" || w.Ordinal != 1 || w.Log != "l.log" || !slices.Equal(w.Models, []string{"m"}) {
		t.Errorf("worker projection lost its identity: %+v", w)
	}
	if len(rec.Passes[0].Agents) != 2 || rec.Passes[0].USD != 2 {
		t.Fatal("grouping mutated the input Record")
	}
}

// The main row carries the Pass's own totals (claude's result usage covers the
// main loop only for input and cache tokens) and a subagent's row adds on top.
func TestGroupAgentsSumsToUngroupedTotalPlusSubagentTokens(t *testing.T) {
	recs := []dispatchrecord.Record{agentRecord()}
	_, _, wantUSD := AggregateRoles(recs)
	var usd float64
	var in, out, cr, cc, calls int
	for _, g := range GroupAgents(recs) {
		for _, r := range g.Records {
			usd += RecordUSD(r)
			for _, p := range r.Passes {
				in += p.InputTokens
				out += p.OutputTokens
				cr += p.CacheReadInputTokens
				cc += p.CacheCreationInputTokens
				calls += p.APICalls
			}
		}
	}
	if math.Abs(usd-wantUSD) > 1e-9 {
		t.Errorf("USD across groups = %v, want %v", usd, wantUSD)
	}
	// Ungrouped: 31/42/50/60 and 8 calls; the worker row adds 20/0/30/30 of its own.
	if in != 51 || out != 42 || cr != 80 || cc != 90 || calls != 8 {
		t.Errorf("tokens across groups = %d/%d/%d/%d calls %d, want 51/42/80/90 calls 8", in, out, cr, cc, calls)
	}
}

func TestGroupAgentsNoneAndOrdering(t *testing.T) {
	recs := []dispatchrecord.Record{
		{DispatchKey: "1", Passes: []dispatchrecord.Pass{
			{USD: 5, InputTokens: 9},
			{Agents: []dispatchrecord.PassAgent{{Agent: "scout", APICalls: 1}, {Agent: "main", APICalls: 1}}},
		}},
		{DispatchKey: "2"},
	}
	gs := GroupAgents(recs)
	if got, want := keysOf(gs), []string{"main", "scout", None}; !slices.Equal(got, want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
	none := gs[2].Records
	if len(none) != 2 || len(none[0].Passes) != 1 || none[0].Passes[0].USD != 5 || none[0].Passes[0].InputTokens != 9 {
		t.Fatalf("(none) group = %+v, want the unprojected agentless pass and the pass-less Record", none)
	}
}
