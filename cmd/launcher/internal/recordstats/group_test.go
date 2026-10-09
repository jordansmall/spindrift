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
