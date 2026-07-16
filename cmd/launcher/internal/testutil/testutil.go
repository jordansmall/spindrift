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
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w

	fn()

	w.Close()
	os.Stderr = orig

	var buf strings.Builder
	tmp := make([]byte, 4096)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf.Write(tmp[:n])
		}
		if err != nil {
			break
		}
	}
	return buf.String()
}
