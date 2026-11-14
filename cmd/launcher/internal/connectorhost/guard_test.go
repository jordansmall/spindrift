package connectorhost_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/connectorhost"
)

// A Connector is untrusted, so each way its call exports can lie about memory,
// size or signature, or misbehave while running, must fail the call with its
// own named error rather than read or write out of bounds or hang the host.
// Every failure but a bad export closes the Connector.
func TestCallGuardsAgainstHostileGuest(t *testing.T) {
	const resp = `{"result":5}`
	respPtr := int64(respOff<<32 | len(resp))
	req, err := connectorhost.EncodeRequest("m", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, outCap := connectorhost.LimitsFor(len(req))
	build := func(alloc, call int64, edit func(fns []guestFn), data ...dataSeg) []byte {
		hs, fns := connectorFns(nil, alloc, call)
		if edit != nil {
			edit(fns)
		}
		return guestModule(nil, append([]dataSeg{{0, hs}, {respOff, []byte(resp)}}, data...), fns...)
	}
	answer := func(s string) []byte {
		return build(allocOff, respOff<<32|int64(len(s)), nil, dataSeg{respOff, []byte(s)})
	}
	body := func(i int, b []byte) func([]guestFn) { return func(fns []guestFn) { fns[i].body = b } }
	hs, all := connectorFns(nil, allocOff, respPtr)
	seg := []dataSeg{{0, hs}, {respOff, []byte(resp)}}
	tests := []struct {
		name string
		wasm []byte
		want error // nil means the call must succeed
	}{
		{"well-formed", guestModule(nil, seg, all...), nil},
		{"no call exports", guestModule(nil, seg, all[0]), connectorhost.ErrExport},
		{"no connector_call", guestModule(nil, seg, all[:2]...), connectorhost.ErrExport},
		{"no connector_alloc", guestModule(nil, seg, all[0], all[2]), connectorhost.ErrExport},
		{"alloc takes nothing", build(allocOff, respPtr, func(f []guestFn) { f[fnAlloc].params = nil }), connectorhost.ErrExport},
		{"alloc returns i64", build(allocOff, respPtr, func(f []guestFn) { f[fnAlloc].results, f[fnAlloc].body = []byte{i64}, i64Const(allocOff) }), connectorhost.ErrExport},
		{"call takes one parameter", build(allocOff, respPtr, func(f []guestFn) { f[fnCall].params = []byte{i32} }), connectorhost.ErrExport},
		{"call returns i32", build(allocOff, respPtr, func(f []guestFn) { f[fnCall].results, f[fnCall].body = []byte{i32}, i32Const(0) }), connectorhost.ErrExport},
		{"response over the cap", build(allocOff, respOff<<32|(int64(outCap)+1), nil), connectorhost.ErrOutputTooLarge},
		{"response over the cap and past memory", build(allocOff, respOff<<32|(1<<31), nil), connectorhost.ErrOutputTooLarge},
		{"alloc past memory", build(page, respPtr, nil), connectorhost.ErrMalformedOutput},
		{"response pointer past memory", build(allocOff, page<<32|int64(len(resp)), nil), connectorhost.ErrMalformedOutput},
		{"response length past memory", build(allocOff, respOff<<32|(page+1), nil), connectorhost.ErrMalformedOutput},
		{"response not JSON", answer("not"), connectorhost.ErrMalformedOutput},
		{"response not UTF-8", answer("\"\xff\""), connectorhost.ErrMalformedOutput},
		{"response not an envelope", answer("[]"), connectorhost.ErrMalformedOutput},
		{"result of the wrong type", answer(`{"result":"x"}`), connectorhost.ErrMalformedOutput},
		{"alloc traps", build(allocOff, respPtr, body(fnAlloc, []byte{unreachable})), connectorhost.ErrTrap},
		{"call traps", build(allocOff, respPtr, body(fnCall, []byte{unreachable})), connectorhost.ErrTrap},
		{"call loops", build(allocOff, respPtr, body(fnCall, append(loop, i64Const(0)...))), connectorhost.ErrBudget},
		{"call grows memory past the cap", build(allocOff, respPtr, body(fnCall, append(grow(capPages+1), i64Const(respPtr)...))), connectorhost.ErrMemoryCap},
	}
	for engine, ld := range loadersFor(t) {
		t.Run(engine, func(t *testing.T) {
			for _, tc := range tests {
				t.Run(tc.name, func(t *testing.T) {
					c := loadWasmUsing(t, ld, tc.wasm)
					var out int
					start := time.Now()
					err := c.Call(context.Background(), "m", nil, &out)
					if d := time.Since(start); d > connectorhost.CallBudget+slack {
						t.Errorf("Call took %v, want a prompt return", d)
					}
					if tc.want == nil {
						if err != nil || out != 5 {
							t.Fatalf("Call = %d, %v, want 5", out, err)
						}
						return
					}
					if !errors.Is(err, connectorhost.ErrCall) {
						t.Fatalf("err = %v, want ErrCall", err)
					}
					assertOnlySentinel(t, err, tc.want)
					again := c.Call(context.Background(), "m", nil, &out)
					if tc.want == connectorhost.ErrExport {
						// A missing export is the module's shape, not damage: it stays loaded.
						if errors.Is(again, connectorhost.ErrUnusable) {
							t.Errorf("a bad export closed the Connector")
						}
					} else if !errors.Is(again, connectorhost.ErrCall) || !errors.Is(again, connectorhost.ErrUnusable) {
						t.Errorf("Call after the failure: err = %v, want ErrCall and ErrUnusable", again)
					}
				})
			}
		})
	}
}

// A Connector-reported error is an answer, not a failure: it wraps ErrCall
// alone and the Connector stays usable.
func TestConnectorReportedErrorDoesNotPoison(t *testing.T) {
	c := loadWasm(t, answeringGuest(nil, `{"error":"boom"}`))
	for range 2 {
		err := c.Call(context.Background(), "m", nil, nil)
		if !errors.Is(err, connectorhost.ErrCall) || errors.Is(err, connectorhost.ErrUnusable) {
			t.Fatalf("err = %v, want ErrCall and not ErrUnusable", err)
		}
		for _, s := range sentinels {
			if errors.Is(err, s) {
				t.Errorf("errors.Is(err, %v) = true, err = %v", s, err)
			}
		}
	}
}

func TestCallAfterCloseIsUnusable(t *testing.T) {
	c := loadWasm(t, answeringGuest(nil, `{"result":null}`))
	if err := c.Call(context.Background(), "m", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.Call(context.Background(), "m", nil, nil); !errors.Is(err, connectorhost.ErrCall) || !errors.Is(err, connectorhost.ErrUnusable) {
		t.Fatalf("err = %v, want ErrCall and ErrUnusable", err)
	}
}

func TestCallerCancellationPoisonsConnector(t *testing.T) {
	c := loadWasm(t, answeringGuest(nil, `{"result":null}`))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := c.Call(ctx, "m", nil, nil)
	if !errors.Is(err, connectorhost.ErrCall) || !errors.Is(err, context.Canceled) || errors.Is(err, connectorhost.ErrBudget) {
		t.Fatalf("err = %v, want ErrCall and the caller's context.Canceled, not ErrBudget", err)
	}
	if err := c.Call(context.Background(), "m", nil, nil); !errors.Is(err, connectorhost.ErrUnusable) {
		t.Fatalf("err = %v, want ErrUnusable", err)
	}
}

// A failed call releases the module itself; the Close that follows, and a
// second one, must not release it again.
func TestCloseAfterFailedCallIsIdempotent(t *testing.T) {
	c := loadWasm(t, answeringGuest(nil, `{"result":null}`))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Call(ctx, "m", nil, nil); err == nil {
		t.Fatal("call under a cancelled context succeeded")
	}
	for range 2 {
		if err := c.Close(context.Background()); err != nil {
			t.Fatalf("Close = %v, want nil", err)
		}
	}
}

// The same, with a compilation cache, whose release is the engine's other half
// of close.
func TestCloseAfterFailedCallIsIdempotentCompiled(t *testing.T) {
	c, err := connectorhost.LoadCompiled(context.Background(), answeringGuest(nil, `{"result":null}`), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Call(ctx, "m", nil, nil); err == nil {
		t.Fatal("call under a cancelled context succeeded")
	}
	for range 2 {
		if err := c.Close(context.Background()); err != nil {
			t.Fatalf("Close = %v, want nil", err)
		}
	}
}
