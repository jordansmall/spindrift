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
	kinds       []Kind        // configured order; slotOrder derives priority from it
	reservation int           // running reserved-tier children the reserved tier is owed first
	idleCap     time.Duration // the backoff cap: how far ahead a tip-only Park sets Until
	state       map[Kind]kindSched
}

// NextDue is when a child-reported kind next has work, as one child's
// not_due records report it: At is the earliest instant a Chore lifts (zero
// for none) and OnTipMove that some Chore waits on a tip move rather than a
// clock. The zero value is no report.
type NextDue struct {
	At        time.Time
	OnTipMove bool
}

// IsZero reports that n carries no report.
func (n NextDue) IsZero() bool { return n.At.IsZero() && !n.OnTipMove }

// Merge folds another record into n: the earliest non-zero instant, and
// OnTipMove if either has it.
func (n NextDue) Merge(o NextDue) NextDue {
	if !o.At.IsZero() && (n.At.IsZero() || o.At.Before(n.At)) {
		n.At = o.At
	}
	n.OnTipMove = n.OnTipMove || o.OnTipMove
	return n
}

// kindSched is one kind's state. interval > 0 marks a probed kind, whose
// work-or-not answer comes from a Demand count; interval == 0 keeps the
// exit-driven backoff semantics: both exit 2 and exit 3 back off, Continue
// resets, a moved tip resets only a jam. A probed kind keeps probing under a
// jam gate, since a count above readyAtJam lifts it.
//
// A reported kind (the descriptor's DemandChildReported) is exit-driven
// until a child reports when it next has work: then it waits for that
// instant (or a moved tip) instead of backing off. Without a report it falls
// back to the exit-driven backoff, so an older child still behaves.
//
// gate does double duty: for an unprobed kind it is the no-work backoff, for
// a probed kind only the jam gate (an empty queue is the Demand's job there,
// and waits exactly one interval rather than growing).
type kindSched struct {
	interval time.Duration
	gate     kindBackoff

	reported bool
	// due is a child's report of when it next has work; zero until one
	// reports, and again once a child runs.
	due NextDue

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

	// tracker keys the rate-limit pause kinds share: kinds counting against one
	// tracker are paused together, since they spend one quota.
	tracker string
	// limitedUntil is when the tracker's rate-limit pause ends; the zero time
	// or a past instant means not paused.
	limitedUntil time.Time

	// fault is how the kind's own last probe ended, so the pool announces
	// transitions rather than every repeat.
	fault probeFault
}

// probeFault is how a kind's last probe ended.
type probeFault int

const (
	probeClean       probeFault = iota // answered, or never probed
	probeFailed                        // errored
	probeRateLimited                   // refused by the tracker's rate limit
)

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

