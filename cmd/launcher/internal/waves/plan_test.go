package waves

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// TestNewPlan_Discovered_NoEdges_SelectsDrainMode verifies a label-discovered
// batch always selects ModeDrain, even with MaxJobs unset (0) — MAX_JOBS=0 is
// the uncapped drain case (ADR 0019).
func TestNewPlan_Discovered_NoEdges_SelectsDrainMode(t *testing.T) {
	cfg := Config{}
	in := Input{
		Origin: OriginDiscovered,
		Batch:  Batch{Issues: []Issue{{Number: "1", Title: "a"}}},
	}
	plan, err := NewPlan(cfg, in)
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	if plan.Mode != ModeDrain {
		t.Errorf("Mode = %v, want ModeDrain", plan.Mode)
	}
}

// TestNewPlan_Discovered_Edges_SelectsDrainMode verifies a batch with
// in-batch blocker edges also selects ModeDrain for OriginDiscovered — the
// per-issue readiness gate in drainMaxJobs holds a blocked dependent for the
// next invocation instead of looping waves in-process.
func TestNewPlan_Discovered_Edges_SelectsDrainMode(t *testing.T) {
	cfg := Config{}
	in := Input{
		Origin: OriginDiscovered,
		Batch:  Batch{Issues: []Issue{{Number: "1"}, {Number: "2"}}, Edges: map[string][]string{"2": {"1"}}},
	}
	plan, err := NewPlan(cfg, in)
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	if plan.Mode != ModeDrain {
		t.Errorf("Mode = %v, want ModeDrain", plan.Mode)
	}
	if len(plan.Edges["2"]) != 1 || plan.Edges["2"][0] != "1" {
		t.Errorf("Edges not carried through: %v", plan.Edges)
	}
}

// TestNewPlan_Selective_NoEdges_SelectsDrainMode verifies OriginSelective
// selects ModeDrain regardless of MaxJobs — #524 reroutes selective-list
// dispatch off the old multi-wave loop onto the same at-most-one-wave drain
// shape as the queue path (ADR 0019).
func TestNewPlan_Selective_NoEdges_SelectsDrainMode(t *testing.T) {
	cfg := Config{}
	in := Input{
		Origin: OriginSelective,
		Batch:  Batch{Issues: []Issue{{Number: "1", Title: "a"}}},
	}
	plan, err := NewPlan(cfg, in)
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	if plan.Mode != ModeDrain {
		t.Errorf("Mode = %v, want ModeDrain", plan.Mode)
	}
}

// TestNewPlan_Cycle_ReturnsError verifies a cyclic in-batch dependency graph
// is reported as an error rather than a Plan — this is the one place the
// cycle check happens; Run, selective dispatch, and preview all rely on it
// instead of repeating the check themselves.
func TestNewPlan_Cycle_ReturnsError(t *testing.T) {
	cfg := Config{}
	in := Input{
		Origin: OriginDiscovered,
		Batch:  Batch{Issues: []Issue{{Number: "1"}, {Number: "2"}}, Edges: map[string][]string{"1": {"2"}, "2": {"1"}}},
	}
	_, err := NewPlan(cfg, in)
	if err == nil {
		t.Fatal("NewPlan: want cycle error, got nil")
	}
}

// TestNewPlan_Cycle_ReturnsError_EvenWithMaxJobs verifies the cycle check
// runs before mode selection: a drain-eligible batch (MaxJobs > 0) with a
// cyclic dependency graph still errors rather than producing a ModeDrain
// Plan — no path dispatches a single issue out of a cyclic batch.
func TestNewPlan_Cycle_ReturnsError_EvenWithMaxJobs(t *testing.T) {
	cfg := Config{MaxJobs: 1}
	in := Input{
		Origin: OriginDiscovered,
		Batch:  Batch{Issues: []Issue{{Number: "1"}, {Number: "2"}}, Edges: map[string][]string{"1": {"2"}, "2": {"1"}}},
	}
	_, err := NewPlan(cfg, in)
	if err == nil {
		t.Fatal("NewPlan: want cycle error, got nil")
	}
}

// TestNewPlan_MaxJobs_SelectsDrainMode verifies cfg.MaxJobs > 0 selects
// ModeDrain regardless of edges.
func TestNewPlan_MaxJobs_SelectsDrainMode(t *testing.T) {
	cfg := Config{MaxJobs: 2}
	in := Input{
		Origin: OriginDiscovered,
		Batch:  Batch{Issues: []Issue{{Number: "1"}, {Number: "2"}, {Number: "3"}}},
	}
	plan, err := NewPlan(cfg, in)
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	if plan.Mode != ModeDrain {
		t.Errorf("Mode = %v, want ModeDrain", plan.Mode)
	}
}

