package waves

import (
	"fmt"
	"strconv"
	"time"
)

// StaleDrainReport summarizes what a stale drain cost (issue #2678).
// FreeSlotSecs totals the slot-seconds that sat idle between StaleAt and
// DrainedAt.
type StaleDrainReport struct {
	StaleAt      time.Time
	DrainedAt    time.Time
	FreeSlotSecs float64
	// HeldBack counts issues that didn't launch solely because the run had
	// already gone stale by the time they were reached. Queue.Pending()
	// supplies it (#2939) and each adapter defines it in its own terms:
	// headlessQueue filters through CountReady, Console's runContinuousQueue
	// uses a raw PickQueued tally. Either way it is a scope decision (#2778).
	HeldBack int
	// HeldBackUnknown is true when queue.Pending() errored at the moment of the
	// stale verdict (#2678, #2939). Console() and HostLog() must render this
	// distinctly from a confirmed zero.
	HeldBackUnknown bool
}

// Duration returns the wall-clock gap between StaleAt and DrainedAt. It is not
// rounded here so a zero-length drain returns exactly zero.
func (r StaleDrainReport) Duration() time.Duration {
	return r.DrainedAt.Sub(r.StaleAt)
}

func (r StaleDrainReport) heldBackText() string {
	if r.HeldBackUnknown {
		return "unknown"
	}
	return strconv.Itoa(r.HeldBack)
}

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

// HostLog renders one space-delimited key=value line prefixed "STALE_DRAIN ",
// ending in "\n", so an external loop script can parse and sum repeated appends.
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
