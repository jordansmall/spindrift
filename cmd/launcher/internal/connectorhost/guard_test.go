package connectorhost_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
)

// A Connector is untrusted, so each way its call exports can lie about memory
// or size must fail the call rather than read or write out of bounds.
func TestCallGuardsAgainstHostileGuest(t *testing.T) {
	const resp = `{"result":5}`
	hs, all := connectorFns(nil, allocOff, respOff<<32|int64(len(resp)))
	seg := []dataSeg{{0, hs}, {respOff, []byte(resp)}}
	build := func(alloc, call int64) []byte {
		_, fns := connectorFns(nil, alloc, call)
		return guestModule(nil, seg, fns...)
	}
	tests := []struct {
		name string
		wasm []byte
		want string // "" means the call must succeed
	}{
		{"well-formed", guestModule(nil, seg, all...), ""},
		{"no call exports", guestModule(nil, seg, all[0]), "does not export"},
		{"no connector_call", guestModule(nil, seg, all[:2]...), "does not export"},
		{"no connector_alloc", guestModule(nil, seg, all[0], all[2]), "does not export"},
		{"response over the cap", build(allocOff, respOff<<32|(64<<20+1)), "exceeds"},
		{"alloc past memory", build(page, respOff<<32|int64(len(resp))), "connector_alloc"},
		{"response pointer past memory", build(allocOff, page<<32|int64(len(resp))), "outside module memory"},
		{"response length past memory", build(allocOff, respOff<<32|(page+1)), "outside module memory"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := loadWasm(t, tc.wasm)
			var out int
			err := c.Call(context.Background(), "m", nil, &out)
			if tc.want == "" {
				if err != nil || out != 5 {
					t.Fatalf("Call = %d, %v, want 5", out, err)
				}
				return
			}
			if !errors.Is(err, connectorhost.ErrCall) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want ErrCall mentioning %q", err, tc.want)
			}
		})
	}
}
