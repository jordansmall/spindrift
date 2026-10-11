package daemon

import "slices"

// commitHandoffIfClean commits the in-place handoff (ADR 0063) when s has
// reached a clean moment: the build passed and the pool is waiting, nothing is
// halting or stopping, the discovery baton is free, and every running child
// has sent its first report. mutate calls it after every state change, so the
// change that makes the moment clean also commits, in the same lock hold: no
// slot starts a child between the two.
//
// Committing abandons every slot's wait (the children keep running) and
// cancels the pool context so parked slots return. It records no halt: the pool
// hands its children on rather than stopping. Closing and cancelling here is
// safe under p.mu because neither calls back into the pool. A halt recorded
// after this point (a child that exited with a halting code in the race
// before its wait was abandoned, or a Stop the Stop watcher picks up late) is
// dropped by recordHalt, with no halt event: the handoff stands, since the
// other children are already abandoned and cannot be waited on again.
func (p *pool) commitHandoffIfClean(s *state) {
	// Abort closing without Stop would refuse the handoff for good, leaving
	// parked slots waiting on ctx alone. Production cannot reach that:
	// announceStop closes Stop before Abort.
	if s.self.Phase != SelfUpdateWaiting || s.halted || p.stopClosed() || p.abortClosed() {
		return
	}
	// A nil baton is a single-slot pool, which has no sibling to stagger
	// against. The pre-assigned leader's token counts as held until that slot's
	// first round passes it, which a slot parking for the handoff does.
	if p.baton != nil && s.batonSlot != noBaton {
		return
	}
	for _, sl := range s.slots {
		if sl.phase == PhaseRunning && !sl.flight.reported {
			return
		}
	}
	s.self.Phase = SelfUpdateRestarting
	close(p.abandon)
	p.cancel()
}

// abortClosed is stopClosed for cfg.Abort.
func (p *pool) abortClosed() bool {
	select {
	case <-p.cfg.Abort:
		return true
	default:
		return false
	}
}

// noteReported records that slot's child has delivered a record. A no-op once
// it has, so a chatty child does not publish a status per record for this.
func (p *pool) noteReported(slot int) {
	if !p.cfg.RestartInPlace {
		return // only the in-place handoff reads the flag
	}
	p.mu.Lock()
	done := p.st.slots[slot].phase != PhaseRunning || p.st.slots[slot].flight.reported
	p.mu.Unlock()
	if done {
		return
	}
	p.mutate(func(s *state) []Event {
		if s.slots[slot].phase == PhaseRunning {
			s.slots[slot].flight.reported = true
		}
		return nil
	})
}

// noteAbandoned records the state of slot's child after its wait was
// abandoned, which is what the successor re-attaches it from.
func (p *pool) noteAbandoned(slot int, child ChildState) {
	p.mutate(func(s *state) []Event {
		s.slots[slot].handed = &child
		return nil
	})
}

// leaveSlot is runSlot's exit reset: it idles the slot, except one handed to a
// successor, which stays busy in the status file because its child still runs.
func (p *pool) leaveSlot(slot int) {
	p.mutate(func(s *state) []Event {
		if s.slots[slot].handed == nil {
			s.slots[slot].phase = PhaseIdle
		}
		return nil
	})
}

// committedHandoff is the document for a committed handoff, nil when the pool
// did not commit one. Only meaningful after every slot has returned. LockFD is
// left zero: the descriptor holding the instance lock belongs to the exec
// step (#5016), which fills it in.
func (p *pool) committedHandoff() *Handoff {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := &p.st
	if s.self.Phase != SelfUpdateRestarting {
		return nil
	}
	h := &Handoff{
		Stop:    p.stopClosed(),
		Abort:   p.abortClosed(),
		Kinds:   s.sched.snapshot(),
		Breaker: s.b.snapshot(),
	}
	for slot, sl := range s.slots {
		if sl.handed == nil {
			continue
		}
		f := sl.flight
		h.Slots = append(h.Slots, HandoffSlot{
			Slot: slot, Kind: f.kind, Revision: f.revision,
			ChildStart: f.childStart.time, ChildStartN: f.childStart.n, ChildLog: f.childLog,
			GateGen: f.gateGen, Key: f.key, Issues: slices.Clone(f.issues),
			Pass: f.pass, Model: f.model, ModelRole: f.modelRole,
			Child: *sl.handed,
		})
	}
	return h
}
