package settle

import (
	"errors"
	"fmt"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/retry"
)

// retryingRelay wraps br so every bundle relay shares one retry-with-backoff
// seam. A nil br stays nil, so callers' nil checks keep meaning "no relay".
func (s *Settle) retryingRelay(num string, gen uint64, br forge.BundleRelay) forge.BundleRelay {
	if br == nil {
		return nil
	}
	return &retryingBundleRelay{
		backoff:    s.transientBackoff(),
		maxRetries: s.cfg.Policy.Max,
		stopped:    func() bool { return s.terminated(num, gen) },
		num:        num,
		inner:      br,
	}
}

type retryingBundleRelay struct {
	backoff    retry.LinearBackoff
	maxRetries int
	stopped    func() bool
	num        string // log lines only
	inner      forge.BundleRelay
}

// RelayBundle retries every failure whatever its class: the relay re-fetches a
// fixed bundle and force-pushes the same ref with a lease, so a repeat is safe,
// and a transient forge outage (a ~40s GitHub SSH auth blip stranded #4600 and
// #4606) is indistinguishable from a permanent one at this layer.
// ErrBundleNotFound is the exception: it is the local fact that the Box wrote
// no bundle, which no amount of waiting changes. An operator stop during a
// backoff returns errAbandoned wrapping the last relay error.
func (r *retryingBundleRelay) RelayBundle(outboxDir, ref string) error {
	for attempt := 1; ; attempt++ {
		err := r.inner.RelayBundle(outboxDir, ref)
		if err == nil || errors.Is(err, forge.ErrBundleNotFound) || attempt > r.maxRetries {
			return err
		}
		fmt.Printf("    #%s  landing=%s  status=relay-retry  attempt=%d/%d  !! %v\n",
			r.num, ref, attempt, r.maxRetries, err)
		if r.backoff.DoUnless(attempt, r.stopped) {
			return fmt.Errorf("%w: %w", errAbandoned, err)
		}
	}
}
