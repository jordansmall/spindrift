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

// RelayBundle retries a failure whatever its class: a repeat re-pushes a fixed
// bundle with a lease, so it is safe, and a transient outage (a ~40s GitHub
// SSH auth blip stranded #4600 and #4606) is indistinguishable from a
// permanent one here. Only ErrBundleNotFound and ErrWorkflowPermissionDenied,
// which waiting cannot change, are not retried.
func (r *retryingBundleRelay) RelayBundle(outboxDir, ref string) error {
	return r.fresh().run(StatusRelayRetry, "", ref, // "": exhaustion returns at once
		func() error { return r.inner.RelayBundle(outboxDir, ref) },
		func(err error) bool {
			return errors.Is(err, forge.ErrBundleNotFound) || errors.Is(err, forge.ErrWorkflowPermissionDenied)
		},
		nil) // no afterBackoff hook
}