// TestNewPlan_OriginPropagates verifies Plan.Origin carries through every
// Origin value unchanged — the explicit replacement for the old
// issueNumber != "" sentinel.
func TestNewPlan_OriginPropagates(t *testing.T) {
	for _, origin := range []Origin{OriginDiscovered, OriginClaimed, OriginSelective} {
		in := Input{Origin: origin, Batch: Batch{Issues: []Issue{{Number: "1"}}}}
		plan, err := NewPlan(Config{}, in)
		if err != nil {
			t.Fatalf("NewPlan: %v", err)
		}
		if plan.Origin != origin {
			t.Errorf("Origin = %v, want %v", plan.Origin, origin)
		}
	}
}

// TestNewPlan_FailedPropagates verifies NewPlan carries Input.Failed through
// to Plan.Failed unchanged — drainMaxJobs (#1103) reads it off the Plan to
// hold an issue whose own NewReadiness/DepsOf call errored, rather than
// treating the missing Edges entry as a confirmed zero-blocker issue.
func TestNewPlan_FailedPropagates(t *testing.T) {
	cfg := Config{}
	in := Input{
		Origin: OriginDiscovered,
		Batch:  Batch{Issues: []Issue{{Number: "1", Title: "a"}}, Failed: map[string]bool{"1": true}},
	}
	plan, err := NewPlan(cfg, in)
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	if !plan.Failed["1"] {
		t.Errorf("Plan.Failed = %v, want it to carry issue 1", plan.Failed)
	}
}

// planIssueNumber extracts an Issue's Number field, passed to
// forge.Numbers below for concise ordering assertions.
func planIssueNumber(i Issue) string { return i.Number }

// TestNewPlan_SortsByPriorityDescending verifies a mixed-priority
// OriginDiscovered batch sorts to Critical, High, Normal, Low regardless of
// input order (ADR 0040).
func TestNewPlan_SortsByPriorityDescending(t *testing.T) {
	cfg := Config{}
	in := Input{
		Origin: OriginDiscovered,
		Batch: Batch{Issues: []Issue{
			{Number: "1", Priority: forge.PriorityNormal},
			{Number: "2", Priority: forge.PriorityCritical},
			{Number: "3", Priority: forge.PriorityLow},
			{Number: "4", Priority: forge.PriorityHigh},
		}},
	}
	plan, err := NewPlan(cfg, in)
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	got := forge.Numbers(plan.Issues, planIssueNumber)
	want := []string{"2", "4", "1", "3"}
	if len(got) != len(want) {
		t.Fatalf("Issues order = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Issues order = %v, want %v", got, want)
			break
		}
	}
}

// TestNewPlan_SortIsStableWithinTier verifies equal-priority issues keep
// their original relative (oldest-first, since every Issue Tracker adapter
// returns issues oldest-first) order after the priority sort.
func TestNewPlan_SortIsStableWithinTier(t *testing.T) {
	cfg := Config{}
	in := Input{
		Origin: OriginDiscovered,
		Batch: Batch{Issues: []Issue{
			{Number: "10", Priority: forge.PriorityHigh},
			{Number: "5", Priority: forge.PriorityHigh},
			{Number: "7", Priority: forge.PriorityHigh},
		}},
	}
	plan, err := NewPlan(cfg, in)
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	got := forge.Numbers(plan.Issues, planIssueNumber)
	want := []string{"10", "5", "7"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Issues order = %v, want %v (input order preserved within tier)", got, want)
			break
		}
	}
}

// TestNewPlan_LowSortsLast verifies every Low-priority issue sorts after
// every Normal-priority issue, no matter how the input interleaves them.
func TestNewPlan_LowSortsLast(t *testing.T) {
	cfg := Config{}
	in := Input{
		Origin: OriginDiscovered,
		Batch: Batch{Issues: []Issue{
			{Number: "1", Priority: forge.PriorityLow},
			{Number: "2", Priority: forge.PriorityNormal},
			{Number: "3", Priority: forge.PriorityLow},
			{Number: "4", Priority: forge.PriorityNormal},
		}},
	}
	plan, err := NewPlan(cfg, in)
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	seenLow := false
	for _, iss := range plan.Issues {
		if iss.Priority == forge.PriorityNormal && seenLow {
			t.Fatalf("Normal issue #%s sorted after a Low issue: %v", iss.Number, forge.Numbers(plan.Issues, planIssueNumber))
		}
		if iss.Priority == forge.PriorityLow {
			seenLow = true
		}
	}
}

