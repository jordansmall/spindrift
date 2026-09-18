package waves

import "time"

// staleDrainTracker holds the stale-drain report state RunContinuous accumulates
// across a single stale transition and drain-out (#2678, #2774). RunContinuous's
// mu serializes every access, so this type keeps no lock of its own.
type staleDrainTracker struct {
	// heldBack is queue.Pending()'s raw return value at the moment the stale
	// verdict fired (#2678, #2939); each Queue adapter defines that count in
	// its own terms. It stays zero whenever heldBackUnknown is set, since the
	// two are mutually exclusive per drain.
	heldBack int

	// heldBackUnknown is set only when the stale-transition branch's
	// queue.Pending() call errors, a transient tracker hiccup rather than a
	// confirmed zero, so both StaleDrainReport emission sites render "unknown"
	// instead of a fabricated 0 (#2678 review finding).
	heldBackUnknown bool

	// The zero value of start and end doubles as a single-emit guard: the
	// stale-transition branch runs at most once per RunContinuous call, so a
	// second report can never be computed. slotAt is the wall-clock time of
	// the last checkpoint call, or start before the first one.
	start, end, slotAt time.Time

	// freeSlotSecs accumulates idle slot-seconds across every interval
	// checkpoint has closed out since start.
	freeSlotSecs float64

	// cap is the cap that actually held since the last checkpoint, not
	// limiter.Cap() read live. A Console operator can resize the live cap
	// mid-interval (ADR 0023), and a fresh read would retroactively credit
	// the new cap to the whole preceding interval. Only checkpoint refreshes
	// it, and only after closing out the interval that just ended.
	cap int
}

// inProgress reports whether a stale drain has started and not yet been
// reported.
func (t *staleDrainTracker) inProgress() bool {
	return !t.start.IsZero() && t.end.IsZero()
}

// begin starts a new drain interval at now with liveCap as the limiter's live
// cap. Callers must hold RunContinuous's mu. slotAt starts equal to start so
// the first checkpoint measures idle time from the drain's own start.
func (t *staleDrainTracker) begin(now time.Time, liveCap int) {
	t.start = now
	t.slotAt = now
	t.cap = liveCap
}

// checkpoint closes out the interval since the last checkpoint using t.cap,
// then refreshes t.cap from liveCap for the interval starting at now. Callers
// must hold RunContinuous's mu and have already confirmed t.inProgress().
func (t *staleDrainTracker) checkpoint(now time.Time, liveCap, outstanding int) {
	// ResizeDelta never revokes an already-claimed slot (limiter.go), so a cap
	// lowered mid-drain below the outstanding count makes t.cap-outstanding
	// negative. Such an interval has no free slots to credit, so clamp to zero
	// rather than subtract from the running total.
	t.freeSlotSecs += float64(max(t.cap-outstanding, 0)) * now.Sub(t.slotAt).Seconds()
	t.slotAt = now
	t.cap = liveCap
}

// checkpointIfNeeded calls checkpoint only when a drain is in progress, so any
// RunContinuous checkpoint site can call it unconditionally.
func (t *staleDrainTracker) checkpointIfNeeded(now time.Time, liveCap, outstanding int) {
	if t.inProgress() {
		t.checkpoint(now, liveCap, outstanding)
	}
}

// finish closes out the drain at end and renders the StaleDrainReport
// RunContinuous emits. end is t.start for an already-drained zero-duration
// report, or t.slotAt once every outstanding Box has landed.
func (t *staleDrainTracker) finish(end time.Time) StaleDrainReport {
	t.end = end
	return t.report()
}

// report renders t's fields into a StaleDrainReport. freeSlotSecs is 0.0 when
// no checkpoint has run, because slotAt still equals start then.
func (t *staleDrainTracker) report() StaleDrainReport {
	return StaleDrainReport{
		StaleAt:         t.start,
		DrainedAt:       t.end,
		FreeSlotSecs:    t.freeSlotSecs,
		HeldBack:        t.heldBack,
		HeldBackUnknown: t.heldBackUnknown,
	}
}
