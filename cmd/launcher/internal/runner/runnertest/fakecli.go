package runnertest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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
	// Shell is a /bin/sh snippet run before the call prints and exits, with
	// the recording dir as cwd, so a scripted call can create, test or wait on
	// marker files (never named call-*) and `exit` early to override Exit.
	// Empty means no prelude.
	Shell string
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
	writeRecordPrelude(&b, dir)
	b.WriteString("case $n in\n")
	for i, c := range calls {
		pattern := fmt.Sprintf("%d", i)
		if i == len(calls)-1 {
			pattern += "|*"
		}
		fmt.Fprintf(&b, "%s) %s ;;\n", pattern, callBody(dir, c))
	}
	b.WriteString("esac\n")

	if err := os.WriteFile(script, []byte(b.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	return script, dir
}

// NewFakeCLIBySubcommand is NewFakeCLI keyed on the invocation's first
// argument (the runtime subcommand: inspect, run, ps, rm, ...) instead of its
// position, so a test mixing subcommands on one adapter needs no call-order
// bookkeeping. It records argv to the same call-NN.txt files, so ReadCall and
// CallCount work unchanged. A subcommand with no scripted Call fails with exit
// 127 and a stderr message naming it.
func NewFakeCLIBySubcommand(t *testing.T, calls map[string]Call) (script, dir string) {
	t.Helper()
	if len(calls) == 0 {
		t.Fatal("NewFakeCLIBySubcommand: at least one scripted subcommand required")
	}
	dir = t.TempDir()
	script = filepath.Join(dir, "fake-cli")

	var b strings.Builder
	writeRecordPrelude(&b, dir)
	b.WriteString("case $1 in\n")
	names := make([]string, 0, len(calls))
	for name := range calls {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(&b, "%q) %s ;;\n", name, callBody(dir, calls[name]))
	}
	b.WriteString("*) printf 'fake-cli: unscripted subcommand %s\\n' \"$1\" >&2; exit 127 ;;\n")
	b.WriteString("esac\n")

	if err := os.WriteFile(script, []byte(b.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	return script, dir
}

// writeRecordPrelude emits the shared script head: it sets n to the zero-based
// invocation index and records argv to call-NN.txt.
func writeRecordPrelude(b *strings.Builder, dir string) {
	b.WriteString("#!/bin/sh\n")
	fmt.Fprintf(b, "n=$(ls %q/call-*.txt 2>/dev/null | wc -l)\n", dir)
	fmt.Fprintf(b, "printf '%%s\\n' \"$@\" > %q/call-$(printf '%%02d' $n).txt\n", dir)
}

// callBody renders c as the shell commands of one case arm.
func callBody(dir string, c Call) string {
	var b strings.Builder
	if c.Shell != "" {
		fmt.Fprintf(&b, "cd %q; %s\n", dir, c.Shell)
	}
	fmt.Fprintf(&b, "printf '%%s' %q; printf '%%s' %q >&2; exit %d", c.Stdout, c.Stderr, c.Exit)
	return b.String()
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
