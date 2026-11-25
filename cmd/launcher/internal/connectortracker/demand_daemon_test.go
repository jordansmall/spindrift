package connectortracker

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/daemon"
	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/forge"
)

// loopClock is a fake daemon.Clock whose Sleep advances time.
type loopClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *loopClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *loopClock) Sleep(_ context.Context, d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type doneChild struct{}

func (doneChild) Wait(<-chan struct{}) (daemon.ChildResult, error) { return daemon.ChildResult{}, nil }
func (doneChild) State() daemon.ChildState                         { return daemon.ChildState{} }

// countRunner serves the pool's demand probe from a tracker's DemandCounter.
type countRunner struct {
	counter forge.DemandCounter
	clk     *loopClock
	probes  []time.Time
	starts  int
	onStart func()
}

func (r *countRunner) ResolveTip(context.Context) (daemon.Tip, error) {
	return daemon.Tip{Revision: "rev1"}, nil
}

func (r *countRunner) Demand(_ context.Context, _ daemon.Kind, fresh bool, _ dispatchkey.Set) (daemon.Demand, error) {
	r.probes = append(r.probes, r.clk.Now())
	n, err := r.counter.CountReady(fresh)
	return daemon.Demand{Ready: n}, err
}

func (r *countRunner) StartChild(context.Context, daemon.ChildRequest) (daemon.ChildHandle, error) {
	r.starts++
	r.onStart()
	return doneChild{}, nil
}

func (r *countRunner) InheritChild(context.Context, daemon.ChildRequest, daemon.ChildState) (daemon.ChildHandle, error) {
	return doneChild{}, nil
}

func (r *countRunner) Prebuild(context.Context, daemon.Tip) error { return nil }

func (r *countRunner) SweepLateMerges(context.Context, string) error { return nil }

// A tracker Connector that answers rate-limited holds the daemon's demand
// probe, as a forge rate limit does: the Connector's limit names no reset, so
// the pool pauses four probe intervals, then resumes and starts a child.
func TestConnectorRateLimitHoldsAndResumesTheDemandProbe(t *testing.T) {
	call, _ := stubGuestErrs(t,
		map[string]string{
			"tracker.countInterval": `{"seconds":1}`,
			"tracker.count":         `{"count":1}`,
		},
		map[string][]error{"tracker.count": {&connectorhost.CallError{Class: connectorhost.RateLimited, Message: "slow down"}}})
	c := &connectorhost.Connector{Capabilities: []string{"tracker", CapCount}, ProtocolVersion: "0.2.0"}
	fake, _ := fakeClock()
	tr, err := NewCalling(c, call, testLifecycle, testTarget, fake)
	if err != nil {
		t.Fatal(err)
	}
	counter, ok := forge.As[forge.DemandCounter](tr)
	if !ok {
		t.Fatal("tracker has no DemandCounter")
	}
	interval := counter.ProbeInterval()

	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	clk := &loopClock{now: t0}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &countRunner{counter: counter, clk: clk, onStart: cancel}
	work := daemon.KindOf(dispatchkind.Work)
	cfg := daemon.Config{
		Kinds:            []daemon.Kind{work},
		ProbeIntervals:   map[daemon.Kind]time.Duration{work: interval},
		Slots:            1,
		IdleFloor:        time.Minute,
		IdleCap:          time.Hour,
		FailureBackoff:   time.Minute,
		BreakerThreshold: 3,
		BreakerWindow:    time.Hour,
	}

	var out bytes.Buffer
	daemon.Loop(ctx, cfg, r, daemon.NewEmitter(&out, &out, clk.Now), clk)

	if len(r.probes) != 2 || !r.probes[0].Equal(t0) || r.probes[1].Sub(r.probes[0]) != 4*interval {
		t.Fatalf("probes at %v, want two, the second four intervals (%s) after the first (log: %s)", r.probes, 4*interval, out.String())
	}
	if r.starts != 1 {
		t.Errorf("children started = %d, want 1 after the hold (log: %s)", r.starts, out.String())
	}
}