// TestNewPlan_PriorityNeverInherited verifies a blocked Low-priority issue's
// own Priority field is unchanged after NewPlan even though its dependent is
// Critical — priority sort never derives or mutates a Priority value from
// Edges, it only reorders the slice using each issue's own field.
func TestNewPlan_PriorityNeverInherited(t *testing.T) {
	cfg := Config{}
	in := Input{
		Origin: OriginDiscovered,
		Batch: Batch{Issues: []Issue{
			{Number: "1", Priority: forge.PriorityLow},      // blocker
			{Number: "2", Priority: forge.PriorityCritical}, // dependent
		}, Edges: map[string][]string{"2": {"1"}}},
	}
	plan, err := NewPlan(cfg, in)
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	for _, iss := range plan.Issues {
		if iss.Number == "1" && iss.Priority != forge.PriorityLow {
			t.Errorf("blocker #1 Priority = %v, want unchanged PriorityLow", iss.Priority)
		}
	}
}

// TestNewPlan_EdgesCarriedThroughUnchangedByPrioritySort verifies that, even
// though a Critical-priority dependent sorts ahead of its Low-priority
// blocker in plan.Issues (sort is blind to edges — that's expected), Edges
// itself is carried through unchanged so a downstream drainMaxJobs readiness
// check still holds the dependent back regardless of its position in the
// sorted slice. Actual dependency enforcement happens in drainMaxJobs, not
// in NewPlan's sort; NewPlan's job here is only to prove priority sorting
// never corrupts or overrides the edges data blocking still relies on
// downstream.
func TestNewPlan_EdgesCarriedThroughUnchangedByPrioritySort(t *testing.T) {
	cfg := Config{}
	edges := map[string][]string{"2": {"1"}}
	in := Input{
		Origin: OriginDiscovered,
		Batch: Batch{Issues: []Issue{
			{Number: "1", Priority: forge.PriorityLow},      // blocker
			{Number: "2", Priority: forge.PriorityCritical}, // dependent, blocked
		}, Edges: edges},
	}
	plan, err := NewPlan(cfg, in)
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	got := forge.Numbers(plan.Issues, planIssueNumber)
	if len(got) != 2 || got[0] != "2" || got[1] != "1" {
		t.Fatalf("Issues order = %v, want [2 1] (Critical dependent sorts ahead of its Low blocker)", got)
	}
	if len(plan.Edges["2"]) != 1 || plan.Edges["2"][0] != "1" {
		t.Errorf("Edges not carried through unchanged: %v", plan.Edges)
	}
}

// TestNewPlan_UnlabeledBatchByteIdenticalOrder verifies an all-PriorityNormal
// (zero value; issues constructed without setting Priority at all) batch in
// an arbitrary Number order sorts to the exact same order as the input —
// zero behaviour change for the common case (no agent-priority-* labels in
// use).
func TestNewPlan_UnlabeledBatchByteIdenticalOrder(t *testing.T) {
	cfg := Config{}
	in := Input{
		Origin: OriginDiscovered,
		Batch: Batch{Issues: []Issue{
			{Number: "42"},
			{Number: "7"},
			{Number: "13"},
			{Number: "1"},
		}},
	}
	want := []string{"42", "7", "13", "1"}
	plan, err := NewPlan(cfg, in)
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	got := forge.Numbers(plan.Issues, planIssueNumber)
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Issues order = %v, want %v (byte-identical to input)", got, want)
			break
		}
	}
}

// TestNewPlan_SelectiveNeverReordersByPriority verifies OriginSelective —
// the operator's hand-picked list — is never reordered by priority (ADR
// 0040: a selective list keeps the operator's typed order).
func TestNewPlan_SelectiveNeverReordersByPriority(t *testing.T) {
	cfg := Config{}
	in := Input{
		Origin: OriginSelective,
		Batch: Batch{Issues: []Issue{
			{Number: "1", Priority: forge.PriorityLow},
			{Number: "2", Priority: forge.PriorityCritical},
			{Number: "3", Priority: forge.PriorityNormal},
		}},
	}
	want := []string{"1", "2", "3"}
	plan, err := NewPlan(cfg, in)
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	got := forge.Numbers(plan.Issues, planIssueNumber)
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Issues order = %v, want %v (selective order untouched)", got, want)
			break
		}
	}
}
