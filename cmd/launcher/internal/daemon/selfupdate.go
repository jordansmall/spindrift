package daemon

import (
	"context"
	"fmt"
)

// selfUpdate handles a fetched tip whose daemon build differs from the
// running one (issue #5012, ADR 0063): it starts a prebuild and smoke test of
// that build in the background and returns at once, so the slot carries on and
// the pool keeps filling under the running build. Whether to build is decided
// inside one mutate, which makes the build single-flight across the pool; a
// revision that blocked is not rebuilt until the tip moves.
//
// Only the build's goroutine halts, and only on the tip it built, so a slot
// that later sees a newer tip never halts on a build it has not made. A
// failed build never halts.
func (p *pool) selfUpdate(ctx context.Context, kind Kind, tip Tip) {
	revision := tip.Revision
	build := false
	p.mutate(func(s *state) []Event {
		switch {
		case s.self.Phase == SelfUpdateBuilding, s.self.Phase == SelfUpdateBuilt:
		case s.self.Phase == SelfUpdateBlocked && s.self.Revision == revision:
		default:
			s.self = SelfUpdate{Phase: SelfUpdateBuilding, Revision: revision}
			build = true
		}
		return nil
	})
	if !build {
		return
	}

	p.bg.Add(1)
	go func() {
		defer p.bg.Done()
		err := p.r.Prebuild(ctx, tip)
		halted := false
		p.mutate(func(s *state) []Event {
			switch {
			case ctx.Err() != nil || s.halted:
				// A stop or another halt cancelled the build mid-flight: that
				// says nothing about the revision, so leave no verdict behind.
				s.self = SelfUpdate{}
				return nil
			case err != nil:
				s.self = SelfUpdate{Phase: SelfUpdateBlocked, Revision: revision, Reason: err.Error()}
				return []Event{s.self.event()}
			}
			s.self = SelfUpdate{Phase: SelfUpdateBuilt, Revision: revision}
			// Halting in this same step leaves no gap in which a slot could
			// start a child after the built event.
			halted = true
			return append([]Event{s.self.event()}, s.recordHalt(Halt{
				Class:    HaltSelfChanged,
				Detail:   fmt.Sprintf("daemon build at %s is %s, running %s", revision, tip.SelfPath, p.cfg.SelfProgram),
				Kind:     kind,
				Revision: revision,
			})...)
		})
		if halted {
			p.cancel()
		}
	}()
}

// clearBlockedSelfUpdate drops a blocked verdict once a resolved tip's build
// matches the running one again, so a stale entry does not linger in the
// status file.
func (p *pool) clearBlockedSelfUpdate() {
	p.mu.Lock()
	blocked := p.st.self.Phase == SelfUpdateBlocked
	p.mu.Unlock()
	if !blocked {
		return
	}
	p.mutate(func(s *state) []Event {
		if s.self.Phase == SelfUpdateBlocked {
			s.self = SelfUpdate{}
		}
		return nil
	})
}
