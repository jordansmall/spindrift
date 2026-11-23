package connectorcall_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/connectorcall"
	"spindrift.dev/launcher/internal/connectorforge"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectortracker"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/retry"
)

// TestClassifyAcrossAxes drives the forge and tracker axes through the one
// shared path: each error class against an idempotent and a never-retried
// method.
func TestClassifyAcrossAxes(t *testing.T) {
	failure := func(c connectorhost.Class) error {
		// Wrapped as connectorhost.Connector.Call wraps a guest error.
		return fmt.Errorf("%w: m: %w", connectorhost.ErrCall, &connectorhost.CallError{Class: c, Message: "boom"})
	}
	backoff := []time.Duration{connectorcall.BackoffUnit, 2 * connectorcall.BackoffUnit}
	axes := []struct {
		name   string
		policy connectorcall.Policy
		idem   string
		single []string // the axis's never-retried methods, listed here rather than read from the policy
	}{
		{"forge", connectorforge.CallPolicy(), "forge.pr", []string{"forge.merge", "forge.openPR", "forge.comment"}},
		{"tracker", connectortracker.CallPolicy(), connectortracker.CapEditLabels, []string{
			connectortracker.CapComment, connectortracker.CapFileIssue, connectortracker.CapCreateLabel, connectortracker.CapTransition,
		}},
	}
	cases := []struct {
		name      string
		single    bool // call each of the axis's never-retried methods
		errs      []error
		wantCalls int
		wantSleep []time.Duration
		wantClass connectorhost.Class // "" for success
	}{
		{"transient then success", false, []error{failure(connectorhost.Transient), nil}, 2, backoff[:1], ""},
		{"transient exhausts the attempts", false, []error{failure(connectorhost.Transient), failure(connectorhost.Transient), failure(connectorhost.Transient)}, 3, backoff, connectorhost.Transient},
		{"permanent called once", false, []error{failure(connectorhost.Permanent)}, 1, nil, connectorhost.Permanent},
		{"transient on a never-retried method called once", true, []error{failure(connectorhost.Transient)}, 1, nil, connectorhost.Transient},
		{"rate limited", false, []error{failure(connectorhost.RateLimited)}, 1, nil, connectorhost.RateLimited},
		{"auth", false, []error{failure(connectorhost.Auth)}, 1, nil, connectorhost.Auth},
	}
	for _, ax := range axes {
		for _, tc := range cases {
			methods := []string{ax.idem}
			if tc.single {
				methods = ax.single
			}
			for _, method := range methods {
				t.Run(ax.name+"/"+tc.name+"/"+method, func(t *testing.T) {
					var slept []time.Duration
					clock := retry.Clock{Now: time.Now, Sleep: func(d time.Duration) { slept = append(slept, d) }}
					calls := 0
					raw := func(context.Context, string, any, any) error {
						calls++
						return tc.errs[calls-1]
					}
					err := connectorcall.Classify(raw, clock, ax.policy)(context.Background(), method, nil, nil)

					if calls != tc.wantCalls {
						t.Errorf("calls = %d, want %d", calls, tc.wantCalls)
					}
					if !slices.Equal(slept, tc.wantSleep) {
						t.Errorf("slept %v, want %v", slept, tc.wantSleep)
					}
					if got := connectorhost.ErrorClass(err); got != tc.wantClass {
						t.Errorf("class = %q, want %q (err %v)", got, tc.wantClass, err)
					}
					if tc.wantClass == "" {
						if err != nil {
							t.Fatalf("err = %v, want nil", err)
						}
						return
					}
					var rl *forge.RateLimitError
					switch tc.wantClass {
					case connectorhost.RateLimited:
						if !errors.Is(err, forge.ErrRateLimit) || !errors.As(err, &rl) {
							t.Fatalf("err = %v, want a *forge.RateLimitError", err)
						}
						// A Connector's CallError carries no reset on the wire.
						if !rl.Reset.IsZero() {
							t.Errorf("Reset = %v, want zero", rl.Reset)
						}
					default:
						if errors.Is(err, forge.ErrRateLimit) {
							t.Errorf("err = %v matches ErrRateLimit", err)
						}
					}
					if got, want := errors.Is(err, forge.ErrAuthFailure), tc.wantClass == connectorhost.Auth; got != want {
						t.Errorf("matches ErrAuthFailure = %v, want %v", got, want)
					}
				})
			}
		}
	}
}
