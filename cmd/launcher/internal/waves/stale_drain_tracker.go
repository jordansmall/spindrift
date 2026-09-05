package waves

import "time"

// staleDrainTracker consolidates the stale-drain report state RunContinuous
// accumulates across a single stale transition and drain-out (#2678, #2774).
// Every field here is protected by RunContinuous's own mu; staleDrainTracker
// keeps no lock of its own, matching Session's plain-field-bag style, since
// mu already serializes every access before this type ever existed.
type staleDrainTracker struct {
	// heldBack is queue.Pending()'s raw return value at the moment the stale
	// verdict fired (#2678, #2939) -- a quiet remaining-candidate count
	// defined by each Queue adapter in its own terms (see
	// StaleDrainReport.HeldBack's own doc comment for what headless vs
	// Console each count). It is left at its zero value whenever
	// heldBackUnknown is set, since the two are mutually exclusive per
	// drain.
	heldBack int

	// heldBackUnknown is set true only when the stale-transition branch's
	// queue.Pending() call errors -- a transient tracker hiccup, not a
	// confirmed zero result -- so both StaleDrainReport emission sites can
	// render "unknown" instead of silently asserting a fabricated 0 (#2678
	// review finding).
	heldBackUnknown bool

	// start, end, slotAt track the stale-drain report (#2678). t.start/t.end
	// stay zero-valued until the stale verdict fires and this invocation's
	// report is emitted; that zero value doubles as a single-emit guard
	// against ever computing a second report for the same RunContinuous
	// call, since the stale-transition branch runs at most once per
	// call. t.slotAt is the wall-clock time of the last checkpoint call
	// (or t.start, before the first one) -- the start of the interval the
	// next checkpoint call will close out.
	start, end, slotAt time.Time

	// freeSlotSecs accumulates idle slot-seconds across every interval
	// checkpoint has closed out since t.start.
	freeSlotSecs float64

	// cap tracks the cap that was actually in effect since the last
	// checkpoint -- not limiter.Cap() read live at checkpoint time. A
	// Console operator can raise or lower the live cap mid-interval via
	// ResizeDelta (ADR 0023); reading limiter.Cap() fresh at the next
	// checkpoint would retroactively credit the new cap to the whole
	// preceding interval, when only the cap that actually held during
	// that interval is correct. checkpoint is the only place t.cap is
	// refreshed, and always after closing out the interval that just ended
	// -- driven by both the completion goroutine and the resize listener
	// in RunContinuous, so a fresh read only ever applies to the interval
	// starting now, whichever direction the resize went.
	cap int
}

// inProgress reports whether a stale drain has started and not yet been
// reported -- t.start is only ever set inside the stale=true branch
// in RunContinuous, so a bare stale check would be redundant with
// !t.start.IsZero() here; this is the single definition every checkpoint
// guard in RunContinuous shares -- both completion-goroutine sites
// and the resize listener's -- rather than duplicating the condition
// verbatim at each site.
func (t *staleDrainTracker) inProgress() bool {
	return !t.start.IsZero() && t.end.IsZero()
}

// begin starts a new drain interval: now is the moment the stale
// verdict fired (RunContinuous's cfg.now override, or the production
// clock), and liveCap is the limiter's live cap (limiter.Cap()) at
// that moment. Callers must hold RunContinuous's mu. t.slotAt starts
// equal to t.start, so the first checkpoint call measures idle time
// from the drain's own start rather than some earlier, unrelated instant.
func (t *staleDrainTracker) begin(now time.Time, liveCap int) {
	t.start = now
	t.slotAt = now
	t.cap = liveCap
}

// checkpoint closes out the drain interval since the last checkpoint (or
// t.start) using t.cap -- the cap that was actually in effect over that
// interval, not a live cap read -- then refreshes t.cap from liveCap for
// the interval that starts at now. Callers must hold RunContinuous's
// mu and have already confirmed t.inProgress(). now is the current
// time (RunContinuous's cfg.now override, or the production clock);
// liveCap is the limiter's live cap (limiter.Cap()); outstanding is
// RunContinuous's in-flight Box count.
func (t *staleDrainTracker) checkpoint(now time.Time, liveCap, outstanding int) {
	// A Console operator can lower the live cap mid-drain via ResizeDelta
	// while more Boxes are outstanding than the new cap allows --
	// ResizeDelta never revokes an already-claimed slot (limiter.go),
	// so t.cap-outstanding can go negative here. Clamp to zero: a
	// lowered-below-outstanding interval has no free slots to credit,
	// not a negative contribution that would corrupt the running total.
	t.freeSlotSecs += float64(max(t.cap-outstanding, 0)) * now.Sub(t.slotAt).Seconds()
	t.slotAt = now
	t.cap = liveCap
}

// checkpointIfNeeded calls checkpoint only when a drain is actually
// in progress (t.inProgress()), folding the guard every RunContinuous
// checkpoint site otherwise repeated verbatim into one call. Safe to
// call unconditionally from any of those sites -- a no-op outside a
// drain. now, liveCap, and outstanding are checkpoint's own parameters,
// forwarded unchanged.
func (t *staleDrainTracker) checkpointIfNeeded(now time.Time, liveCap, outstanding int) {
	if t.inProgress() {
		t.checkpoint(now, liveCap, outstanding)
	}
}

// finish closes out the drain at end -- either t.start itself, for
// an already-drained zero-duration report (no Box was ever outstanding), or
// t.slotAt, once every outstanding Box has landed -- and renders
// the finished drain into the StaleDrainReport RunContinuous emits. finish
// is the only caller of report, and always sets t.end immediately
// beforehand, so report itself carries no caller precondition to document.
func (t *staleDrainTracker) finish(end time.Time) StaleDrainReport {
	t.end = end
	return t.report()
}

// report renders t's fields into the StaleDrainReport RunContinuous
// emits. freeSlotSecs is naturally 0.0 when finish is called before
// any checkpoint call has run: t.slotAt still equals t.start then.
func (t *staleDrainTracker) report() StaleDrainReport {
	return StaleDrainReport{
		StaleAt:         t.start,
		DrainedAt:       t.end,
		FreeSlotSecs:    t.freeSlotSecs,
		HeldBack:        t.heldBack,
		HeldBackUnknown: t.heldBackUnknown,
	}
}
