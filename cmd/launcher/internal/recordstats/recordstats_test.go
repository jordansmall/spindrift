package recordstats

import (
	"testing"

	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/passmachine"
)

func TestAggregateRolesOrdersAndTotals(t *testing.T) {
	rec := dispatchrecord.Record{Passes: []dispatchrecord.Pass{
		{Role: "zeta", USD: 1},
		{Role: string(passmachine.RoleReview), USD: 2, Verdict: string(passmachine.VerdictBlock)},
		{Role: string(passmachine.RoleReview), USD: 3, Verdict: string(passmachine.VerdictApprove)},
		{Role: string(passmachine.RoleImplement), USD: 4},
		{USD: 5},
	}}
	rows, passes, usd := AggregateRoles([]dispatchrecord.Record{rec})
	var got []string
	for _, r := range rows {
		got = append(got, r.Role)
	}
	want := []string{"implement", "review", None, "zeta"}
	if len(got) != len(want) {
		t.Fatalf("roles = %v, want %v", got, want)
	}
	if passes != 5 || usd != 15 {
		t.Fatalf("passes=%d usd=%v, want 5 and 15", passes, usd)
	}
	if br := rows[1].BlockRate(); br != "50%" {
		t.Fatalf("review BlockRate = %q, want 50%%", br)
	}
	if br := rows[0].BlockRate(); br != "-" {
		t.Fatalf("implement BlockRate = %q, want -", br)
	}
}

func TestRecordUSDSumsPasses(t *testing.T) {
	r := dispatchrecord.Record{Passes: []dispatchrecord.Pass{{USD: 1.5}, {USD: 0.25}}}
	if got := RecordUSD(r); got != 1.75 {
		t.Fatalf("RecordUSD = %v, want 1.75", got)
	}
	if got := RecordUSD(dispatchrecord.Record{}); got != 0 {
		t.Fatalf("RecordUSD of no passes = %v, want 0", got)
	}
}

// TestAggregateRolesSumsAPICalls pins the figure `stats --by role` shows per
// role: each pass's pass_usage-derived APICalls, summed across the role's passes.
func TestAggregateRolesSumsAPICalls(t *testing.T) {
	rec := dispatchrecord.Record{Passes: []dispatchrecord.Pass{
		{Role: string(passmachine.RoleImplement), APICalls: 13},
		{Role: string(passmachine.RoleImplement), APICalls: 4},
		{Role: string(passmachine.RoleReview), APICalls: 2},
	}}
	rows, _, _ := AggregateRoles([]dispatchrecord.Record{rec})
	if len(rows) != 2 || rows[0].APICalls != 17 || rows[1].APICalls != 2 {
		t.Fatalf("rows = %+v, want implement APICalls=17 and review APICalls=2", rows)
	}
}
