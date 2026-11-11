package connectorhost_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/connectorhost"
)

// The hostile fixtures are real Go-built Connectors whose connector_call
// misbehaves as named; each must fail closed without taking the host down or
// hanging the caller.
func TestHostileFixtureConnectorsFailClosed(t *testing.T) {
	tests := []struct {
		fixture string
		want    error
	}{
		{"hostile-loop.wasm", connectorhost.ErrBudget},
		{"hostile-trap.wasm", connectorhost.ErrTrap},
		{"hostile-memory.wasm", connectorhost.ErrMemoryCap},
		{"hostile-oversize.wasm", connectorhost.ErrOutputTooLarge},
		{"hostile-malformed.wasm", connectorhost.ErrMalformedOutput},
		{"hostile-invalid.wasm", connectorhost.ErrMalformedOutput},
	}
	for _, tc := range tests {
		t.Run(tc.fixture, func(t *testing.T) {
			c, err := load(t, tc.fixture)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			start := time.Now()
			err = c.Call(context.Background(), "misbehave", nil, nil)
			if d := time.Since(start); d > connectorhost.CallBudget+slack {
				t.Errorf("Call took %v, want a prompt return", d)
			}
			if !errors.Is(err, connectorhost.ErrCall) {
				t.Errorf("err = %v, want ErrCall", err)
			}
			assertOnlySentinel(t, err, tc.want)
			if err := c.Call(context.Background(), "misbehave", nil, nil); !errors.Is(err, connectorhost.ErrUnusable) {
				t.Errorf("Call after the failure: err = %v, want ErrUnusable", err)
			}
			// The host survives: a fresh well-behaved Connector still works.
			good, err := load(t, "call.wasm")
			if err != nil {
				t.Fatalf("Load after the hostile call: %v", err)
			}
			var out string
			if err := good.Call(context.Background(), "echo", "ok", &out); err != nil || out != "ok" {
				t.Errorf("good Connector echo = %q, %v", out, err)
			}
		})
	}
}
