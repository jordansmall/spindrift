package waves

import "testing"

// TestNewPlan_Discovered_NoEdges_SelectsDrainMode verifies a label-discovered
// batch always selects ModeDrain, even with MaxJobs unset (0) — MAX_JOBS=0 is
// the uncapped drain case (ADR 0019).
func TestNewPlan_Discovered_NoEdges_SelectsDrainMode(t *testing.T) {
	cfg := Config{}
	in := Input{
		Origin: OriginDiscovered,
		Issues: []Issue{{Number: "1", Title: "a"}},
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
		Issues: []Issue{{Number: "1"}, {Number: "2"}},
		Edges:  map[string][]string{"2": {"1"}},
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
		Issues: []Issue{{Number: "1", Title: "a"}},
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
		Issues: []Issue{{Number: "1"}, {Number: "2"}},
		Edges:  map[string][]string{"1": {"2"}, "2": {"1"}},
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
		Issues: []Issue{{Number: "1"}, {Number: "2"}},
		Edges:  map[string][]string{"1": {"2"}, "2": {"1"}},
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
		Issues: []Issue{{Number: "1"}, {Number: "2"}, {Number: "3"}},
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
		in := Input{Origin: origin, Issues: []Issue{{Number: "1"}}}
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
// hold an issue whose own BuildEdges/DepsOf call errored, rather than
// treating the missing Edges entry as a confirmed zero-blocker issue.
func TestNewPlan_FailedPropagates(t *testing.T) {
	cfg := Config{}
	in := Input{
		Origin: OriginDiscovered,
		Issues: []Issue{{Number: "1", Title: "a"}},
		Failed: map[string]bool{"1": true},
	}
	plan, err := NewPlan(cfg, in)
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	if !plan.Failed["1"] {
		t.Errorf("Plan.Failed = %v, want it to carry issue 1", plan.Failed)
	}
}
