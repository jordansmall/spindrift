package waves

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"spindrift.dev/launcher/internal/dispatch"
)

// staleDrainMarker is the stale-drain report file under .spindrift/logs/ (#2678).
const staleDrainMarker = "stale-drain.log"

// Claimer is the one-method claim seam Queue embeds, for a one-shot dispatch
// entry point that needs nothing else from Queue (issue #2919).
type Claimer interface {
	// Claim marks num claimed immediately before dispatch, e.g. the
	// Dispatchable-to-InProgress label transition. A failed claim (a stale
	// listing racing a concurrent claimant) means skip, never an error the
	// caller must propagate. An implementation whose caller already claimed as
	// a side effect documents Claim as a no-op instead.
	Claim(num string) error
}

// Queue is the wave engine's work-supply seam (issue #2937, spec #2919): where
// dispatchable work comes from, how the Queue claims it, how many candidates
// remain queued, and where a finished stale-drain report goes.
type Queue interface {
	Claimer

	// Discover returns the current dispatchable Batch. RunContinuous calls it
	// once at startup and again before every slot refill, retrying a
	// rate-limit error up to Config.Policy.Max. It is never called purely to
	// report heldBack; Pending covers that instead.
	Discover() (Batch, error)

	// Pending reports how many candidates remain queued, without claiming and
	// without any discovery side effect. claimed is the caller's own record of
	// what it has claimed this run (issue #3035); an implementation must not
	// retain the reference past the call. err is non-nil only when the count
	// could not be determined, never a partial or best-effort value.
	Pending(claimed map[string]bool) (int, error)

	// ReportStaleDrain delivers a finished stale-drain report to the Queue's
	// own destination.
	ReportStaleDrain(report StaleDrainReport)

	// EnsureLogDirExists idempotently makes whatever on-disk log directory this
	// Queue writes to, so RunContinuous does not derive and create that
	// directory itself from a separately threaded pwd (issue #3036). An
	// implementation with no log directory of its own is a documented no-op.
	EnsureLogDirExists() error
}

// QueueFromDiscoverer wraps discover as a Queue whose Claim and
// ReportStaleDrain are no-ops and whose Pending errors, for call sites that
// only need Discover.
func QueueFromDiscoverer(discover func() (Batch, error)) Queue {
	return discoverQueue(discover)
}

type discoverQueue func() (Batch, error)

func (d discoverQueue) Discover() (Batch, error) { return d() }
func (d discoverQueue) Claim(string) error       { return nil }

// Pending always errors rather than fabricating a count: the error routes a
// caller that reaches it anyway to RunContinuous's heldBackUnknown path instead
// of handing it a confirmed-looking 0.
func (d discoverQueue) Pending(map[string]bool) (int, error) {
	return 0, errors.New("waves: QueueFromDiscoverer has no Pending count; use NewHeadlessQueue or the Console adapter instead")
}
func (d discoverQueue) ReportStaleDrain(StaleDrainReport) {}

// EnsureLogDirExists is a no-op: this adapter never touches the filesystem.
func (d discoverQueue) EnsureLogDirExists() error { return nil }

// NewHeadlessQueue adapts discover, claimer, and pending into a Queue for
// headless RunContinuous callers; pwd locates the stale-drain log file. Claim
// makes the real Dispatchable to InProgress transition (issue #2938), unlike
// QueueFromDiscoverer's no-op. pending must be a quiet, unlogged listing that
// takes Pending's claimed set, so a stale listing cannot inflate it (#2939).
func NewHeadlessQueue(discover func() (Batch, error), claimer Claimer, pending func(map[string]bool) (int, error), pwd string) Queue {
	return headlessQueue{discover: discover, claimer: claimer, pending: pending, pwd: pwd}
}

type headlessQueue struct {
	discover func() (Batch, error)
	claimer  Claimer
	pending  func(map[string]bool) (int, error)
	pwd      string
}

func (q headlessQueue) Discover() (Batch, error) { return q.discover() }

func (q headlessQueue) Claim(num string) error { return q.claimer.Claim(num) }

func (q headlessQueue) Pending(claimed map[string]bool) (int, error) { return q.pending(claimed) }

// ReportStaleDrain prints report to stdout and appends its HostLog line to
// q.pwd's stale-drain.log, reporting a file error to stderr rather than
// failing (#2678).
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

// EnsureLogDirExists creates q.pwd's .spindrift/logs directory, the same path
// ReportStaleDrain's stale-drain.log lives under, idempotently (issue #3036).
func (q headlessQueue) EnsureLogDirExists() error {
	return os.MkdirAll(dispatch.HostLogDirFor(q.pwd), 0o755)
}
