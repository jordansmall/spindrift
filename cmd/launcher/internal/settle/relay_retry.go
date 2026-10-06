package settle

import (
	"errors"
	"fmt"

	"spindrift.dev/launcher/internal/forge"
)

// retryingRelay wraps br so every bundle relay shares one retry-with-backoff
// seam. A nil br stays nil, so callers' nil checks keep meaning "no relay".
func (s *Settle) retryingRelay(num string, gen uint64, br forge.BundleRelay) forge.BundleRelay {
	if br == nil {
		return nil
	}
	return &retryingBundleRelay{s: s, num: num, gen: gen, inner: br}
}

type retryingBundleRelay struct {
	s     *Settle
	num   string
	gen   uint64
	inner forge.BundleRelay
}

// RelayBundle retries every failure whatever its class: the relay re-fetches a
// fixed bundle and force-pushes the same ref with a lease, so a repeat is safe,
// and a transient forge outage (a ~40s GitHub SSH auth blip stranded #4600 and
// #4606) is indistinguishable from a permanent one at this layer.
// ErrBundleNotFound is the exception: it is the local fact that the Box wrote
// no bundle, which no amount of waiting changes. An operator stop during a
// backoff returns errAbandoned wrapping the last relay error.
func (r *retryingBundleRelay) RelayBundle(outboxDir, ref string) error {
	backoff := r.s.transientBackoff()
	for attempt := 1; ; attempt++ {
		err := r.inner.RelayBundle(outboxDir, ref)
		if err == nil || errors.Is(err, forge.ErrBundleNotFound) || attempt > r.s.cfg.Policy.Max {
			return err
		}
		fmt.Printf("    #%s  landing=%s  status=relay-retry  attempt=%d/%d  !! %v\n",
			r.num, ref, attempt, r.s.cfg.Policy.Max, err)
		if backoff.DoUnless(attempt, func() bool { return r.s.terminated(r.num, r.gen) }) {
			return fmt.Errorf("%w: %w", errAbandoned, err)
		}
	}
}
