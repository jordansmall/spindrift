package waves

import (
	"fmt"
	"strconv"
	"time"
)

// StaleDrainReport summarizes what a stale drain cost (issue #2678): the
// wall-clock window between refilling stopping and every in-flight Box
// landing, the slot-seconds that sat idle across that window, and (see
// HeldBack's own doc comment below for the exact, narrower semantics, and
// its caveat under cfg.IgnoreBlockers) how many issues were genuinely
// ready to dispatch but didn't launch solely because the run had already
// gone stale.
type StaleDrainReport struct {
	StaleAt      time.Time
	DrainedAt    time.Time
	FreeSlotSecs float64
	// HeldBack counts issues that passed the narrow check this field
	// measures -- every blocker/touch-overlap/DepsOf check, evaluated the
	// same way issueReadiness evaluates it -- but didn't launch solely
	// because the run had already gone stale by the time they were
	// reached. It is a scope decision, not a claim that every excluded
	// issue would definitely never have launched fresh: it deliberately
	// excludes three categories that issueReadiness also marks not-ready,
	// and two of the three are frequently transient rather than durable
	// blocks. Dependency-blocked issues (an unresolved blocker edge) are
	// the one genuinely durable, pre-existing exclusion, independent of
	// this run. Touch-overlap-deferred issues (waiting on a still
	// in-flight Box's file overlap) are frequently self-induced by this
	// run's own concurrency -- ListIssues(InProgress) during a drain is
	// dominated by this run's own in-flight Boxes, so once the colliding
	// Box lands, a fresh run's next refill would very plausibly launch
	// it. And (only when !cfg.IgnoreBlockers, continuous.go's own guard)
	// issues whose own DepsOf check failed are held only because that one
	// check call failed transiently -- continuous.go's own comment on
	// that case says "the next refill retries" -- so a fresh run's next
	// refill would very plausibly succeed and launch it too. Counting
	// either of those two into HeldBack would conflate the drain's own
	// cost with issues that were, at most, incidentally delayed by this
	// run's own state (#2778). Under cfg.IgnoreBlockers (research-kind
	// continuous dispatch, main.go's dispatchKindResearch wiring), only
	// that DepsOf-failed switch case's own guard is bypassed, so such an
	// issue IS counted into HeldBack; the unresolved-blocker-edge
	// exclusion above is untouched by cfg.IgnoreBlockers (it is gated
	// solely on !cfg.PreResolved) and so a blocker-edge-blocked issue
	// stays excluded from HeldBack even under cfg.IgnoreBlockers --
	// unlike engine.go's drainMaxJobs, which additionally gates the
	// blocker-edge computation itself on !cfg.IgnoreBlockers and so skips
	// it entirely in that mode (#2778 review finding).
	HeldBack int
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
