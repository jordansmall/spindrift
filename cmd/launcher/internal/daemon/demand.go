package daemon

import (
	"context"
	"fmt"
)

// Demand is a kind's tracker-side answer to "is there anything for a child
// to start": the count of startable items. It is advisory — children still
// discover and claim for themselves — so a stale or wrong count costs an
// empty child or a delayed start, never a wrong claim.
type Demand struct {
	Ready int
}

// demandFlight is one in-flight Runner.Demand call for a kind, shared by
// every slot that finds the kind stale while it runs, the same shape as
// hostRunner's tipFlight. Joiners need no result: they re-decide on the
// schedule the leader's Observe already updated.
type demandFlight struct {
	done chan struct{}
}

// probeResult tells runSlot what to do after a probe round.
type probeResult int

const (
	probeDecideAgain probeResult = iota // re-Decide: the schedule has new facts
	probeRetop                          // back to the top of the loop: this slot backed off, ctx died, or the window shut
	probeHalted                         // the pool halted; return
)

// probe refreshes Demand for each of kinds and reports what the slot does
// next. A kind is probed once however many slots ask at once: the first
// caller leads and calls Runner.Demand, the rest wait for its answer and
// simply re-decide on the Observe the leader made.
//
// A leader's error counts toward the breaker like a ResolveTip failure
// (backoffOrHalt): a tracker that cannot even be counted is the systemic
// fault the breaker exists for. It still Observes DemandFailed first, so the
// kind rests one interval instead of every slot re-probing it at once.
//
// The Awake window is re-checked before every kind: a probe in flight can
// outlive the window, and the slot must park in awaitWindow (probeRetop)
// rather than ask the tracker about the next kind while it is shut.
func (p *pool) probe(ctx context.Context, slot int, kinds []Kind) probeResult {
	for _, kind := range kinds {
		if wait, _ := p.cfg.Awake.Until(p.clk.Now()); wait > 0 {
			return probeRetop
		}
		led, err := p.probeKind(ctx, slot, kind)
		if !led {
			if err != nil {
				return probeRetop
			}
			continue
		}
		if err != nil {
			if p.backoffOrHalt(ctx, slot, kind, "", fmt.Sprintf("demand: %v", err), batonPassFailed) {
				return probeHalted
			}
			return probeRetop
		}
	}
	return probeDecideAgain
}

// probeKind runs or joins kind's Demand flight. led reports this call was the
// leader; err is the leader's Demand error, or a joiner's ctx error.
func (p *pool) probeKind(ctx context.Context, slot int, kind Kind) (led bool, err error) {
	p.mu.Lock()
	if f := p.demandFlights[kind]; f != nil {
		p.mu.Unlock()
		if p.onDemandJoin != nil {
			p.onDemandJoin()
		}
		// ctx.Done, not just f.done: a joiner left waiting on another slot's
		// call after its own ctx died is the same hang hostRunner.ResolveTip's
		// joiners avoid.
		select {
		case <-f.done:
			return false, nil
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	f := &demandFlight{done: make(chan struct{})}
	p.demandFlights[kind] = f
	p.mu.Unlock()

	// Deferred so a panic in Demand still clears the flight and releases
	// the joiners. Cleared before done closes: the window between the two
	// only ever starts a fresh, legitimate flight.
	defer func() {
		p.mu.Lock()
		delete(p.demandFlights, kind)
		p.mu.Unlock()
		close(f.done)
	}()

	d, err := p.r.Demand(ctx, kind)
	now := p.clk.Now()
	if err != nil {
		p.mutate(func(s *state) []Event {
			s.sched, _ = s.sched.Observe(now, DemandFailed{Kind: kind})
			return nil
		})
		return true, err
	}
	p.mutate(func(s *state) []Event {
		// Zero when never probed, so a first probe finding work reads as
		// appearing and one finding none as nothing at all.
		prev := s.sched.View(kind, now).Counted
		s.sched, _ = s.sched.Observe(now, DemandProbed{Kind: kind, Ready: d.Ready})
		switch {
		case prev == 0 && d.Ready > 0:
			return []Event{{Event: "demand_appeared", Kind: kind, Slot: intPtr(slot), Ready: intPtr(d.Ready)}}
		case prev > 0 && d.Ready == 0:
			return []Event{{Event: "demand_drained", Kind: kind, Slot: intPtr(slot), Ready: intPtr(d.Ready)}}
		}
		return nil
	})
	return true, nil
}
