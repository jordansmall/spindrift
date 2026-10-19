package runnertest

import (
	"bytes"
	"io"
	"os"
	"testing"
)

// CaptureStdoutDuring runs fn with os.Stdout redirected to a pipe and returns
// what was written.
func CaptureStdoutDuring(t *testing.T, fn func()) string {
	t.Helper()
	return captureDuring(t, &os.Stdout, fn)
}

// CaptureStderrDuring is CaptureStdoutDuring's sibling for os.Stderr: a
// warning written on the degrade path is a stderr fact.
func CaptureStderrDuring(t *testing.T, fn func()) string {
	t.Helper()
	return captureDuring(t, &os.Stderr, fn)
}

// captureDuring defers both the restore and w.Close so a panic in fn can
// neither strand *stream on the pipe for the rest of the package's tests nor
// leave the io.Copy below blocked on an unclosed writer.
func captureDuring(t *testing.T, stream **os.File, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := *stream
	*stream = w
	defer func() { *stream = orig }()
	func() {
		defer w.Close()
		fn()
	}()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}
