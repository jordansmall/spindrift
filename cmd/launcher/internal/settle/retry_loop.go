package settle

import (
	"fmt"

	"spindrift.dev/launcher/internal/retry"
)

// retryLoop holds the configuration for run, the attempt loop every settle
// retry wrapper shares, so their backoff, stop handling, and errAbandoned wrap
// cannot drift apart (#5174).
type retryLoop struct {
	backoff    retry.LinearBackoff
	maxRetries int
	stopped    func() bool
	num        string // log lines only
}

// newRetryLoop builds issue num's loop; an operator stop of generation gen ends it.
func (s *Settle) newRetryLoop(num string, gen uint64) retryLoop {
	return retryLoop{
		backoff:    s.transientBackoff(),
		maxRetries: s.cfg.Policy.Max,
		stopped:    func() bool { return s.terminated(num, gen) },
		num:        num,
	}
}

// run calls attempt until it succeeds, fails with an error final reports (nil
// final: none), or exhausts maxRetries, logging status=<status> before each
// backoff. afterBackoff, when non-nil, runs after each backoff, and true from
// it ends the loop with success. A non-empty finalStatus gives the exhausted
// attempt one more logged backoff and afterBackoff call before its error is
// returned, so Policy.Max=0 still waits once. An operator stop during a
// backoff returns errAbandoned wrapping the last error.
func (l retryLoop) run(status, finalStatus, landing string, attempt func() error, final func(error) bool, afterBackoff func() bool) error {
	for n := 1; ; n++ {
		err := attempt()
		if err == nil || (final != nil && final(err)) {
			return err
		}
		exhausted := n > l.maxRetries
		if exhausted && finalStatus == "" {
			return err
		}
		st, of := status, l.maxRetries
		if exhausted {
			st, of = finalStatus, l.maxRetries+1
		}
		fmt.Printf("    #%s  landing=%s  status=%s  attempt=%d/%d  !! %v\n",
			l.num, landing, st, n, of, err)
		if l.backoff.DoUnless(n, l.stopped) {
			return fmt.Errorf("%w: %w", errAbandoned, err)
		}
		if afterBackoff != nil && afterBackoff() {
			return nil
		}
		if exhausted {
			return err
		}
	}
}
