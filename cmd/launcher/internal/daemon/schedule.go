package daemon

import (
	"time"

	"spindrift.dev/launcher/internal/dispatchkind"
)

// Schedule is the daemon's one scheduling decision-maker (ADR 0059): which
// kind a free slot starts, which kinds' Demand it must first re-probe, or how
// long it parks. It is pure — no clock, goroutine, or Runner call — so the
// pool owns every side effect and the tests are plain tables of events to
// decisions.
//
// A plain value like kindBackoff (issue #3623): Observe returns the updated
// copy and the pool stores it back under p.mu. Observe clones the per-kind
// map, so an older copy never sees a later Observe.
type Schedule struct {
	kinds       []Kind // configured order; slotOrder derives priority from it
	reservation int    // running reserved-tier children the reserved tier is owed first
	state       map[Kind]kindSched
}

// kindSched is one kind's state. interval > 0 marks a probed kind, whose
// work-or-not answer comes from a Demand count; interval == 0 keeps the
// exit-driven backoff semantics: both exit 2 and exit 3 back off, Continue
// resets, a moved tip resets only a jam. A probed kind keeps probing under a
// jam gate, since a count above readyAtJam lifts it.
//
// gate does double duty: for an unprobed kind it is the no-work backoff, for
// a probed kind only the jam gate (an empty queue is the Demand's job there,
// and waits exactly one interval rather than growing).
type kindSched struct {
	interval time.Duration
	gate     kindBackoff

	ready      int
	probedAt   time.Time // zero = never probed, or invalidated by a Continue
	readyAtJam int       // ready as of the Jammed result that set the gate

	// counted is the tracker's last probed count, set only by DemandProbed.
	// ready is also zeroed by a child's exit 2, which is no tracker
	// observation, so zero crossings compare against this instead.
	counted int

	// claims counts the Claimed events folded into this kind, so a probe can
	// tell whether one landed while it was in flight.
	claims int

	// fresh forces the next probe to skip any adapter cache: Demand said
	// there was work and the child found none, so the cached count is
	// suspect.
	fresh bool
}

// Occupancy is what the pool's slot phases say is running, passed in on each
// Decide rather than tracked twice.
//
// Starting counts the slots that chose a kind and whose child has not yet
// reported its claim; the pool counts a running slot with no flight key as
// starting, so Starting is a subset of Running. Running alone feeds the
// reservation floor.
type Occupancy struct {
	Running  map[Kind]int
	Starting map[Kind]int
}

// Decision is what a free slot should do next: Start, Probe, or Park.
type Decision interface{ isDecision() }

// Start says start a child of Kind.
type Start struct{ Kind Kind }

// Probe says re-count Demand for Kinds before deciding again: each is stale
// and sits ahead of every startable kind in priority order. A jam-gated kind is
// probed too, since a rising count lifts the jam.
type Probe struct{ Kinds []Kind }

// Park says nothing is startable; wait until Until. JamPoll reports that a
// jammed kind is gated at the decision, so the caller slices the wait by
// IdleFloor to poll the tip (idleSleep's jammedGate); Until may be a probe due
// sooner than the jam's end.
type Park struct {
	Until   time.Time
	JamPoll bool
}

func (Start) isDecision() {}
func (Probe) isDecision() {}
func (Park) isDecision()  {}

// ChildOutcome is how a child's check ended, as far as scheduling cares.
type ChildOutcome int

const (
	ChildContinue ChildOutcome = iota // exit 0 or 4: it did or found something
	ChildEmpty                        // exit 2: queue empty
	ChildJammed                       // exit 3: none dispatchable
)

// SchedEvent is a fact the pool feeds to Observe.
type SchedEvent interface {
	// kind is the Kind the event concerns; "" for one that names none.
	kind() Kind
}

// ChildDone: a child of Kind finished with Result.
type ChildDone struct {
	Kind   Kind
	Result ChildOutcome
}

// DemandProbed: a probe of Kind counted Ready startable items.
type DemandProbed struct {
	Kind  Kind
	Ready int
	// Claims is the kind's claim count when the probe began.
	Claims int
	Fresh  bool // the probe skipped any adapter cache
}

// DemandFailed: a probe of Kind errored.
type DemandFailed struct{ Kind Kind }

// Claimed: a child of Kind reported its claim, taking an item out of the count
// it was started against.
type Claimed struct{ Kind Kind }

func (e ChildDone) kind() Kind    { return e.Kind }
func (e DemandProbed) kind() Kind { return e.Kind }
func (e DemandFailed) kind() Kind { return e.Kind }
func (e Claimed) kind() Kind      { return e.Kind }

