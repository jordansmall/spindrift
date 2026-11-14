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
	for engine, ld := range loadersFor(t) {
		t.Run(engine, func(t *testing.T) {
			for _, tc := range tests {
				t.Run(tc.fixture, func(t *testing.T) {
					c, err := loadUsing(t, ld, tc.fixture)
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
					good, err := loadUsing(t, ld, "call.wasm")
					if err != nil {
						t.Fatalf("Load after the hostile call: %v", err)
					}
					var out string
					if err := good.Call(context.Background(), "echo", "ok", &out); err != nil || out != "ok" {
						t.Errorf("good Connector echo = %q, %v", out, err)
					}
				})
			}
		})
	}
}

// Err is how a caller whose API has no error return (a Decoder) learns that the
// Connector failed closed; a deliberate Close is not a failure.
func TestErrReportsTheFirstFailure(t *testing.T) {
	c, err := load(t, "hostile-trap.wasm")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Err(); err != nil {
		t.Fatalf("Err before any call = %v, want nil", err)
	}
	_ = c.Call(context.Background(), "misbehave", nil, nil)
	_ = c.Call(context.Background(), "misbehave", nil, nil) // refused as ErrUnusable
	if err := c.Err(); !errors.Is(err, connectorhost.ErrTrap) {
		t.Errorf("Err after a trap = %v, want ErrTrap", err)
	}
}

func TestErrIgnoresClose(t *testing.T) {
	c, err := load(t, "hostile-trap.wasm")
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close(context.Background())
	if err := c.Err(); err != nil {
		t.Errorf("Err after Close = %v, want nil", err)
	}
}

// Abort ends a call another goroutine holds mu for, which Close cannot: it
// waits on the same mutex. The caller gets a prompt, ordinary failure and the
// Connector is dead.
func TestAbortInterruptsAnInFlightCall(t *testing.T) {
	c, err := load(t, "hostile-loop.wasm")
	if err != nil {
		t.Fatal(err)
	}
	errc := make(chan error, 1)
	go func() { errc <- c.Call(context.Background(), "misbehave", nil, nil) }()

	time.Sleep(200 * time.Millisecond) // let the guest enter its loop
	start := time.Now()
	c.Abort()
	select {
	case err := <-errc:
		if d := time.Since(start); d > connectorhost.CallBudget/2 {
			t.Errorf("Call returned %v after Abort, want far inside its %v budget", d, connectorhost.CallBudget)
		}
		if !errors.Is(err, connectorhost.ErrCall) || errors.Is(err, connectorhost.ErrBudget) {
			t.Errorf("err = %v, want ErrCall without ErrBudget", err)
		}
	case <-time.After(connectorhost.CallBudget + slack):
		t.Fatal("Call still running after Abort")
	}
	if c.Err() == nil {
		t.Error("Err = nil after an aborted call, want the failure")
	}
	if err := c.Call(context.Background(), "misbehave", nil, nil); !errors.Is(err, connectorhost.ErrUnusable) {
		t.Errorf("Call after Abort: err = %v, want ErrUnusable", err)
	}
}
