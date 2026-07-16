package testutil

import (
	"fmt"
	"os"
	"testing"
)

func TestCaptureStderr_ReturnsWrittenOutput(t *testing.T) {
	out := CaptureStderr(t, func() {
		fmt.Fprint(os.Stderr, "hello stderr")
	})
	if out != "hello stderr" {
		t.Fatalf("CaptureStderr output = %q, want %q", out, "hello stderr")
	}
}
