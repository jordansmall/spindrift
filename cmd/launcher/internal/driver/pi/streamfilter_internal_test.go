package pi

import (
	"bytes"
	"testing"
)

// coalesced cannot fail on a line line() held, so this seeds flushHeld directly.
func TestFlushHeldWritesRawLinesWhenCoalescingFails(t *testing.T) {
	var out bytes.Buffer
	f := &streamFilter{w: &out, held: []heldDelta{
		{line: "bad one", index: 0, delta: "a"},
		{line: "bad two", index: 0, delta: "b"},
	}}
	if err := f.flushHeld(); err != nil {
		t.Fatal(err)
	}
	if want := "bad one\nbad two\n"; out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
}
