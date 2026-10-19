package runnertest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Call scripts one invocation of a fake CLI binary: the exit code it
// returns and the stdout/stderr it prints. Real podman/docker print their
// name-collision refusal to stderr, not stdout, so a test pinning that
// refusal must use stderr to actually exercise the stream Run tees.
type Call struct {
	Exit   int
	Stdout string
	Stderr string
}

// NewFakeCLI writes a stub runtime binary that records each invocation's argv
// to call-NN.txt (zero-indexed) in a temp dir and exits/prints per the scripted
// calls in order. Once the invocation count exceeds len(calls), the last
// scripted call repeats. Returns the script path and the dir holding the
// recorded calls.
func NewFakeCLI(t *testing.T, calls ...Call) (script, dir string) {
	t.Helper()
	if len(calls) == 0 {
		t.Fatal("NewFakeCLI: at least one scripted call required")
	}
	dir = t.TempDir()
	script = filepath.Join(dir, "fake-cli")

	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	fmt.Fprintf(&b, "n=$(ls %q/call-*.txt 2>/dev/null | wc -l)\n", dir)
	fmt.Fprintf(&b, "printf '%%s\\n' \"$@\" > %q/call-$(printf '%%02d' $n).txt\n", dir)
	b.WriteString("case $n in\n")
	for i, c := range calls {
		pattern := fmt.Sprintf("%d", i)
		if i == len(calls)-1 {
			pattern += "|*"
		}
		fmt.Fprintf(&b, "%s) printf '%%s' %q; printf '%%s' %q >&2; exit %d ;;\n", pattern, c.Stdout, c.Stderr, c.Exit)
	}
	b.WriteString("esac\n")

	if err := os.WriteFile(script, []byte(b.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	return script, dir
}

// ReadCall returns the argv (split on newline) recorded for the n-th
// (zero-indexed) invocation of a fake CLI built by NewFakeCLI.
func ReadCall(t *testing.T, dir string, n int) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("call-%02d.txt", n)))
	if err != nil {
		t.Fatalf("call-%02d.txt not written: %v", n, err)
	}
	return strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
}

// CallCount returns the number of invocations recorded for a fake CLI built
// by NewFakeCLI.
func CallCount(t *testing.T, dir string) int {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "call-*.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return len(matches)
}

// StubExec routes the exec seam (e.g. runner.ExecCommand) to a fake CLI that
// replays call, restoring the seam when the test ends.
func StubExec(t *testing.T, seam *func(string, ...string) *exec.Cmd, call Call) {
	t.Helper()
	script, _ := NewFakeCLI(t, call)
	orig := *seam
	t.Cleanup(func() { *seam = orig })
	*seam = func(name string, args ...string) *exec.Cmd {
		return exec.Command(script, args...)
	}
}