// newSchedule builds a Schedule over kinds. probe holds the Demand interval
// for each probed kind; a kind absent or at 0 stays exit-driven. floor and
// cap bound the per-kind backoff, as for idleBackoff.
func newSchedule(kinds []Kind, reservation int, floor, cap time.Duration, probe map[Kind]time.Duration) Schedule {
	s := Schedule{kinds: kinds, reservation: reservation, state: make(map[Kind]kindSched, len(kinds))}
	for _, k := range kinds {
		s.state[k] = kindSched{interval: probe[k], gate: newKindBackoff(floor, cap)}
	}
	return s
}

func (k kindSched) probed() bool { return k.interval > 0 }

// stale reports whether the Demand count is too old to trust at now.
func (k kindSched) stale(now time.Time) bool {
	return k.probedAt.IsZero() || !now.Before(k.probedAt.Add(k.interval))
}

// jamGated reports whether a Jammed gate still holds at now.
func (k kindSched) jamGated(now time.Time) bool {
	return k.gate.jammedNow() && !k.gate.runnable(now)
}

// startable reports whether a child of this kind may start at now, with
// starting children yet to claim. A starting child's item is still counted in
// ready, so a probed kind's children in discovery are capped at ready in
// total: ready-starting is how many more may start. An exit-driven kind has
// no count to spend.
func (k kindSched) startable(now time.Time, starting int) bool {
	if !k.probed() {
		return k.gate.runnable(now)
	}
	return !k.jamGated(now) && !k.stale(now) && k.ready > starting
}

// deadline is the instant this kind next needs attention, for a kind that is
// neither startable nor probe-due. Zero when it has none. Under a jam gate that
// is the earlier of the next probe and the jam's end.
func (k kindSched) deadline(now time.Time) time.Time {
	if !k.probed() {
		return k.gate.until
	}
	next := k.probedAt.Add(k.interval)
	if k.jamGated(now) && k.gate.until.Before(next) {
		return k.gate.until
	}
	return next
}

// Decide chooses the next action for a free slot at now.
func (s Schedule) Decide(now time.Time, occ Occupancy) Decision {
	running := 0
	for k, n := range occ.Running {
		if kindPriority(k) == dispatchkind.PriorityReserved {
			running += n
		}
	}
	var stale []Kind
	for _, kind := range slotOrder(s.kinds, running < s.reservation) {
		ks := s.state[kind]
		if ks.startable(now, occ.Starting[kind]) {
			if len(stale) > 0 {
				return Probe{Kinds: stale}
			}
			return Start{Kind: kind}
		}
		if ks.probed() && ks.stale(now) {
			stale = append(stale, kind)
		}
	}
	if len(stale) > 0 {
		return Probe{Kinds: stale}
	}
	var until time.Time
	jamPoll := false
	for _, kind := range s.kinds {
		ks := s.state[kind]
		if at := ks.deadline(now); !at.IsZero() && (until.IsZero() || at.Before(until)) {
			until = at
		}
		jamPoll = jamPoll || ks.jamGated(now)
	}
	if until.IsZero() {
		until = now
	}
	return Park{Until: until, JamPoll: jamPoll}
}

// Observe folds ev into the schedule at now and returns the updated copy.
// woke reports that the event made its kind startable when it was not before.
// Only the observed kind changes, so that is the whole startable set growing.
func (s Schedule) Observe(now time.Time, ev SchedEvent) (Schedule, bool) {
	kind := ev.kind()
	ks, ok := s.state[kind]
	if !ok {
		return s, false
	}
	switch e := ev.(type) {
	case ChildDone:
		ks = ks.childDone(now, e.Result)
	case DemandProbed:
		ks.ready, ks.counted = e.Ready, e.Ready
		// A claim that landed mid-probe moved the count after it was read, so
		// stay stale (the Claimed zeroed probedAt) and re-probe, and never
		// lift a jam on that pre-claim count.
		if e.Claims == ks.claims {
			ks.probedAt = now
			// More ready than when the jam was recorded means someone added
			// work, which a fall or a steady count is no evidence of. Strictly
			// greater: the same count says the jam's cause is still there. An
			// ended gate has nothing to lift, and a reset would wipe its streak.
			if ks.jamGated(now) && e.Ready > ks.readyAtJam {
				ks.gate = ks.gate.reset()
			}
		}
		// Only a fresh probe answers the pending force: a conditional one
		// already in flight when the child exited 2 must not clear it.
		if e.Fresh {
			ks.fresh = false
		}
	case DemandFailed:
		// Keep the old count: only a rest from probing, so siblings don't
		// hammer a failing tracker.
		ks.probedAt = now
	case Claimed:
		if ks.probed() {
			ks.claims++
			// Keep a failed re-probe from resting on the pre-claim count; counted
			// stays as read, for the demand_appeared/drained comparison.
			if ks.ready > 0 {
				ks.ready--
			}
			ks.probedAt = time.Time{} // the claim moved the count; re-probe before the next start
		}
	}
	return s.with(kind, ks), opened(s.state[kind], ks, now)
}

