package dispatch

import (
	"testing"

	"spindrift.dev/launcher/internal/runner"
)

// A nil generation means "use the runner adapter's own startup-baked default",
// matching runner.Box.ClosureGeneration's nil-means-default contract.
func TestFactory_AgentGenerationNilBeforeSet(t *testing.T) {
	dir := tempLogDir(t)

	fr := runner.NewFake()
	f, err := NewFactory(Config{}, dir, fr, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	if got := f.AgentGeneration(); got != nil {
		t.Errorf("AgentGeneration before any SetAgentGeneration: want nil, got %+v", got)
	}
}

func TestFactory_SetAgentGenerationThenGet(t *testing.T) {
	dir := tempLogDir(t)

	fr := runner.NewFake()
	f, err := NewFactory(Config{}, dir, fr, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	gen := runner.NewAgentGeneration("/nix/store/abc-agent-files")
	f.SetAgentGeneration(&gen)

	got := f.AgentGeneration()
	if got != &gen {
		t.Errorf("AgentGeneration after SetAgentGeneration: want %p, got %p", &gen, got)
	}
}

// Unlike SetHeartbeatOut, SetAgentGeneration carries no before-any-New() panic
// guard: a hot-swap has to be able to land after dispatching already started.
func TestFactory_SetAgentGenerationAfterNewStillApplies(t *testing.T) {
	dir := tempLogDir(t)

	fr := runner.NewFake()
	f, err := NewFactory(Config{}, dir, fr, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	f.New("1", "first dispatch")

	gen := runner.NewAgentGeneration("/nix/store/def-agent-files")
	f.SetAgentGeneration(&gen)

	if got := f.AgentGeneration(); got != &gen {
		t.Errorf("AgentGeneration after New() then SetAgentGeneration: want %p, got %p", &gen, got)
	}
}

// Every non-bwrap-hotswap caller depends on this unchanged nil default.
func TestRun_BoxClosureGenerationDefaultNil(t *testing.T) {
	dir := tempLogDir(t)

	fr := runner.NewFake()
	f, err := NewFactory(Config{}, dir, fr, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	d := f.New("99", "no generation set")
	if result := d.Run(); !result.Success {
		t.Fatalf("Run: want Success=true, got %+v", result)
	}

	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: want 1, got %d", len(fr.RunCalls))
	}
	if got := fr.RunCalls[0].ClosureGeneration; got != nil {
		t.Errorf("Box.ClosureGeneration with no SetAgentGeneration: want nil, got %+v", got)
	}
}

func TestRun_BoxClosureGenerationSnapshottedAtNew(t *testing.T) {
	dir := tempLogDir(t)

	fr := runner.NewFake()
	f, err := NewFactory(Config{}, dir, fr, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	gen := runner.NewAgentGeneration("/nix/store/ghi-agent-files")
	f.SetAgentGeneration(&gen)

	d := f.New("100", "generation set before New")
	if result := d.Run(); !result.Success {
		t.Fatalf("Run: want Success=true, got %+v", result)
	}

	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: want 1, got %d", len(fr.RunCalls))
	}
	if got := fr.RunCalls[0].ClosureGeneration; got != &gen {
		t.Errorf("Box.ClosureGeneration: want %p, got %p", &gen, got)
	}
}

// Pins issue #2682: a Dispatch minted before a hot-swap keeps launching Boxes
// with the generation it snapshotted at its own New(), even across a Run() that
// happens after a later SetAgentGeneration, while a Dispatch minted after the
// swap picks up the new generation.
func TestDispatch_KeepsAgentGenerationSnapshotFromNewDespiteLaterSwap(t *testing.T) {
	dir := tempLogDir(t)

	fr := runner.NewFake()
	f, err := NewFactory(Config{}, dir, fr, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	d1 := f.New("1", "pre-swap")

	gen := runner.NewAgentGeneration("/nix/store/swap-agent-closure")
	f.SetAgentGeneration(&gen)

	if result := d1.Run(); !result.Success {
		t.Fatalf("d1.Run: want Success=true, got %+v", result)
	}
	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls after d1.Run: want 1, got %d", len(fr.RunCalls))
	}
	if got := fr.RunCalls[0].ClosureGeneration; got != nil {
		t.Errorf("d1 Box.ClosureGeneration after later swap: want nil (snapshot from before swap), got %+v", got)
	}

	d2 := f.New("2", "post-swap")
	if result := d2.Run(); !result.Success {
		t.Fatalf("d2.Run: want Success=true, got %+v", result)
	}
	if len(fr.RunCalls) != 2 {
		t.Fatalf("RunCalls after d2.Run: want 2, got %d", len(fr.RunCalls))
	}
	if got := fr.RunCalls[1].ClosureGeneration; got != &gen {
		t.Errorf("d2 Box.ClosureGeneration: want %p, got %p", &gen, got)
	}
}
