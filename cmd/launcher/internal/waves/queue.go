package waves

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"spindrift.dev/launcher/internal/dispatch"
)

// staleDrainMarker is the file a stale-drain report gets appended to, under
// .spindrift/logs/.
const staleDrainMarker = "stale-drain.log"

// Claimer is the one-method claim seam Queue embeds -- the subset a one-shot
// dispatch entry point needs without depending on the rest of Queue.
type Claimer interface {
	// Claim marks num claimed -- e.g. the Dispatchable-to-InProgress label
	// transition -- immediately before dispatch. A failed claim (a stale
	// listing racing a concurrent claimant) means skip, never an error the
	// caller must propagate; an implementation for which claiming is already
	// someone else's job documents Claim as a no-op instead.
	Claim(num string) error
}

// Queue is the wave engine's work-supply seam: where dispatchable work comes
// from, how it's claimed, how many candidates remain queued, and where a
// finished stale-drain report goes.
type Queue interface {
	Claimer

	// Discover returns the current dispatchable Batch (see Batch's own doc
	// comment). RunContinuous calls it once at startup and again before every
	// slot refill, retrying on a rate-limit error up to Config.Policy.Max. It
	// is never called purely to report heldBack at stale-drain time --
	// Pending covers that instead.
	Discover() (Batch, error)

	// Pending reports how many candidates remain queued, as a quiet count:
	// no claim, no discovery side effect, safe to call purely for a report.
	// err is non-nil only when the count could not be determined (a
	// transient query failure), never a partial/best-effort value.
	Pending() (int, error)

	// ReportStaleDrain delivers a finished stale-drain report to the Queue's
	// own destination -- stdout and a log file, a session banner, or wherever
	// else an implementation's operator watches.
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

// Pending always errors rather than fabricating a count: a caller that
// reaches it anyway is routed to RunContinuous's heldBackUnknown path instead
// of being handed a confirmed-looking 0.
func (d discoverQueue) Pending() (int, error) {
	return 0, errors.New("waves: QueueFromDiscoverer has no Pending count; use NewHeadlessQueue or the Console adapter instead")
}
func (d discoverQueue) ReportStaleDrain(StaleDrainReport) {}

// NewHeadlessQueue adapts discover, claimer, and pending into a Queue for
// headless RunContinuous callers. Claim performs the real
// Dispatchable->InProgress transition and records the claimed issue so
// Pending can exclude it. pending must be a quiet, side-effect-free, unlogged
// listing; it is handed the set of issues claimed so far this run so an
// eventually-consistent listing can't inflate the count. pwd locates the
// stale-drain log file ReportStaleDrain appends to.
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
// records num in q.claimed for Pending to exclude. No locking: both Claim and
// Pending are only ever called from RunContinuous's refill closure, itself
// always under RunContinuous's own mutex.
func (q headlessQueue) Claim(num string) error {
	if err := q.claimer.Claim(num); err != nil {
		return err
	}
	q.claimed[num] = true
	return nil
}

func (q headlessQueue) Pending() (int, error) { return q.pending(q.claimed) }

// ReportStaleDrain prints report to stdout and appends its HostLog line to
// q.pwd's stale-drain.log, swallowing any file error to stderr. No Console
// forwarding: Console has its own ReportStaleDrain implementation.
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
