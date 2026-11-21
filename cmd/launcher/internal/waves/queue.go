package waves

import (
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
	// Dispatchable-to-InProgress transition. A failed claim (a stale listing
	// racing a concurrent claimant) means skip, never an error to propagate.
	Claim(num string) error
}

// Queue is the wave engine's work-supply seam (issue #2937, spec #2919).
type Queue interface {
	Claimer

	// Discover returns the current dispatchable Batch. RunContinuous calls it
	// at startup and before every slot refill, retrying a rate-limit error up
	// to Config.Policy.Max. It is never called just to report heldBack;
	// Pending covers that.
	Discover() (Batch, error)

	// Pending reports how many candidates remain queued, without claiming or
	// discovery side effects. claimed is the caller's record of this run's
	// claims (issue #3035); do not retain it past the call. err is non-nil
	// only when the count could not be determined, never with a partial value.
	Pending(claimed map[string]bool) (int, error)

	// ReportStaleDrain delivers a finished stale-drain report to the Queue's
	// own destination.
	ReportStaleDrain(report StaleDrainReport)

	// EnsureLogDirExists idempotently makes the Queue's on-disk log directory,
	// so RunContinuous need not derive it from a threaded pwd (issue #3036).
	EnsureLogDirExists() error
}

// NewHeadlessQueue adapts discover, claimer, and pending into a Queue for
// headless RunContinuous callers; pwd locates the stale-drain log file. Claim
// makes the real Dispatchable to InProgress transition (issue #2938). pending
// must be a quiet, unlogged listing that takes Pending's claimed set, so a
// stale listing cannot inflate it (#2939).
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
	fmt.Print(report.Stdout())
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
