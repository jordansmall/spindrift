// Package testutil holds test-only helpers shared across cmd/launcher's
// internal packages.
package testutil

import (
	"os"
	"strings"
	"testing"
)

// CaptureStderr runs fn with os.Stderr redirected to a pipe and returns
// everything written to it.
func CaptureStderr(t *testing.T, fn func()) string {
	t.Helper()
	return capture(t, &os.Stderr, fn)
}

// CaptureStdout runs fn with os.Stdout redirected to a pipe and returns
// everything written to it.
func CaptureStdout(t *testing.T, fn func()) string {
	t.Helper()
	return capture(t, &os.Stdout, fn)
}

// capture redirects *target to a pipe for the duration of fn and returns
// everything written to it. The read runs concurrently in a goroutine
// started before fn(), not after w.Close(): fn() would otherwise deadlock
// writing past the OS pipe buffer (~64KiB on Linux) with nothing draining it.
func capture(t *testing.T, target **os.File, fn func()) string {
	t.Helper()
	orig := *target
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	*target = w

	done := make(chan string)
	go func() {
		var buf strings.Builder
		tmp := make([]byte, 4096)
		for {
			n, rerr := r.Read(tmp)
			if n > 0 {
				buf.Write(tmp[:n])
			}
			if rerr != nil {
				break
			}
		}
		done <- buf.String()
	}()

	fn()

	w.Close()
	*target = orig

	out := <-done
	r.Close()
	return out
}
