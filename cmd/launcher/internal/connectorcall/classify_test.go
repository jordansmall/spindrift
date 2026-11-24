package connectorcall_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/connectorcall"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectorprotocol"
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
		axis   connectorprotocol.Axis
		idem   string
		single []string // the axis's never-retried methods, listed here rather than read from the table
	}{
		{"forge", connectorprotocol.AxisForge, "forge.pr", []string{"forge.merge", "forge.openPR", "forge.comment"}},
		{"tracker", connectorprotocol.AxisTracker, connectortracker.CapEditLabels, []string{
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
					err := connectorcall.Classify(raw, clock, axisCaps(ax.axis))(context.Background(), method, nil, nil)

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

// axisCaps is every capability the protocol declares for axis.
func axisCaps(axis connectorprotocol.Axis) []string {
	var caps []string
	for name, a := range connectorprotocol.Capabilities {
		if a == axis {
			caps = append(caps, name)
		}
	}
	return caps
}

func TestClassifyDoneOnAnsweredOnRetry(t *testing.T) {
	failure := func(c connectorhost.Class) error {
		return fmt.Errorf("%w: m: %w", connectorhost.ErrCall, &connectorhost.CallError{Class: c, Message: "boom"})
	}
	clock := retry.Clock{Now: time.Now, Sleep: func(time.Duration) {}}
	caps := axisCaps(connectorprotocol.AxisForge)
	for _, tc := range []struct {
		name      string
		errs      []error
		wantCalls int
		wantClass connectorhost.Class // "" for success
	}{
		{"not-found after a transient failure is success", []error{failure(connectorhost.Transient), failure(connectorhost.NotFound)}, 2, ""},
		{"not-found on the first attempt stays an error", []error{failure(connectorhost.NotFound)}, 1, connectorhost.NotFound},
		{"another class on a retry stays an error", []error{failure(connectorhost.Transient), failure(connectorhost.Permanent)}, 2, connectorhost.Permanent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			raw := func(context.Context, string, any, any) error {
				calls++
				return tc.errs[calls-1]
			}
			err := connectorcall.Classify(raw, clock, caps)(context.Background(), connectorprotocol.MethodForgeDeleteBranch, nil, nil)
			if calls != tc.wantCalls {
				t.Errorf("calls = %d, want %d", calls, tc.wantCalls)
			}
			if got := connectorhost.ErrorClass(err); got != tc.wantClass {
				t.Errorf("class = %q, want %q (err %v)", got, tc.wantClass, err)
			}
		})
	}
}

func TestClassifyRefusesWhatTheTableDoesNotAllow(t *testing.T) {
	for _, tc := range []struct {
		name, method string
		caps         []string
		wantIn       string
	}{
		{"unadvertised capability", connectorprotocol.MethodForgeDeleteBranch, []string{connectorprotocol.CapForge}, connectorprotocol.CapForgeBranches},
		{"unknown method", "forge.noSuchMethod", axisCaps(connectorprotocol.AxisForge), "forge.noSuchMethod"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			raw := func(context.Context, string, any, any) error { calls++; return nil }
			err := connectorcall.Classify(raw, retry.Clock{}, tc.caps)(context.Background(), tc.method, nil, nil)
			if !errors.Is(err, connectorhost.ErrProtocolUnsupported) {
				t.Fatalf("err = %v, want ErrProtocolUnsupported", err)
			}
			if !strings.Contains(err.Error(), tc.wantIn) || !strings.Contains(err.Error(), tc.method) {
				t.Errorf("err = %q, want it to name %q and %q", err, tc.method, tc.wantIn)
			}
			if calls != 0 {
				t.Errorf("raw called %d times, want 0", calls)
			}
		})
	}
}