// Park says nothing is startable; wait until Until. TipPoll reports that a
// moved tip can end the wait early — a jammed kind's gate, or a reported kind
// waiting on a tip move — so the caller slices it by IdleFloor to poll the
// tip. Until may be a probe due sooner than the jam's end, and is after the
// Decide's now whenever TipPoll is set, or idleSleep would return at once and
// the slot would spin instead of slicing.
type Park struct {
	Until   time.Time
	TipPoll bool
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
	// NextDue is the child's report of when its kind next has work; read only
	// for a child-reported kind.
	NextDue NextDue
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

// DemandRateLimited: a probe of Kind was refused by its tracker's rate limit.
// Reset is when the tracker says the limit lifts; zero when it reported none.
// The pause covers every kind on the same tracker, not just Kind.
type DemandRateLimited struct {
	Kind  Kind
	Reset time.Time
}

func (e ChildDone) kind() Kind    { return e.Kind }
func (e DemandProbed) kind() Kind { return e.Kind }
func (e DemandFailed) kind() Kind { return e.Kind }
func (e Claimed) kind() Kind      { return e.Kind }

func (e DemandRateLimited) kind() Kind { return e.Kind }

// newSchedule builds a Schedule over kinds. probe holds the Demand interval
// for each probed kind; a kind absent or at 0 stays exit-driven. floor and
// idleCap bound the per-kind backoff, as for idleBackoff. trackers names the
// tracker each kind's Demand counts against; a kind without an entry is its
// own tracker, so it shares no rate-limit pause.
func newSchedule(kinds []Kind, reservation int, floor, idleCap time.Duration, probe map[Kind]time.Duration, trackers map[Kind]string) Schedule {
	s := Schedule{kinds: kinds, reservation: reservation, idleCap: idleCap, state: make(map[Kind]kindSched, len(kinds))}
	for _, k := range kinds {
		tracker, ok := trackers[k]
		if !ok {
			tracker = string(k)
		}
		s.state[k] = kindSched{interval: probe[k], gate: newKindBackoff(floor, idleCap), tracker: tracker, reported: kindReported(k) && probe[k] == 0}
	}
	return s
}

// kindReported reports that only a child of k knows when it next has work
// (the descriptor's DemandChildReported), so the schedule learns it from the
// child's report rather than a probe.
func kindReported(k Kind) bool {
	d, ok := dispatchkind.ByVerb(string(k))
	return ok && d.DemandSource == dispatchkind.DemandChildReported
}

func (k kindSched) probed() bool { return k.interval > 0 }

// stale reports whether the Demand count is too old to trust at now.
func (k kindSched) stale(now time.Time) bool {
	return k.probedAt.IsZero() || !now.Before(k.probedAt.Add(k.interval))
}

// paused reports whether the tracker's rate-limit pause still holds at now.
func (k kindSched) paused(now time.Time) bool { return now.Before(k.limitedUntil) }

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
	if k.paused(now) {
		return false
	}
	if !k.due.IsZero() {
		return !k.due.At.IsZero() && !now.Before(k.due.At)
	}
	if !k.probed() {
		return k.gate.runnable(now)
	}
	return !k.jamGated(now) && !k.stale(now) && k.ready > starting
}

// tipWait reports that only a moved tip can lift this kind's known due state.
func (k kindSched) tipWait() bool { return k.due.OnTipMove }

// deadline is the instant this kind next needs attention, for a kind that is
// neither startable nor probe-due. Zero when it has none. Under a jam gate that
// is the earlier of the next probe and the jam's end.
func (k kindSched) deadline(now time.Time) time.Time {
	if k.paused(now) {
		return k.limitedUntil
	}
	if !k.due.IsZero() {
		return k.due.At
	}
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
		if ks.probed() && !ks.paused(now) && ks.stale(now) {
			stale = append(stale, kind)
		}
	}
	if len(stale) > 0 {
		return Probe{Kinds: stale}
	}
	var until time.Time
	tipPoll, tipWait := false, false
	for _, kind := range s.kinds {
		ks := s.state[kind]
		tw := ks.tipWait()
		if at := ks.deadline(now); !at.IsZero() && (until.IsZero() || at.Before(until)) {
			until = at
		}
		tipPoll = tipPoll || ks.jamGated(now) || tw
		tipWait = tipWait || tw
	}
	if until.IsZero() {
		until = now
		if tipWait {
			// Nothing is timed, but idleSleep returns at once for a wait not
			// after now and slices only a longer one: give it a wait to slice
			// so the slot resolves the tip each slice.
			until = now.Add(s.idleCap)
		}
	}
	return Park{Until: until, TipPoll: tipPoll}
}

