package connectorhost_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
)

func TestCallEchoRoundTrips(t *testing.T) {
	c, err := load(t, "call.wasm")
	if err != nil {
		t.Fatal(err)
	}
	in := map[string]any{"a": []any{1.0, "two"}, "b": map[string]any{"c": true}}
	var out map[string]any
	if err := c.Call(context.Background(), "echo", in, &out); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(out) != fmt.Sprint(in) {
		t.Errorf("echo = %v, want %v", out, in)
	}
}

func TestCallNilResultIgnoresResponse(t *testing.T) {
	c, err := load(t, "call.wasm")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Call(context.Background(), "echo", "x", nil); err != nil {
		t.Fatal(err)
	}
}

func TestCallLargePayload(t *testing.T) {
	c, err := load(t, "call.wasm")
	if err != nil {
		t.Fatal(err)
	}
	in := strings.Repeat("x", 1<<20)
	var out string
	if err := c.Call(context.Background(), "echo", in, &out); err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Errorf("echoed %d bytes, want %d", len(out), len(in))
	}
}

func TestCallErrors(t *testing.T) {
	c, err := load(t, "call.wasm")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ method, want string }{
		{"fail", "fixture failure"},
		{"nope", `unknown method "nope"`},
	} {
		err := c.Call(context.Background(), tc.method, nil, nil)
		if !errors.Is(err, connectorhost.ErrCall) {
			t.Errorf("%s: err = %v, want ErrCall", tc.method, err)
		} else if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %q, want it to contain %q", tc.method, err, tc.want)
		}
	}
}

func TestCallConcurrent(t *testing.T) {
	c, err := load(t, "call.wasm")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var out int
			if err := c.Call(context.Background(), "echo", i, &out); err != nil || out != i {
				t.Errorf("echo(%d) = %d, %v", i, out, err)
			}
		}()
	}
	wg.Wait()
}
