package waves

import "time"

// staleDrainTracker consolidates the stale-drain report state RunContinuous
// accumulates across a single stale transition and drain-out (#2678, #2774).
// Every field here is protected by RunContinuous's own mu; staleDrainTracker
// keeps no lock of its own, matching Session's plain-field-bag style, since
// mu already serializes every access before this type ever existed.
type staleDrainTracker struct {
	// heldBack is the count of issues that were actually ready to dispatch
	// (nextReady/countReady's own blocked/touch-overlap/failed-check
	// filtering, not just unclaimed) at the moment the stale verdict fired
	// (#2678). It is left at its zero value whenever heldBackUnknown is set,
	// since the two are mutually exclusive per drain.
	heldBack int

	// heldBackUnknown is set true only when the stale-transition branch's
	// reporting-only discover() call errors -- a transient tracker hiccup,
	// not a confirmed zero-blocker result -- so both StaleDrainReport emission
	// sites can render "unknown" instead of silently asserting a fabricated
	// 0 (#2678 review finding). It is never set in the cfg.PendingCount
	// branch: that path has no discover() call to fail.
	heldBackUnknown bool

	// staleDrainStart, staleDrainEnd, staleDrainSlotAt track the stale-drain
	// report (#2678). staleDrainStart/staleDrainEnd stay zero-valued until
	// the stale verdict fires and this invocation's report is emitted; that
	// zero value doubles as a single-emit guard against ever computing a
	// second report for the same RunContinuous call, since the
	// stale-transition branch runs at most once per call. staleDrainSlotAt
	// is the wall-clock time of the last checkpointStaleDrain call (or
	// staleDrainStart, before the first one) -- the start of the interval
	// the next checkpointStaleDrain call will close out.
	staleDrainStart, staleDrainEnd, staleDrainSlotAt time.Time

	// freeSlotSecs accumulates idle slot-seconds across every interval
	// checkpointStaleDrain has closed out since staleDrainStart.
	freeSlotSecs float64

	// staleDrainCap tracks the cap that was actually in effect since the
	// last checkpoint -- not limiter.Cap() read live at checkpoint time. A
	// Console operator can raise or lower the live cap mid-interval via
	// ResizeDelta (ADR 0023); reading limiter.Cap() fresh at the next
	// checkpoint would retroactively credit the new cap to the whole
	// preceding interval, when only the cap that actually held during
	// that interval is correct. checkpointStaleDrain is the only place
	// staleDrainCap is refreshed, and always after closing out the interval
	// that just ended -- driven by both the completion goroutine and the
	// resize listener in RunContinuous, so a fresh read only ever applies
	// to the interval starting now, whichever direction the resize went.
	staleDrainCap int
}

// inProgress reports whether a stale drain has started and not yet been
// reported -- staleDrainStart is only ever set inside the stale=true branch
// in RunContinuous, so a bare stale check would be redundant with
// !t.staleDrainStart.IsZero() here; this is the single definition every
// checkpoint guard in RunContinuous shares -- both completion-goroutine
// sites and the resize listener's -- rather than duplicating the condition
// verbatim at each site.
func (t *staleDrainTracker) inProgress() bool {
	return !t.staleDrainStart.IsZero() && t.staleDrainEnd.IsZero()
}

// begin starts a new drain interval: now is the moment the stale verdict
// fired (RunContinuous's cfg.now override, or the production clock), and
// liveCap is the limiter's live cap (limiter.Cap()) at that moment. Callers
// must hold RunContinuous's mu. staleDrainSlotAt starts equal to
// staleDrainStart, so the first checkpointStaleDrain call measures idle
// time from the drain's own start rather than some earlier, unrelated
// instant.
func (t *staleDrainTracker) begin(now time.Time, liveCap int) {
	t.staleDrainStart = now
	t.staleDrainSlotAt = now
	t.staleDrainCap = liveCap
}

// checkpointStaleDrain closes out the drain interval since the last
// checkpoint (or staleDrainStart) using t.staleDrainCap -- the cap that was
// actually in effect over that interval, not a live cap read -- then
// refreshes t.staleDrainCap from liveCap for the interval that starts at
// now. Callers must hold RunContinuous's mu and have already confirmed
// t.inProgress(). now is the current time (RunContinuous's cfg.now
// override, or the production clock); liveCap is the limiter's live cap
// (limiter.Cap()); outstanding is RunContinuous's in-flight Box count.
func (t *staleDrainTracker) checkpointStaleDrain(now time.Time, liveCap, outstanding int) {
	// A Console operator can lower the live cap mid-drain via ResizeDelta
	// while more Boxes are outstanding than the new cap allows --
	// ResizeDelta never revokes an already-claimed slot (limiter.go), so
	// staleDrainCap-outstanding can go negative here. Clamp to zero: a
	// lowered-below-outstanding interval has no free slots to credit, not a
	// negative contribution that would corrupt the running total.
	t.freeSlotSecs += float64(max(t.staleDrainCap-outstanding, 0)) * now.Sub(t.staleDrainSlotAt).Seconds()
	t.staleDrainSlotAt = now
	t.staleDrainCap = liveCap
}

// checkpointIfStaleDraining calls checkpointStaleDrain only when a drain is
// actually in progress (t.inProgress()), folding the guard every
// RunContinuous checkpoint site otherwise repeated verbatim into one call.
// Safe to call unconditionally from any of those sites -- a no-op outside a
// drain. now, liveCap, and outstanding are checkpointStaleDrain's own
// parameters, forwarded unchanged.
func (t *staleDrainTracker) checkpointIfStaleDraining(now time.Time, liveCap, outstanding int) {
	if t.inProgress() {
		t.checkpointStaleDrain(now, liveCap, outstanding)
	}
}

// finish closes out the drain at end -- either staleDrainStart itself, for
// an already-drained zero-duration report (no Box was ever outstanding), or
// staleDrainSlotAt, once every outstanding Box has landed -- and renders
// the finished drain into the StaleDrainReport RunContinuous emits. finish
// is the only caller of report, and always sets staleDrainEnd immediately
// beforehand, so report itself carries no caller precondition to document.
func (t *staleDrainTracker) finish(end time.Time) StaleDrainReport {
	t.staleDrainEnd = end
	return t.report()
}

// report renders t's fields into the StaleDrainReport RunContinuous emits.
// freeSlotSecs is naturally 0.0 when finish is called before any
// checkpointStaleDrain call has run: staleDrainSlotAt still equals
// staleDrainStart then.
func (t *staleDrainTracker) report() StaleDrainReport {
	return StaleDrainReport{
		StaleAt:         t.staleDrainStart,
		DrainedAt:       t.staleDrainEnd,
		FreeSlotSecs:    t.freeSlotSecs,
		HeldBack:        t.heldBack,
		HeldBackUnknown: t.heldBackUnknown,
	}
}
