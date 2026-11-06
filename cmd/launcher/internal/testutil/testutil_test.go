package testutil

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCaptureStderr_ReturnsWrittenOutput(t *testing.T) {
	out := CaptureStderr(t, func() {
		fmt.Fprint(os.Stderr, "hello stderr")
	})
	if out != "hello stderr" {
		t.Fatalf("CaptureStderr output = %q, want %q", out, "hello stderr")
	}
}

func TestCaptureStdout_ReturnsWrittenOutput(t *testing.T) {
	out := CaptureStdout(t, func() {
		fmt.Fprint(os.Stdout, "hello stdout")
	})
	if out != "hello stdout" {
		t.Fatalf("CaptureStdout output = %q, want %q", out, "hello stdout")
	}
}

// TestCaptureStdout_LargeOutput guards against the deadlock that reappears
// if capture ever goes back to reading after fn() returns: a write past the
// OS pipe buffer (~64KiB on Linux) would block fn() forever with nothing
// draining the pipe.
func TestCaptureStdout_LargeOutput(t *testing.T) {
	want := strings.Repeat("x", 200*1024)
	got := make(chan string, 1)
	go func() {
		got <- CaptureStdout(t, func() {
			fmt.Print(want)
		})
	}()
	select {
	case g := <-got:
		if g != want {
			t.Fatalf("CaptureStdout: got %d bytes, want %d", len(g), len(want))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("CaptureStdout: timed out after 5s (deadlock regression)")
	}
}

func TestGitOutput_ReturnsTrimmedStdout(t *testing.T) {
	dir := t.TempDir()
	GitRun(t, dir, "init")
	GitRun(t, dir, "config", "user.name", "Test Name")
	if got := GitOutput(t, dir, "config", "--get", "user.name"); got != "Test Name" {
		t.Fatalf("GitOutput = %q, want %q", got, "Test Name")
	}
}

func TestGitOutput_ExcludesStderr(t *testing.T) {
	dir := t.TempDir()
	GitRun(t, dir, "init")
	// "Switched to a new branch" goes to stderr only.
	if got := GitOutput(t, dir, "checkout", "-b", "x"); got != "" {
		t.Fatalf("GitOutput = %q, want empty (stderr must not leak into the result)", got)
	}
}
