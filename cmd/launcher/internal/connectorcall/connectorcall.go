// Package connectorcall is core's one retry-and-error-class path for tracker
// and forge Connector calls: each axis wraps its raw call in Classify, which
// reads retry safety and required capability from the protocol's method table.
package connectorcall

import (
	"context"
	"fmt"
	"slices"
	"time"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectorprotocol"
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

// Classify is raw made to read as core does: a call is checked against the
// protocol's method table, a transient failure is retried per the method's row,
// then the Connector's error class is mapped onto core's sentinels (a rate
// limit matches forge.ErrRateLimit and an auth failure forge.ErrAuthFailure).
// The class stays readable through connectorhost.ErrorClass. A method the table
// lacks, or whose capability caps (the Connector's advertised capabilities) do
// not list, fails with connectorhost.ErrProtocolUnsupported before reaching
// raw. A zero clock sleeps for real.
func Classify(raw Call, clock retry.Clock, caps []string) Call {
	clock = RealIfZero(clock)
	b := retry.LinearBackoff{Unit: BackoffUnit, Clock: clock}
	return func(ctx context.Context, method string, params, result any) error {
		m, ok := connectorprotocol.Methods[method]
		if !ok {
			return fmt.Errorf("%w: %s is not a protocol method", connectorhost.ErrProtocolUnsupported, method)
		}
		if m.Capability != "" && !slices.Contains(caps, m.Capability) {
			return fmt.Errorf("%w: %s needs capability %q", connectorhost.ErrProtocolUnsupported, method, m.Capability)
		}
		attempt := 0
		attempts := Attempts
		if !m.Retry.Safe {
			attempts = 1
		}
		err := retryTransient(attempts, b, func() error {
			attempt++
			err := raw(ctx, method, params, result)
			// A retry may find its own earlier attempt already landed, its
			// failure lost on the way back: that class is success.
			if attempt > 1 && err != nil && slices.Contains(m.Retry.DoneOn, string(connectorhost.ErrorClass(err))) {
				return nil
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
