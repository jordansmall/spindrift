package waves

import (
	"reflect"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// MaxJobs unset (0) is the uncapped drain case, not a disabled one (ADR 0019).
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

// In-batch blocker edges do not change the mode. The per-issue readiness gate
// in drainMaxJobs holds a blocked dependent for the next invocation instead of
// looping waves in-process.
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

// #524 reroutes selective-list dispatch off the old multi-wave loop onto the
// same at-most-one-wave drain shape as the queue path, regardless of MaxJobs
// (ADR 0019).
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

// NewPlan is the one place the cycle check happens. Run, selective dispatch,
// and preview all rely on it instead of repeating the check themselves.
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

// The cycle check runs before mode selection, so no path dispatches a single
// issue out of a cyclic batch.
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

// Plan.Origin is the explicit replacement for the old issueNumber != ""
// sentinel, so every Origin value has to survive NewPlan unchanged.
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

// drainMaxJobs (#1103) reads Plan.Failed to hold an issue whose own
// NewReadiness/DepsOf call errored, rather than treating the missing Edges
// entry as a confirmed zero-blocker issue.
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

func planIssueNumber(i Issue) string { return i.Number }

// ADR 0040 orders a discovered batch Critical, High, Normal, Low regardless of
// the input order.
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

// A stable sort preserves oldest-first order within a tier, because every
// Issue Tracker adapter hands NewPlan its issues oldest-first.
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

// The sort only reorders the slice using each issue's own field. It never
// derives or mutates a Priority from Edges, so a Critical dependent does not
// lift its Low blocker.
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

// The sort is blind to edges, so a Critical dependent sorting ahead of its Low
// blocker is expected. drainMaxJobs enforces the dependency later off Edges,
// which is why Edges must survive the sort unchanged.
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

// The issues deliberately leave Priority unset, so this is the common case
// where no agent-priority-* label is in use. That batch must come out in the
// input order, unchanged by the sort.
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

// ADR 0040: a selective list keeps the operator's typed order, so priority
// never reorders it.
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

// The loop runs two distinct Origin values so a hardcoded Origin cannot pass.
func TestNewInput_AssemblesBatchFromReadiness(t *testing.T) {
	issues := []Issue{{Number: "1", Title: "a"}, {Number: "2", Title: "b"}}
	readiness := Readiness{
		Edges:   map[string][]string{"2": {"1"}},
		Sources: Sources{"2": {"1": forge.DepSourceNative}},
		Failed:  map[string]bool{"3": true},
	}

	for _, origin := range []Origin{OriginDiscovered, OriginSelective} {
		in := NewInput(origin, readiness, issues)

		if in.Origin != origin {
			t.Errorf("origin %v: Origin = %v, want %v", origin, in.Origin, origin)
		}
		if !reflect.DeepEqual(in.Issues, issues) {
			t.Errorf("origin %v: Issues = %v, want %v", origin, in.Issues, issues)
		}
		if !reflect.DeepEqual(in.Edges, readiness.Edges) {
			t.Errorf("origin %v: Edges = %v, want %v", origin, in.Edges, readiness.Edges)
		}
		if !reflect.DeepEqual(in.Sources, readiness.Sources) {
			t.Errorf("origin %v: Sources = %v, want %v", origin, in.Sources, readiness.Sources)
		}
		if !reflect.DeepEqual(in.Failed, readiness.Failed) {
			t.Errorf("origin %v: Failed = %v, want %v", origin, in.Failed, readiness.Failed)
		}
	}
}
