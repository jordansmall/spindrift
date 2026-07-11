package waves

import "testing"

func TestNewPlan_NoEdges_DefaultsToWavesMode(t *testing.T) {
	cfg := Config{}
	in := Input{
		Origin: OriginDiscovered,
		Issues: []Issue{{Number: "1", Title: "a"}},
	}
	plan, err := NewPlan(cfg, in)
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	if plan.Mode != ModeWaves {
		t.Errorf("Mode = %v, want ModeWaves", plan.Mode)
	}
}

// TestNewPlan_Edges_OrderedWaves verifies a batch with in-batch blocker
// edges (and MaxJobs unset) still selects ModeWaves — the dependency-order
// engine, not the drain cap — carrying the edges through unchanged for Run
// to order.
func TestNewPlan_Edges_OrderedWaves(t *testing.T) {
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
	if plan.Mode != ModeWaves {
		t.Errorf("Mode = %v, want ModeWaves", plan.Mode)
	}
	if len(plan.Edges["2"]) != 1 || plan.Edges["2"][0] != "1" {
		t.Errorf("Edges not carried through: %v", plan.Edges)
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
