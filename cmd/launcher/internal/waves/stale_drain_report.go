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
	// already gone stale, from Queue.Pending() (#2939). What counts is a scope
	// decision (#2778).
	HeldBack int
	// HeldBackUnknown is true when queue.Pending() errored at the stale
	// verdict (#2678, #2939); it must render distinctly from a confirmed zero.
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

// Stdout renders a human-readable summary line for stdout, ending in "\n".
func (r StaleDrainReport) Stdout() string {
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