// Observe folds ev into the schedule at now and returns the updated copy.
// woke reports that the event made its kind startable when it was not before.
// Only the observed kind changes — except DemandRateLimited, which pauses
// every kind on its tracker and only ever closes kinds, so woke is false.
func (s Schedule) Observe(now time.Time, ev SchedEvent) (Schedule, bool) {
	kind := ev.kind()
	ks, ok := s.state[kind]
	if !ok {
		return s, false
	}
	switch e := ev.(type) {
	case DemandRateLimited:
		return s.rateLimited(now, kind, e.Reset), false
	case ChildDone:
		ks = ks.childDone(now, e.Result, e.NextDue)
	case DemandProbed:
		ks.ready, ks.counted, ks.fault = e.Ready, e.Ready, probeClean
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
		ks.probedAt, ks.fault = now, probeFailed
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

// rateLimitFallbackIntervals is how many probe intervals a rate limit without
// a usable reset pauses for.
const rateLimitFallbackIntervals = 4

// rateLimited pauses every kind on kind's tracker until reset. A zero or
// already-past reset would re-probe straight into the limit, so the pause is
// four of the observed kind's intervals instead. Counts are left as read.
func (s Schedule) rateLimited(now time.Time, kind Kind, reset time.Time) Schedule {
	observed := s.state[kind]
	until := reset
	if !reset.After(now) {
		until = now.Add(rateLimitFallbackIntervals * observed.interval)
	}
	m := make(map[Kind]kindSched, len(s.state))
	for k, ks := range s.state {
		// Keep the later deadline: a sibling probe already in flight when the
		// tracker paused may report a limit with no reset.
		if ks.tracker == observed.tracker && until.After(ks.limitedUntil) {
			ks.limitedUntil = until
		}
		if k == kind {
			ks.fault = probeRateLimited
		}
		m[k] = ks
	}
	s.state = m
	return s
}

// opened reports that a kind startable after a change was not before it.
func opened(before, after kindSched, now time.Time) bool {
	return after.startable(now, 0) && !before.startable(now, 0)
}

func (k kindSched) childDone(now time.Time, r ChildOutcome, nd NextDue) kindSched {
	if k.reported {
		k.due = NextDue{}
		switch {
		case r == ChildEmpty && !nd.IsZero():
			k.due = nd
			k.gate = k.gate.reset()
		case r == ChildContinue:
			// A swept Chore may leave another due; the next child re-reports.
			k.gate = k.gate.reset()
		default:
			k.gate, _ = k.gate.markNoWork(now, r == ChildJammed)
		}
		return k
	}
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

// TipMoved is the schedule's answer to a moved tip. It ends every jammed
// kind's gate — a moved tip is evidence a merge unblocked them, though not
// that an empty queue refilled — and forgets the due state of a reported kind
// waiting on a tip move, so its next child re-reports. It returns the kinds
// lifted in configured order, for the tip_moved event. woke is Observe's
// answer for the lift: some lifted kind is startable at now that was not.
func (s Schedule) TipMoved(now time.Time) (_ Schedule, lifted []Kind, woke bool) {
	for _, kind := range s.kinds {
		if ks := s.state[kind]; ks.gate.jammedNow() || ks.tipWait() {
			before := ks
			ks.gate = ks.gate.reset()
			ks.due = NextDue{}
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
	// Gated reports the kind cannot start at now, with Until the instant that
	// ends it: the backoff for an unprobed kind, the jam or the fresh-empty
	// Demand for a probed one, the reported due instant for a child-reported
	// one. Until is zero when not Gated, never a stale deadline, and also zero
	// for a Gated kind waiting only on a tip move: no clock lifts it.
	// A rate-limit pause also Gates, with Until the later of its end and any
	// other gate's.
	Gated  bool
	Until  time.Time
	Jammed bool

	Tracker          string
	RateLimitedUntil time.Time // zero unless paused at now
	fault            probeFault

	Probed     bool
	Ready      int
	ProbedAt   time.Time
	NextProbe  time.Time // zero when never probed
	JamUntil   time.Time // zero unless jam-gated
	ReadyAtJam int
	Counted    int  // last probed count; unlike Ready, a child's exit 2 leaves it
	Fresh      bool // the next probe must skip any adapter cache

	NextDue NextDue // a child-reported kind's due state; zero when none is known
}

// View describes kind at now; the zero KindView for an unknown kind.
func (s Schedule) View(kind Kind, now time.Time) KindView {
	ks, ok := s.state[kind]
	if !ok {
		return KindView{}
	}
	v := ks.view(now)
	v.Tracker, v.fault = ks.tracker, ks.fault
	if ks.paused(now) {
		v.RateLimitedUntil, v.Gated = ks.limitedUntil, true
		if ks.limitedUntil.After(v.Until) {
			v.Until = ks.limitedUntil
		}
	}
	return v
}

// view is View without the tracker or rate-limit pause.
func (ks kindSched) view(now time.Time) KindView {
	v := KindView{Jammed: ks.gate.jammedNow(), NextDue: ks.due}
	if !ks.due.IsZero() {
		if !ks.startable(now, 0) {
			v.Gated, v.Until = true, ks.due.At
		}
		return v
	}
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
