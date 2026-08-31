package waves

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"spindrift.dev/launcher/internal/dispatch"
)

// staleDrainMarker is the file a stale-drain report gets appended to, under
// .spindrift/logs/ (#2678) -- named like engine.go's blockedMarker rather
// than left as a scattered string literal.
const staleDrainMarker = "stale-drain.log"

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
	// Config.Policy.Max. It never gets called purely to report
	// heldBack at stale-drain time -- Pending covers that instead.
	Discover() (Batch, error)

	// Pending reports how many candidates remain queued, as a quiet count:
	// no claim, no discovery side effect, safe to call purely for a report.
	// err is non-nil only when the count could not be determined (a
	// transient query failure), never a partial/best-effort value.
	Pending() (int, error)

	// ReportStaleDrain delivers a finished stale-drain report to the
	// Queue's own destination -- stdout and a log file, a session banner,
	// or wherever else an implementation's operator watches.
	ReportStaleDrain(report StaleDrainReport)
}

// QueueFromDiscoverer wraps discover as a Queue whose Claim and
// ReportStaleDrain are no-ops and whose Pending errors -- for tests and call
// sites that only need Discover satisfied and don't exercise the rest of
// the seam yet.
func QueueFromDiscoverer(discover func() (Batch, error)) Queue {
	return discoverQueue(discover)
}

type discoverQueue func() (Batch, error)

func (d discoverQueue) Discover() (Batch, error) { return d() }
func (d discoverQueue) Claim(string) error       { return nil }

// Pending always errors rather than fabricating a count: this adapter is
// for Discover-only call sites, so it has no real queued-candidate count to
// report. A caller that reaches Pending anyway (despite QueueFromDiscoverer
// being documented as Discover-only) gets routed to RunContinuous's
// heldBackUnknown path by this error, rather than being handed a
// confirmed-looking 0. A caller that needs a real heldBack count
// (RunContinuous's stale-transition branch) must use NewHeadlessQueue or
// the Console adapter instead.
func (d discoverQueue) Pending() (int, error) {
	return 0, errors.New("waves: QueueFromDiscoverer has no Pending count; use NewHeadlessQueue or the Console adapter instead")
}
func (d discoverQueue) ReportStaleDrain(StaleDrainReport) {}

// NewHeadlessQueue adapts discover, claimer, and pending into a Queue for
// headless RunContinuous callers. Discover() delegates straight to discover;
// Claim() performs the real Dispatchable->InProgress transition through
// claimer (issue #2938), unlike QueueFromDiscoverer's no-op, and records the
// claimed issue so Pending() can exclude it; Pending() delegates to pending,
// a quiet, side-effect-free, unlogged listing the caller supplies (main.go
// wires this to a queryOpenIssues-only closure that, unlike discover, never
// calls logDiscoveryPoll), passing it the set of issues claimed so far this
// run so an eventually-consistent listing can't inflate the count (issue
// #2939); pwd locates the stale-drain log file ReportStaleDrain appends to.
func NewHeadlessQueue(discover func() (Batch, error), claimer Claimer, pending func(map[string]bool) (int, error), pwd string) Queue {
	return headlessQueue{discover: discover, claimer: claimer, pending: pending, claimed: make(map[string]bool), pwd: pwd}
}

// headlessQueue is NewHeadlessQueue's implementation -- see that
// constructor's doc comment for what each field backs.
type headlessQueue struct {
	discover func() (Batch, error)
	claimer  Claimer
	pending  func(map[string]bool) (int, error)
	claimed  map[string]bool
	pwd      string
}

func (q headlessQueue) Discover() (Batch, error) { return q.discover() }

// Claim performs the real claim through q.claimer, then -- only on success --
// records num in q.claimed for Pending to exclude. Mutating the map is safe
// on this value receiver since maps are reference types; both Claim and
// Pending are only ever called from inside RunContinuous's refill closure,
// itself always under RunContinuous's own mutex, so no extra locking is
// needed here.
func (q headlessQueue) Claim(num string) error {
	if err := q.claimer.Claim(num); err != nil {
		return err
	}
	q.claimed[num] = true
	return nil
}

func (q headlessQueue) Pending() (int, error) { return q.pending(q.claimed) }

// ReportStaleDrain prints report to stdout and appends its HostLog line to
// q.pwd's stale-drain.log, swallowing any file error to stderr -- the same
// behavior continuous.go's former emitStaleDrainReport had (#2678), minus
// any Console forwarding: Console has its own ReportStaleDrain implementation
// (runContinuousQueue.ReportStaleDrain in console/launcher.go) unrelated to
// this method.
func (q headlessQueue) ReportStaleDrain(report StaleDrainReport) {
	fmt.Print(report.Console())
	logPath := filepath.Join(dispatch.HostLogDirFor(q.pwd), staleDrainMarker)
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "continuous: open %s: %v\n", logPath, err)
		return
	}
	defer logFile.Close()
	if _, err := logFile.WriteString(report.HostLog()); err != nil {
		fmt.Fprintf(os.Stderr, "continuous: write %s: %v\n", logPath, err)
	}
}
