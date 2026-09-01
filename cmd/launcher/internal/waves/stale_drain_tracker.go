package waves

import "time"

// staleDrainTracker consolidates the stale-drain report state RunContinuous
// accumulates across a single stale transition and drain-out. Every field is
// protected by RunContinuous's own mu; the tracker keeps no lock of its own.
type staleDrainTracker struct {
	// heldBack is queue.Pending()'s raw return value at the moment the stale
	// verdict fired -- a quiet remaining-candidate count each Queue adapter
	// defines in its own terms (see StaleDrainReport.HeldBack). Left at zero
	// whenever heldBackUnknown is set; the two are mutually exclusive.
	heldBack int

	// heldBackUnknown is set only when that queue.Pending() call errors -- a
	// transient hiccup, not a confirmed zero -- so both StaleDrainReport
	// emission sites render "unknown" rather than a fabricated 0.
	heldBackUnknown bool

	// staleDrainStart/staleDrainEnd stay zero-valued until the stale verdict
	// fires and the report is emitted; that zero value doubles as a
	// single-emit guard against computing a second report for the same
	// RunContinuous call. staleDrainSlotAt is the last checkpointStaleDrain
	// time (or staleDrainStart, before the first) -- the start of the
	// interval the next checkpoint closes out.
	staleDrainStart, staleDrainEnd, staleDrainSlotAt time.Time

	// freeSlotSecs accumulates idle slot-seconds across every interval
	// checkpointStaleDrain has closed out since staleDrainStart.
	freeSlotSecs float64

	// staleDrainCap is the cap that was actually in effect since the last
	// checkpoint, not limiter.Cap() read live at checkpoint time: an operator
	// can resize mid-interval (ADR 0023), and a fresh read would retroactively
	// credit the new cap to the whole preceding interval. checkpointStaleDrain
	// is the only place it is refreshed, always after closing out the interval
	// that just ended, so a fresh read only applies to the interval starting
	// now, whichever direction the resize went.
	staleDrainCap int
}

// inProgress reports whether a stale drain has started and not yet been
// reported -- the single definition every checkpoint guard in RunContinuous
// shares.
func (t *staleDrainTracker) inProgress() bool {
	return !t.staleDrainStart.IsZero() && t.staleDrainEnd.IsZero()
}

// begin starts a new drain interval: now is the moment the stale verdict
// fired, liveCap the limiter's cap at that moment. Callers must hold
// RunContinuous's mu. staleDrainSlotAt starts equal to staleDrainStart, so
// the first checkpoint measures idle time from the drain's own start.
func (t *staleDrainTracker) begin(now time.Time, liveCap int) {
	t.staleDrainStart = now
	t.staleDrainSlotAt = now
	t.staleDrainCap = liveCap
}

// checkpointStaleDrain closes out the interval since the last checkpoint (or
// staleDrainStart) using t.staleDrainCap, then refreshes it from liveCap for
// the interval starting at now. outstanding is RunContinuous's in-flight Box
// count. Callers must hold mu and have already confirmed t.inProgress().
func (t *staleDrainTracker) checkpointStaleDrain(now time.Time, liveCap, outstanding int) {
	// ResizeDelta never revokes an already-claimed slot (limiter.go), so a cap
	// lowered below the outstanding count makes this difference negative.
	// Clamp: such an interval has no free slots to credit, not a negative
	// contribution that would corrupt the running total.
	t.freeSlotSecs += float64(max(t.staleDrainCap-outstanding, 0)) * now.Sub(t.staleDrainSlotAt).Seconds()
	t.staleDrainSlotAt = now
	t.staleDrainCap = liveCap
}

// checkpointIfStaleDraining calls checkpointStaleDrain only when a drain is
// in progress, so RunContinuous's checkpoint sites can call it
// unconditionally -- a no-op outside a drain.
func (t *staleDrainTracker) checkpointIfStaleDraining(now time.Time, liveCap, outstanding int) {
	if t.inProgress() {
		t.checkpointStaleDrain(now, liveCap, outstanding)
	}
}

// finish closes out the drain at end -- either staleDrainStart itself, for an
// already-drained zero-duration report (no Box was ever outstanding), or
// staleDrainSlotAt once every outstanding Box has landed -- and renders the
// StaleDrainReport RunContinuous emits.
func (t *staleDrainTracker) finish(end time.Time) StaleDrainReport {
	t.staleDrainEnd = end
	return t.report()
}

// report renders t's fields into a StaleDrainReport. freeSlotSecs is
// naturally 0.0 when finish runs before any checkpoint has: staleDrainSlotAt
// still equals staleDrainStart then.
func (t *staleDrainTracker) report() StaleDrainReport {
	return StaleDrainReport{
		StaleAt:         t.staleDrainStart,
		DrainedAt:       t.staleDrainEnd,
		FreeSlotSecs:    t.freeSlotSecs,
		HeldBack:        t.heldBack,
		HeldBackUnknown: t.heldBackUnknown,
	}
}