// opened reports that a kind startable after a change was not before it.
func opened(before, after kindSched, now time.Time) bool {
	return after.startable(now, 0) && !before.startable(now, 0)
}

func (k kindSched) childDone(now time.Time, r ChildOutcome) kindSched {
	if !k.probed() {
		if r == ChildContinue {
			k.gate = k.gate.reset()
		} else {
			k.gate, _ = k.gate.markNoWork(now, r == ChildJammed)
		}
		return k
	}
	switch r {
	case ChildContinue:
		k.gate = k.gate.reset()
		// The child just consumed from the count; re-probe rather than trust it.
		k.probedAt = time.Time{}
	case ChildEmpty:
		// The child's answer is a fresh observation: one interval of rest,
		// not a growing backoff, so an empty queue never becomes a spawn loop.
		k.gate = k.gate.reset()
		k.ready, k.probedAt = 0, now
		if k.counted > 0 {
			k.fresh = true
		}
	case ChildJammed:
		k.gate, _ = k.gate.markNoWork(now, true)
		k.readyAtJam = k.ready
	}
	return k
}

// LiftJams ends every jammed kind's gate — a moved tip is evidence a merge
// unblocked them, though not that an empty queue refilled — and returns the
// kinds lifted in configured order, for the tip_moved event. woke is Observe's
// answer for the lift: some lifted kind is startable at now that was not.
func (s Schedule) LiftJams(now time.Time) (_ Schedule, lifted []Kind, woke bool) {
	for _, kind := range s.kinds {
		if ks := s.state[kind]; ks.gate.jammedNow() {
			before := ks
			ks.gate = ks.gate.reset()
			s = s.with(kind, ks)
			lifted = append(lifted, kind)
			woke = woke || opened(before, ks, now)
		}
	}
	return s, lifted, woke
}

// claimsOf is the number of claims folded into kind, 0 for an unknown kind.
func (s Schedule) claimsOf(kind Kind) int { return s.state[kind].claims }

// with returns a copy of s holding ks for kind, leaving s's own map alone.
func (s Schedule) with(kind Kind, ks kindSched) Schedule {
	m := make(map[Kind]kindSched, len(s.state))
	for k, v := range s.state {
		m[k] = v
	}
	m[kind] = ks
	s.state = m
	return s
}

// KindView is a read-only per-kind picture for status and events.
type KindView struct {
	// Gated reports the kind cannot start at now for a timed reason, with
	// Until the instant that ends it: the backoff for an unprobed kind, the
	// jam or the fresh-empty Demand for a probed one. Until is zero when not
	// Gated, never a stale deadline.
	Gated  bool
	Until  time.Time
	Jammed bool

	Probed     bool
	Ready      int
	ProbedAt   time.Time
	NextProbe  time.Time // zero when never probed
	JamUntil   time.Time // zero unless jam-gated
	ReadyAtJam int
	Counted    int  // last probed count; unlike Ready, a child's exit 2 leaves it
	Fresh      bool // the next probe must skip any adapter cache
}

// View describes kind at now; the zero KindView for an unknown kind.
func (s Schedule) View(kind Kind, now time.Time) KindView {
	ks, ok := s.state[kind]
	if !ok {
		return KindView{}
	}
	v := KindView{Jammed: ks.gate.jammedNow()}
	if !ks.probed() {
		v.Until, v.Gated = ks.gate.readyAt(now)
		if !v.Gated {
			v.Until = time.Time{}
		}
		return v
	}
	v.Probed, v.Ready, v.Counted, v.ProbedAt, v.ReadyAtJam = true, ks.ready, ks.counted, ks.probedAt, ks.readyAtJam
	v.Fresh = ks.fresh
	if !ks.probedAt.IsZero() {
		v.NextProbe = ks.probedAt.Add(ks.interval)
	}
	switch {
	case ks.jamGated(now):
		v.Gated, v.Until, v.JamUntil = true, ks.gate.until, ks.gate.until
	case !ks.stale(now) && ks.ready == 0:
		v.Gated, v.Until = true, v.NextProbe
	}
	return v
}
