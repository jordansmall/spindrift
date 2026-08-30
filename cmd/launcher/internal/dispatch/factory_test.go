package dispatch

import (
	"testing"

	"spindrift.dev/launcher/internal/runner"
)

// TestFactory_AgentGenerationNilBeforeSet verifies that a freshly constructed
// Factory's AgentGeneration() returns nil until SetAgentGeneration is ever
// called -- nil means "use the runner adapter's own startup-baked default",
// matching runner.Box.ClosureGeneration's own nil-means-default contract.
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

// TestFactory_SetAgentGenerationThenGet verifies SetAgentGeneration/
// AgentGeneration round-trip the same pointer.
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

// TestFactory_SetAgentGenerationAfterNewStillApplies verifies that, unlike
// SetHeartbeatOut, SetAgentGeneration carries no before-any-New() panic
// guard: a hot-swap must be able to land after dispatching has already
// started, so calling it after New() must not panic.
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

// TestRun_BoxClosureGenerationDefaultNil verifies that a Factory that never
// calls SetAgentGeneration still produces Boxes with ClosureGeneration ==
// nil -- today's unchanged default behavior for every non-bwrap-hotswap
// caller.
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

// TestRun_BoxClosureGenerationSnapshottedAtNew verifies the end-to-end wire:
// a generation set on the Factory before New() reaches the runner.Box the
// runner adapter actually receives, as ClosureGeneration.
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

// TestDispatch_KeepsAgentGenerationSnapshotFromNewDespiteLaterSwap verifies
// issue #2682's acceptance criterion end-to-end: a Dispatch minted before a
// hot-swap lands must keep launching Boxes with the generation it snapshotted
// at its own New() -- even after Factory.SetAgentGeneration is called again
// later and even across a Run() that happens after that later call -- while a
// Dispatch minted after the swap picks up the new generation.
func TestDispatch_KeepsAgentGenerationSnapshotFromNewDespiteLaterSwap(t *testing.T) {
	dir := tempLogDir(t)

	fr := runner.NewFake()
	f, err := NewFactory(Config{}, dir, fr, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	// d1 is minted before any swap -- its snapshot is nil.
	d1 := f.New("1", "pre-swap")

	// A hot-swap lands after d1 was already minted.
	gen := runner.NewAgentGeneration("/nix/store/swap-agent-closure")
	f.SetAgentGeneration(&gen)

	// d1 must still finish on what it started with (nil), not the swap.
	if result := d1.Run(); !result.Success {
		t.Fatalf("d1.Run: want Success=true, got %+v", result)
	}
	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls after d1.Run: want 1, got %d", len(fr.RunCalls))
	}
	if got := fr.RunCalls[0].ClosureGeneration; got != nil {
		t.Errorf("d1 Box.ClosureGeneration after later swap: want nil (snapshot from before swap), got %+v", got)
	}

	// d2 is minted after the swap -- it must pick up the new generation.
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
