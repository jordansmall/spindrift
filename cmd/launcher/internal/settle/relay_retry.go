package settle

import (
	"errors"

	"spindrift.dev/launcher/internal/forge"
)

// retryingRelay wraps br so every bundle relay shares one retry-with-backoff
// seam. A nil br stays nil, so callers' nil checks keep meaning "no relay".
func (s *Settle) retryingRelay(num string, gen uint64, br forge.BundleRelay) forge.BundleRelay {
	if br == nil {
		return nil
	}
	return &retryingBundleRelay{
		retryLoop: s.newRetryLoop(num, gen),
		inner:     br,
	}
}

type retryingBundleRelay struct {
	retryLoop
	inner forge.BundleRelay
}

// RelayBundle retries every failure whatever its class: the relay re-fetches a
// fixed bundle and force-pushes the same ref with a lease, so a repeat is safe,
// and a transient forge outage (a ~40s GitHub SSH auth blip stranded #4600 and
// #4606) is indistinguishable from a permanent one at this layer.
// ErrBundleNotFound is the exception: it is the local fact that the Box wrote
// no bundle, which no amount of waiting changes.
func (r *retryingBundleRelay) RelayBundle(outboxDir, ref string) error {
	return r.run("relay-retry", "", ref, // "": exhaustion returns at once
		func() error { return r.inner.RelayBundle(outboxDir, ref) },
		func(err error) bool { return errors.Is(err, forge.ErrBundleNotFound) },
		nil) // no afterBackoff hook
}
