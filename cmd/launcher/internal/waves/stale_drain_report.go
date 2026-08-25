package waves

import (
	"fmt"
	"strconv"
	"time"
)

// StaleDrainReport summarizes what a stale drain cost (issue #2678): the
// wall-clock window between refilling stopping and every in-flight Box
// landing, the slot-seconds that sat idle across that window, and how many
// discovered issues were still unclaimed when the stale verdict fired.
type StaleDrainReport struct {
	StaleAt      time.Time
	DrainedAt    time.Time
	FreeSlotSecs float64
	HeldBack     int
	// HeldBackUnknown is true when the stale-drain report's held-back count
	// could not be determined (a transient discover error at the moment of
	// the stale verdict, #2678) -- Console()/HostLog() must render this
	// distinctly from a confirmed zero, never silently reporting 0 as if it
	// were a real count.
	HeldBackUnknown bool
}

// Duration returns the wall-clock gap between StaleAt and DrainedAt. It is
// not rounded here: a zero-length drain (StaleAt == DrainedAt) must return
// exactly zero, not a near-zero rounding artifact.
func (r StaleDrainReport) Duration() time.Duration {
	return r.DrainedAt.Sub(r.StaleAt)
}

// heldBackText renders r.HeldBack (or "unknown" when HeldBackUnknown) as the
// tail both Console and HostLog embed -- the only part their formats differ
// on, so rendering it once here keeps the two Sprintf calls from drifting
// out of sync with each other.
func (r StaleDrainReport) heldBackText() string {
	if r.HeldBackUnknown {
		return "unknown"
	}
	return strconv.Itoa(r.HeldBack)
}

// heldBackTail renders the trailing held-back clause Console embeds -- the
// only part its two shapes (HeldBackUnknown or not) differ on, so extracting
// it here keeps Console down to a single Sprintf call instead of duplicating
// the whole format string across both branches.
func (r StaleDrainReport) heldBackTail() string {
	if r.HeldBackUnknown {
		return fmt.Sprintf("held back: %s (query failed)", r.heldBackText())
	}
	return fmt.Sprintf("%s issue(s) held back", r.heldBackText())
}

// Console renders a human-readable summary line for stdout, ending in "\n".
func (r StaleDrainReport) Console() string {
	return fmt.Sprintf(
		"==> stale-drain: %s idle, %.1f free-slot-s, %s\n",
		r.Duration().Round(time.Millisecond), r.FreeSlotSecs, r.heldBackTail(),
	)
}

// HostLog renders a single space-delimited key=value line, prefixed
// "STALE_DRAIN ", ending in "\n", machine-parseable and summable by an external
// loop script across repeated appends.
func (r StaleDrainReport) HostLog() string {
	return fmt.Sprintf(
		"STALE_DRAIN staleAt=%s drainedAt=%s durationSeconds=%.3f freeSlotSeconds=%.3f heldBack=%s\n",
		r.StaleAt.UTC().Format(time.RFC3339Nano),
		r.DrainedAt.UTC().Format(time.RFC3339Nano),
		r.Duration().Seconds(),
		r.FreeSlotSecs,
		r.heldBackText(),
	)
}
