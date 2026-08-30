package waves

// Claimer is the one-method claim seam Queue embeds -- the subset a
// one-shot dispatch entry point needs without depending on the rest of
// Queue (issue #2919's spec: "the one-shot Dispatch entry point takes a
// Claimer alongside its existing pre-built input").
type Claimer interface {
	// Claim marks num claimed -- e.g. the Dispatchable-to-InProgress label
	// transition -- immediately before dispatch. A failed claim (a stale
	// listing racing a concurrent claimant) means skip, never an error the
	// caller must propagate; an implementation for which claiming is
	// already someone else's job (a caller whose own discovery claimed as
	// a side effect) documents Claim as a no-op instead.
	Claim(num string) error
}

// Queue is the wave engine's work-supply seam (issue #2937, spec #2919):
// where dispatchable work comes from, how it's claimed, how many
// candidates remain queued, and where a finished stale-drain report goes.
// RunContinuous takes one in place of the discovery/claim/pending/report
// closures its callers used to supply piecemeal.
type Queue interface {
	Claimer

	// Discover returns the current dispatchable Batch -- the sealed
	// candidate issues, their blocker edges, each blocker's provenance, and
	// the set of issues whose own readiness check failed transiently (see
	// Batch's own doc comment). RunContinuous calls it once at startup and
	// again before every slot refill, retrying on a rate-limit error up to
	// Config.TransientRetryMax; a !PreResolved caller with no PendingCount
	// also takes one extra call at stale-drain time to report heldBack.
	Discover() (Batch, error)

	// Pending reports how many candidates remain queued, as a quiet count:
	// no claim, no discovery side effect, safe to call purely for a report.
	Pending() int

	// ReportStaleDrain delivers a finished stale-drain report to the
	// Queue's own destination -- stdout and a log file, a session banner,
	// or wherever else an implementation's operator watches.
	ReportStaleDrain(report StaleDrainReport)
}

// QueueFromDiscoverer wraps discover as a Queue whose Claim, Pending, and
// ReportStaleDrain are no-ops -- for tests and call sites that only need
// Discover satisfied and don't exercise the rest of the seam yet.
func QueueFromDiscoverer(discover func() (Batch, error)) Queue {
	return discoverQueue(discover)
}

type discoverQueue func() (Batch, error)

func (d discoverQueue) Discover() (Batch, error)          { return d() }
func (d discoverQueue) Claim(string) error                { return nil }
func (d discoverQueue) Pending() int                      { return 0 }
func (d discoverQueue) ReportStaleDrain(StaleDrainReport) {}
