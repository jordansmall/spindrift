// Package connectorcall is core's one retry-and-error-class path for tracker
// and forge Connector calls: each axis declares its own Policy and wraps its
// raw call in Classify.
package connectorcall

import (
	"context"
	"fmt"
	"slices"
	"time"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/retry"
)

// The retry schedule for every Connector call core makes: three attempts, two
// seconds longer between each.
const (
	Attempts    = 3
	BackoffUnit = 2 * time.Second
)

// Call is a Connector call: connectorhost.Connector.Call's shape.
type Call = func(ctx context.Context, method string, params, result any) error

// Policy is one axis's retry policy.
type Policy struct {
	// NotRetried are the methods whose request is not idempotent; each is
	// called once.
	NotRetried []string
	// OnRetryFailure, when non-nil, runs on every failed attempt after the
	// first and returns the error to carry on with; nil turns the failure into
	// success.
	OnRetryFailure func(method string, err error) error
}

// Classify is raw made to read as core does: a transient failure is retried
// per p, then the Connector's error class is mapped onto core's sentinels (a
// rate limit matches forge.ErrRateLimit and an auth failure
// forge.ErrAuthFailure). The class stays readable through
// connectorhost.ErrorClass. A zero clock sleeps for real.
func Classify(raw Call, clock retry.Clock, p Policy) Call {
	clock = RealIfZero(clock)
	b := retry.LinearBackoff{Unit: BackoffUnit, Clock: clock}
	return func(ctx context.Context, method string, params, result any) error {
		attempt := 0
		attempts := Attempts
		if slices.Contains(p.NotRetried, method) {
			attempts = 1
		}
		err := retryTransient(attempts, b, func() error {
			attempt++
			err := raw(ctx, method, params, result)
			if attempt > 1 && err != nil && p.OnRetryFailure != nil {
				return p.OnRetryFailure(method, err)
			}
			return err
		})
		switch connectorhost.ErrorClass(err) {
		case connectorhost.RateLimited:
			return &forge.RateLimitError{Err: err}
		case connectorhost.Auth:
			return fmt.Errorf("%w: %w", forge.ErrAuthFailure, err)
		}
		return err
	}
}

// RealIfZero is clock, or retry.RealClock() when it carries no Sleep.
func RealIfZero(clock retry.Clock) retry.Clock {
	if clock.Sleep == nil {
		return retry.RealClock()
	}
	return clock
}

// retryTransient calls fn up to attempts times, sleeping b.Do(n) after the nth
// failure, and retries only a Transient Connector error. Any other error
// returns at once: RateLimited belongs to core's rate-limit hold, and a short
// loop there would only burn its budget. It returns the last error.
func retryTransient(attempts int, b retry.LinearBackoff, fn func() error) error {
	var err error
	for n := 1; ; n++ {
		if err = fn(); err == nil || connectorhost.ErrorClass(err) != connectorhost.Transient || n >= attempts {
			return err
		}
		b.Do(n)
	}
}
