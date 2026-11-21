package waves

import "time"

// staleDrainTracker holds the stale-drain report state RunContinuous accumulates
// across a single stale transition and drain-out (#2678, #2774). RunContinuous's
// mu serializes every access, so this type keeps no lock of its own.
type staleDrainTracker struct {
	// heldBack is queue.Pending() at the stale verdict (#2678, #2939); zero
	// when heldBackUnknown is set.
	heldBack int

	// heldBackUnknown is set when that Pending call errors, so the report
	// renders "unknown" rather than a fabricated 0 (#2678).
	heldBackUnknown bool

	// The zero value of start and end doubles as a single-emit guard: the
	// stale transition runs at most once per RunContinuous call. slotAt is the
	// last checkpoint's time, or start before the first.
	start, end, slotAt time.Time

	freeSlotSecs float64

	cap int
}

// inProgress reports whether a stale drain has started and not yet been
// reported.
func (t *staleDrainTracker) inProgress() bool {
	return !t.start.IsZero() && t.end.IsZero()
}

// begin starts a new drain interval at now with cap as the limiter cap.
// Callers must hold RunContinuous's mu.
func (t *staleDrainTracker) begin(now time.Time, cap int) {
	t.start = now
	t.slotAt = now
	t.cap = cap
}

// checkpoint closes out the interval since the last checkpoint using t.cap.
// Callers must hold RunContinuous's mu and have already confirmed
// t.inProgress().
func (t *staleDrainTracker) checkpoint(now time.Time, outstanding int) {
	t.freeSlotSecs += float64(t.cap-outstanding) * now.Sub(t.slotAt).Seconds()
	t.slotAt = now
}

// checkpointIfNeeded calls checkpoint only when a drain is in progress.
func (t *staleDrainTracker) checkpointIfNeeded(now time.Time, outstanding int) {
	if t.inProgress() {
		t.checkpoint(now, outstanding)
	}
}

// finish closes out the drain at end and renders the StaleDrainReport
// RunContinuous emits. end is t.start for an already-drained zero-duration
// report, or t.slotAt once every outstanding Box has landed.
func (t *staleDrainTracker) finish(end time.Time) StaleDrainReport {
	t.end = end
	return t.report()
}

// report renders t's fields into a StaleDrainReport.
func (t *staleDrainTracker) report() StaleDrainReport {
	return StaleDrainReport{
		StaleAt:         t.start,
		DrainedAt:       t.end,
		FreeSlotSecs:    t.freeSlotSecs,
		HeldBack:        t.heldBack,
		HeldBackUnknown: t.heldBackUnknown,
	}
}
