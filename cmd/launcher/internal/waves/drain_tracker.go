package waves

import "time"

// drainTracker consolidates the stale-drain report state RunContinuous
// accumulates across a single stale transition and drain-out (#2678, #2774).
// Every field here is protected by RunContinuous's own mu; drainTracker
// keeps no lock of its own, matching Session's plain-field-bag style, since
// mu already serializes every access before this type ever existed.
type drainTracker struct {
	// heldBack is the count of issues that were actually ready to dispatch
	// (nextReady/countReady's own blocked/touch-overlap/failed-check
	// filtering, not just unclaimed) at the moment the stale verdict fired
	// (#2678). It is left at its zero value whenever heldBackUnknown is set,
	// since the two are mutually exclusive per drain.
	heldBack int

	// heldBackUnknown is set true only when the stale-transition branch's
	// reporting-only discover() call errors -- a transient tracker hiccup,
	// not a confirmed zero-blocker result -- so both DrainReport emission
	// sites can render "unknown" instead of silently asserting a fabricated
	// 0 (#2678 review finding). It is never set in the cfg.PendingCount
	// branch: that path has no discover() call to fail.
	heldBackUnknown bool

	// drainStart, drainEnd, drainSlotAt track the stale-drain report (#2678).
	// drainStart/drainEnd stay zero-valued until the stale verdict fires and
	// this invocation's report is emitted; that zero value doubles as a
	// single-emit guard against ever computing a second report for the same
	// RunContinuous call, since the stale-transition branch runs at most
	// once per call. drainSlotAt is the wall-clock time of the last
	// checkpointDrain call (or drainStart, before the first one) -- the
	// start of the interval the next checkpointDrain call will close out.
	drainStart, drainEnd, drainSlotAt time.Time

	// freeSlotSecs accumulates idle slot-seconds across every interval
	// checkpointDrain has closed out since drainStart.
	freeSlotSecs float64

	// drainCap tracks the cap that was actually in effect since the last
	// checkpoint -- not limiter.Cap() read live at checkpoint time. A
	// Console operator can raise or lower the live cap mid-interval via
	// ResizeDelta (ADR 0023); reading limiter.Cap() fresh at the next
	// checkpoint would retroactively credit the new cap to the whole
	// preceding interval, when only the cap that actually held during
	// that interval is correct. checkpointDrain is the only place drainCap
	// is refreshed, and always after closing out the interval that just
	// ended -- driven by both the completion goroutine and the resize
	// listener in RunContinuous, so a fresh read only ever applies to the
	// interval starting now, whichever direction the resize went.
	drainCap int
}

// inProgress reports whether a stale drain has started and not yet been
// reported -- drainStart is only ever set inside the stale=true branch in
// RunContinuous, so a bare stale check would be redundant with
// !t.drainStart.IsZero() here; this is the single definition every
// checkpoint guard in RunContinuous shares -- both completion-goroutine
// sites and the resize listener's -- rather than duplicating the condition
// verbatim at each site.
func (t *drainTracker) inProgress() bool {
	return !t.drainStart.IsZero() && t.drainEnd.IsZero()
}

// begin starts a new drain interval: now is the moment the stale verdict
// fired (RunContinuous's cfg.now override, or the production clock), and
// liveCap is the limiter's live cap (limiter.Cap()) at that moment. Callers
// must hold RunContinuous's mu. drainSlotAt starts equal to drainStart, so
// the first checkpointDrain call measures idle time from the drain's own
// start rather than some earlier, unrelated instant.
func (t *drainTracker) begin(now time.Time, liveCap int) {
	t.drainStart = now
	t.drainSlotAt = now
	t.drainCap = liveCap
}

// checkpointDrain closes out the drain interval since the last checkpoint
// (or drainStart) using t.drainCap -- the cap that was actually in effect
// over that interval, not a live cap read -- then refreshes t.drainCap from
// liveCap for the interval that starts at now. Callers must hold
// RunContinuous's mu and have already confirmed t.inProgress(). now is the
// current time (RunContinuous's cfg.now override, or the production clock);
// liveCap is the limiter's live cap (limiter.Cap()); outstanding is
// RunContinuous's in-flight Box count.
func (t *drainTracker) checkpointDrain(now time.Time, liveCap, outstanding int) {
	// A Console operator can lower the live cap mid-drain via ResizeDelta
	// while more Boxes are outstanding than the new cap allows --
	// ResizeDelta never revokes an already-claimed slot (limiter.go), so
	// drainCap-outstanding can go negative here. Clamp to zero: a
	// lowered-below-outstanding interval has no free slots to credit, not a
	// negative contribution that would corrupt the running total.
	t.freeSlotSecs += float64(max(t.drainCap-outstanding, 0)) * now.Sub(t.drainSlotAt).Seconds()
	t.drainSlotAt = now
	t.drainCap = liveCap
}

// checkpointIfDraining calls checkpointDrain only when a drain is actually
// in progress (t.inProgress()), folding the guard every RunContinuous
// checkpoint site otherwise repeated verbatim into one call. Safe to call
// unconditionally from any of those sites -- a no-op outside a drain. now,
// liveCap, and outstanding are checkpointDrain's own parameters, forwarded
// unchanged.
func (t *drainTracker) checkpointIfDraining(now time.Time, liveCap, outstanding int) {
	if t.inProgress() {
		t.checkpointDrain(now, liveCap, outstanding)
	}
}

// finish closes out the drain at end -- either drainStart itself, for an
// already-drained zero-duration report (no Box was ever outstanding), or
// drainSlotAt, once every outstanding Box has landed -- and renders the
// finished drain into the DrainReport RunContinuous emits. finish is the
// only caller of report, and always sets drainEnd immediately beforehand,
// so report itself carries no caller precondition to document.
func (t *drainTracker) finish(end time.Time) DrainReport {
	t.drainEnd = end
	return t.report()
}

// report renders t's fields into the DrainReport RunContinuous emits.
// freeSlotSecs is naturally 0.0 when finish is called before any
// checkpointDrain call has run: drainSlotAt still equals drainStart then.
func (t *drainTracker) report() DrainReport {
	return DrainReport{
		StaleAt:         t.drainStart,
		DrainedAt:       t.drainEnd,
		FreeSlotSecs:    t.freeSlotSecs,
		HeldBack:        t.heldBack,
		HeldBackUnknown: t.heldBackUnknown,
	}
}
