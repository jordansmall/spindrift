package connectorforge

import (
	"errors"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/retry"
)

func classed(c connectorhost.Class) error {
	return &connectorhost.CallError{Class: c, Message: "x"}
}

func TestRetryTransient(t *testing.T) {
	var slept []time.Duration
	b := retry.LinearBackoff{Unit: time.Second, Clock: retry.Clock{
		Now:   time.Now,
		Sleep: func(d time.Duration) { slept = append(slept, d) },
	}}
	for _, tc := range []struct {
		name      string
		errs      []error
		attempts  int
		wantCalls int
		wantErr   error
		wantSleep []time.Duration
	}{
		{"transient then success", []error{classed(connectorhost.Transient), classed(connectorhost.Transient), nil}, 5, 3, nil, []time.Duration{time.Second, 2 * time.Second}},
		{"permanent once", []error{classed(connectorhost.Permanent)}, 5, 1, classed(connectorhost.Permanent), nil},
		{"rate limited once", []error{classed(connectorhost.RateLimited)}, 5, 1, classed(connectorhost.RateLimited), nil},
		{"unclassed once", []error{errors.New("host side")}, 5, 1, errors.New("host side"), nil},
		{"exhausted", []error{classed(connectorhost.Transient), classed(connectorhost.Transient), classed(connectorhost.Transient)}, 3, 3, classed(connectorhost.Transient), []time.Duration{time.Second, 2 * time.Second}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			slept = nil
			calls := 0
			err := retryTransient(tc.attempts, b, func() error {
				e := tc.errs[calls]
				calls++
				return e
			})
			if calls != tc.wantCalls {
				t.Errorf("calls = %d, want %d", calls, tc.wantCalls)
			}
			if (err == nil) != (tc.wantErr == nil) || (err != nil && err.Error() != tc.wantErr.Error()) {
				t.Errorf("err = %v, want %v", err, tc.wantErr)
			}
			if len(slept) != len(tc.wantSleep) {
				t.Fatalf("slept %v, want %v", slept, tc.wantSleep)
			}
			for i := range slept {
				if slept[i] != tc.wantSleep[i] {
					t.Errorf("slept %v, want %v", slept, tc.wantSleep)
				}
			}
		})
	}
}
