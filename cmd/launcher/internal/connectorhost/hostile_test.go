package connectorhost_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/connectorhost"
)

// useShortCallBudget lowers the call budget to ShortCallBudget until t ends.
func useShortCallBudget(t *testing.T) {
	t.Helper()
	t.Cleanup(connectorhost.SetCallBudget(connectorhost.ShortCallBudget))
}

// engineCase is a runCasesPerEngine case: its subtest name, and the error its
// call must fail with (nil for none), which sorts it into a group.
type engineCase interface {
	caseName() string
	wantErr() error
}

type hostileCase struct {
	fixture string
	want    error
}

func (c hostileCase) caseName() string { return c.fixture }
func (c hostileCase) wantErr() error   { return c.want }

type handshakeCase struct {
	name string
	wasm []byte
	want error
}

func (c handshakeCase) caseName() string { return c.name }
func (c handshakeCase) wantErr() error   { return c.want }

type guardCase struct {
	name string
	wasm []byte
	want error // nil means the call must succeed
}

func (c guardCase) caseName() string { return c.name }
func (c guardCase) wantErr() error   { return c.want }

// runCasesPerEngine runs every case under every engine, in two serial groups
// so no case sees a budget it did not ask for. "budget" holds the cases that
// expect ErrBudget: it lowers the call budget to ShortCallBudget, and its
// engines and cases run in parallel so the kills overlap instead of queueing.
// "production" holds the rest under the production budget, which leaves their
// legitimate work room on a loaded runner. They touch no process-global state,
// so they run in parallel too unless overlapProduction is false: cases whose
// legitimate work is heavy, like a Go-built fixture filling its memory cap in
// the interpreter, starve one another past that budget when they overlap.
//
// The budget is written only on the budget group's own goroutine. Go resumes a
// group's parallel children after its body returns, and a serial t.Run returns
// only once those children and the group's Cleanup (the restore) are done, so
// "production" starts after "budget" has fully ended.
func runCasesPerEngine[C engineCase](t *testing.T, cases []C, overlapProduction bool, run func(t *testing.T, ld loader, c C, budget time.Duration)) {
	t.Helper()
	loaders := loadersFor(t)
	runGroup := func(label string, budget time.Duration, exhausts bool) {
		overlap := exhausts || overlapProduction
		t.Run(label, func(t *testing.T) {
			if exhausts {
				useShortCallBudget(t)
			}
			for engine, ld := range loaders {
				t.Run(engine, func(t *testing.T) {
					if overlap {
						t.Parallel()
					}
					for _, tc := range cases {
						if errors.Is(tc.wantErr(), connectorhost.ErrBudget) != exhausts {
							continue
						}
						t.Run(tc.caseName(), func(t *testing.T) {
							if overlap {
								t.Parallel()
							}
							run(t, ld, tc, budget)
						})
					}
				})
			}
		})
	}
	runGroup("budget", connectorhost.ShortCallBudget, true)
	runGroup("production", connectorhost.CallBudget, false)
}

// The hostile fixtures are real Go-built Connectors whose connector_call
// misbehaves as named; each must fail closed without taking the host down or
// hanging the caller.
func TestHostileFixtureConnectorsFailClosed(t *testing.T) {
	tests := []hostileCase{
		{"hostile-loop.wasm", connectorhost.ErrBudget},
		{"hostile-trap.wasm", connectorhost.ErrTrap},
		{"hostile-memory.wasm", connectorhost.ErrMemoryCap},
		{"hostile-oversize.wasm", connectorhost.ErrOutputTooLarge},
		{"hostile-malformed.wasm", connectorhost.ErrMalformedOutput},
		{"hostile-invalid.wasm", connectorhost.ErrMalformedOutput},
	}
	runCasesPerEngine(t, tests, false,
		func(t *testing.T, ld loader, tc hostileCase, budget time.Duration) {
			c, err := loadUsing(t, ld, tc.fixture)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			start := time.Now()
			err = c.Call(context.Background(), "misbehave", nil, nil)
			if d := time.Since(start); d > budget+slack {
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
