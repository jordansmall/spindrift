package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/daemon"
	"spindrift.dev/launcher/internal/report"
)

// mustHostRunner is the call-site helper for the tests below that build a
// valid hostRunner and only care about using it, not about newHostRunner's
// error return — the tests that assert on newHostRunner's error return call
// it directly.
func mustHostRunner(t *testing.T, cfg hostRunnerConfig) *hostRunner {
	t.Helper()
	r, err := newHostRunner(cfg)
	if err != nil {
		t.Fatalf("newHostRunner() unexpected error: %v", err)
	}
	return r
}

// TestRunChild_ExitCode points the exec seam at a scripted shell command
// instead of nix (this repo's tests never shell out to nix), and asserts a
// non-zero exit comes back as ChildResult.Exit with no error.
func TestRunChild_ExitCode(t *testing.T) {
	orig := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = orig })

	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", "exit 2")
	}

	// Every runner the tests below use passes env: os.Environ() — a nil env
	// is rejected at construction now (TestNewHostRunner_RejectsNilEnv), and
	// the snapshot is load-bearing for the tests whose children shell out:
	// TestRunChild_ChildInOwnProcessGroup, TestRunChild_ChildStartedAfterBothLatchesClosedSeesBothKinds,
	// TestRunDoctor_CancelledContextTearsDownChild.
	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	got, err := r.RunChild(context.Background(), daemon.ChildRequest{Slot: 0, Kind: daemon.KindDispatch, Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"})
	if err != nil {
		t.Fatalf("RunChild() unexpected error: %v", err)
	}
	if got.Exit != 2 {
		t.Errorf("Exit = %d, want 2", got.Exit)
	}
}

// captureStream redirects *target (an os.Stdout/os.Stderr-shaped global) to
// a pipe for the caller and returns a func that restores the original and
// hands back everything written while redirected. Callers must not run
// t.Parallel: two tests swapping the same global would race. One test may
// capture two distinct globals at once, as the RunDoctor tests do, but must
// not capture the same global twice — the read func restores unconditionally,
// so the inner restore would hand the outer capture's writer back.
func captureStream(t *testing.T, target **os.File) func() []byte {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("captureStream: pipe: %v", err)
	}
	orig := *target
	*target = w
	// Drain concurrently rather than once the caller is done: a callee may
	// wire a child process straight to this descriptor and write past a
	// pipe's capacity before returning — RunChild does, and
	// TestRunChild_StdoutRelayedUnchanged deliberately writes 70 KiB. With
	// nothing reading, the child blocks in write() forever, so it never
	// exits, never closes its report descriptor, and wedges RunChild's read
	// of the report pipe.
	type capture struct {
		out []byte
		err error
	}
	done := make(chan capture, 1)
	go func() {
		out, err := io.ReadAll(r)
		done <- capture{out, err}
	}()
	t.Cleanup(func() {
		if *target == w {
			*target = orig
		}
		_ = w.Close()
		_ = r.Close()
	})
	return func() []byte {
		*target = orig
		_ = w.Close()
		got := <-done
		if got.err != nil {
			t.Fatalf("captureStream: read: %v", got.err)
		}
		return got.out
	}
}

// captureStderr redirects os.Stderr for the caller. RunChild relays a
// child's stdout/stderr and its own malformed-record diagnostic through
// os.Stderr, so this is the only observable route onto either from a test.
func captureStderr(t *testing.T) func() []byte {
	t.Helper()
	return captureStream(t, &os.Stderr)
}

// captureStdout redirects os.Stdout for the caller, for tests asserting the
// daemon keeps it clear of doctor's relayed output.
func captureStdout(t *testing.T) func() []byte {
	t.Helper()
	return captureStream(t, &os.Stdout)
}

// TestRunChild_OnRecordDeliversBoxAndSettledInOrder drives a scripted child
// that writes one "box" and one "settled" record to descriptor 3 (the
// report pipe RunChild hands it — see daemon.ReportFD/SPINDRIFT_REPORT_FD),
// and asserts both reach ChildRequest.OnRecord, in order, with every field
// intact: the live channel a slot has no other way to learn what a child is
// doing, or how it settled, before it exits (issue #3545, #3627).
func TestRunChild_OnRecordDeliversBoxAndSettledInOrder(t *testing.T) {
	orig := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = orig })

	script := `printf '%s\n%s\n' '{"event":"box","issue":"101","phase":"initial"}' '{"event":"settled","issue":"101","state":"merged"}' >&3; exit 0`
	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", script)
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})

	var mu sync.Mutex
	var got []daemon.Record
	req := daemon.ChildRequest{
		Slot:     0,
		Kind:     daemon.KindDispatch,
		Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		OnRecord: func(rec daemon.Record) {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, rec)
		},
	}
	if _, err := r.RunChild(context.Background(), req); err != nil {
		t.Fatalf("RunChild() unexpected error: %v", err)
	}
	want := []daemon.Record{
		{Event: "box", Issue: "101", Phase: "initial"},
		{Event: "settled", Issue: "101", State: "merged"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("OnRecord calls = %+v, want %+v", got, want)
	}
}

// TestRunChild_UnknownEventIgnored asserts an event daemon.ParseRecord
// doesn't recognise is ignored outright: no OnRecord call, no error, no
// stderr diagnostic, and the child's own clean exit still comes through —
// a forward-compatible reader must tolerate an event it doesn't understand
// yet (see ParseRecord's doc).
func TestRunChild_UnknownEventIgnored(t *testing.T) {
	orig := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = orig })

	script := `printf '%s\n' '{"event":"nope","issue":"42"}' >&3; exit 0`
	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", script)
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	readStderr := captureStderr(t)
	var got []daemon.Record
	req := daemon.ChildRequest{
		Slot: 0, Kind: daemon.KindDispatch, Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		OnRecord: func(rec daemon.Record) { got = append(got, rec) },
	}
	result, err := r.RunChild(context.Background(), req)
	if err != nil {
		t.Fatalf("RunChild() unexpected error: %v", err)
	}
	if result.Exit != 0 {
		t.Errorf("Exit = %d, want 0", result.Exit)
	}
	if len(got) != 0 {
		t.Errorf("OnRecord calls = %+v, want none", got)
	}
	if stderr := readStderr(); len(stderr) != 0 {
		t.Errorf("stderr = %q, want empty (an unknown event is not an error)", stderr)
	}
}

// TestRunChild_MalformedLinesReportOnceThenValidArrives drives a child that
// writes two malformed lines followed by one valid record: exactly one
// stderr diagnostic must come out (not two — a hostile or buggy child
// spamming bad lines must not spam the daemon's own log), and the valid
// record after them must still reach OnRecord.
func TestRunChild_MalformedLinesReportOnceThenValidArrives(t *testing.T) {
	orig := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = orig })

	script := `printf '%s\n%s\n%s\n' 'not json' 'also not json' '{"event":"box","issue":"7","phase":"fix-pass-1"}' >&3; exit 0`
	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", script)
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	readStderr := captureStderr(t)
	var got []daemon.Record
	req := daemon.ChildRequest{
		Slot: 0, Kind: daemon.KindDispatch, Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		OnRecord: func(rec daemon.Record) { got = append(got, rec) },
	}
	if _, err := r.RunChild(context.Background(), req); err != nil {
		t.Fatalf("RunChild() unexpected error: %v", err)
	}
	want := []daemon.Record{{Event: "box", Issue: "7", Phase: "fix-pass-1"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("OnRecord calls = %+v, want %+v", got, want)
	}
	stderr := string(readStderr())
	if n := strings.Count(stderr, "daemon: read child report:"); n != 1 {
		t.Errorf("stderr diagnostic count = %d, want exactly 1 (got stderr: %q)", n, stderr)
	}
}

// TestRunChild_OverLongLineDiscardedThenValidArrives drives a child that
// writes one unterminated line well past report.MaxLine, then a valid record:
// readReports must neither buffer the over-long run without limit nor wedge
// on it (issue #3627's review finding) — the over-long line is reported
// exactly once, like any other malformed line, and the record after it still
// reaches OnRecord.
func TestRunChild_OverLongLineDiscardedThenValidArrives(t *testing.T) {
	orig := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = orig })

	// 5000 'x' bytes with no newline is comfortably past report.MaxLine
	// (4096); the trailing printf supplies the newline that ends it, then a
	// well-formed record on its own line.
	script := `head -c 5000 /dev/zero | tr '\0' 'x' >&3; printf '\n%s\n' '{"event":"box","issue":"9","phase":"initial"}' >&3; exit 0`
	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", script)
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	readStderr := captureStderr(t)
	var got []daemon.Record
	req := daemon.ChildRequest{
		Slot: 0, Kind: daemon.KindDispatch, Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		OnRecord: func(rec daemon.Record) { got = append(got, rec) },
	}
	if _, err := r.RunChild(context.Background(), req); err != nil {
		t.Fatalf("RunChild() unexpected error: %v", err)
	}
	want := []daemon.Record{{Event: "box", Issue: "9", Phase: "initial"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("OnRecord calls = %+v, want %+v", got, want)
	}
	stderr := string(readStderr())
	if n := strings.Count(stderr, "daemon: read child report:"); n != 1 {
		t.Errorf("stderr diagnostic count = %d, want exactly 1 (got stderr: %q)", n, stderr)
	}
}

// TestRunChild_SettledLongNoteDeliveredOneRecord closes issue #3627's review
// finding at the point it was reported: a Box's SPINDRIFT_OUTCOME note=<~5KB
// prose> threads through settle/gate.go into report.Settled, and before the
// fix the resulting JSON line exceeded report.MaxLine, so readReports hit
// bufio.ErrBufferFull and discarded the whole record (0 delivered). The
// bytes this test feeds fd 3 are produced by the real report.Reporter over a
// throwaway pipe — not hand-rolled JSON — so the assertion exercises
// report.emit's own clipping, not a stand-in for it. With the fix, the note
// is clipped before the line ever reaches the wire, so exactly one record
// reaches onRecord with the right issue and state.
func TestRunChild_SettledLongNoteDeliveredOneRecord(t *testing.T) {
	orig := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = orig })

	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	rep := report.FromEnv(func(k string) string {
		if k == "SPINDRIFT_REPORT_FD" {
			return fmt.Sprint(pw.Fd())
		}
		return ""
	}, io.Discard)
	if rep == nil {
		t.Fatalf("report.FromEnv: got nil Reporter")
	}
	note := strings.Repeat("a", 5000)
	rep.Settled("123", "blocked", note)
	pw.Close()
	line, err := io.ReadAll(pr)
	if err != nil {
		t.Fatalf("read produced record: %v", err)
	}
	pr.Close()

	dir := t.TempDir()
	lineFile := filepath.Join(dir, "line.json")
	if err := os.WriteFile(lineFile, line, 0o644); err != nil {
		t.Fatalf("write lineFile: %v", err)
	}

	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", `cat "$1" >&3`, "sh", lineFile)
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	readStderr := captureStderr(t)
	var got []daemon.Record
	req := daemon.ChildRequest{
		Slot: 0, Kind: daemon.KindDispatch, Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		OnRecord: func(rec daemon.Record) { got = append(got, rec) },
	}
	if _, err := r.RunChild(context.Background(), req); err != nil {
		t.Fatalf("RunChild() unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("OnRecord calls = %+v, want exactly 1", got)
	}
	if got[0].Event != "settled" || got[0].Issue != "123" || got[0].State != "blocked" {
		t.Errorf("record = %+v, want event=settled issue=123 state=blocked", got[0])
	}
	if stderr := readStderr(); len(stderr) != 0 {
		t.Errorf("stderr = %q, want empty (a within-bound record is not malformed)", stderr)
	}
}

// TestRunChild_StdoutRelayedUnchanged asserts a child's stdout reaches the
// daemon's stderr byte-for-byte, with no userspace copy: the child prints a
// line matching the old (now-deleted) announce regex, followed by a line
// past both bufio's 64 KiB default and the old raised 1 MiB cap, proving
// the cap is really gone rather than just raised further.
func TestRunChild_StdoutRelayedUnchanged(t *testing.T) {
	orig := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = orig })

	dir := t.TempDir()
	bigLine := strings.Repeat("a", 70*1024) + "\n"
	bigFile := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(bigFile, []byte(bigLine), 0o644); err != nil {
		t.Fatalf("write bigFile: %v", err)
	}
	literalLine := "    -> #42 (fix-pass-1): title\n"

	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", `cat "$1"; printf '%s' "$2"`, "sh", bigFile, literalLine)
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: dir, appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	readStderr := captureStderr(t)
	req := daemon.ChildRequest{
		Slot: 0, Kind: daemon.KindDispatch, Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		OnRecord: func(rec daemon.Record) {
			t.Errorf("OnRecord called with %+v, want none (nothing written to the report pipe)", rec)
		},
	}
	result, err := r.RunChild(context.Background(), req)
	if err != nil {
		t.Fatalf("RunChild() unexpected error: %v", err)
	}
	if result.Exit != 0 {
		t.Errorf("Exit = %d, want 0", result.Exit)
	}
	want := bigLine + literalLine
	if got := string(readStderr()); got != want {
		t.Errorf("relayed stdout does not match byte-for-byte\ngot len=%d\nwant len=%d", len(got), len(want))
	}
}

// TestRunChild_CleanExitNoRecords pins the other end of the report-pipe
// seam: a clean exit with nothing written to descriptor 3 reports Exit 0
// and fires OnRecord not at all.
func TestRunChild_CleanExitNoRecords(t *testing.T) {
	orig := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = orig })

	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", `printf 'nothing to see\n'; exit 0`)
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	var got []daemon.Record
	req := daemon.ChildRequest{
		Slot: 0, Kind: daemon.KindResearch, Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		OnRecord: func(rec daemon.Record) { got = append(got, rec) },
	}
	result, err := r.RunChild(context.Background(), req)
	if err != nil {
		t.Fatalf("RunChild() unexpected error: %v", err)
	}
	if result.Exit != 0 {
		t.Errorf("Exit = %d, want 0", result.Exit)
	}
	if len(got) != 0 {
		t.Errorf("OnRecord calls = %+v, want none", got)
	}
}

// envDumpScript has the child report the three variables below through
// shell builtins alone, writing them to the file its %q names. These
// tests hand the child a fixture PATH, so nothing external to the shell
// — env(1) included — is reachable from inside it.
const envDumpScript = `{ echo "MODEL=${MODEL-<unset>}"; echo "PATH=${PATH-<unset>}"; echo "GH_TOKEN=${GH_TOKEN-<unset>}"; } >%q; exit 0`

// assertEnvStripsKnobsKeepsSecrets is the shared assertion for
// TestRunChild_EnvStripsKnobsKeepsSecrets and
// TestRunDoctor_EnvStripsKnobsKeepsSecrets below: the daemon-side knob
// MODEL must never reach the child, while a non-knob (PATH) and a
// secret-shaped var (GH_TOKEN) must pass through untouched.
func assertEnvStripsKnobsKeepsSecrets(t *testing.T, dumped string) {
	t.Helper()
	got := strings.Split(strings.TrimRight(dumped, "\n"), "\n")
	want := []string{"MODEL=<unset>", "PATH=/bin:/usr/bin", "GH_TOKEN=super-secret"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("child env = %v, want %v", got, want)
	}
}

// TestRunChild_EnvStripsKnobsKeepsSecrets drives the exec seam with a
// scripted child that dumps its own environment to a temp file (RunChild's
// stdout now goes straight to os.Stderr with no userspace copy, so a temp
// file is the observable route here, same as RunDoctor's test below).
func TestRunChild_EnvStripsKnobsKeepsSecrets(t *testing.T) {
	orig := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = orig })

	dumpFile := filepath.Join(t.TempDir(), "env.out")
	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", fmt.Sprintf(envDumpScript, dumpFile))
	}

	r := mustHostRunner(t, hostRunnerConfig{
		repoPath:   t.TempDir(),
		appAttr:    ".#",
		baseBranch: "main",
		selfAttr:   ".#daemon",
		nixSystem:  "x86_64-linux",
		env:        []string{"MODEL=from-shell", "PATH=/bin:/usr/bin", "GH_TOKEN=super-secret"},
		knobs:      []string{"MODEL"},
	})
	got, err := r.RunChild(context.Background(), daemon.ChildRequest{Slot: 0, Kind: daemon.KindDispatch, Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"})
	if err != nil {
		t.Fatalf("RunChild() unexpected error: %v", err)
	}
	if got.Exit != 0 {
		t.Fatalf("Exit = %d, want 0", got.Exit)
	}

	dumped, err := os.ReadFile(dumpFile)
	if err != nil {
		t.Fatalf("read dumped env: %v", err)
	}
	assertEnvStripsKnobsKeepsSecrets(t, string(dumped))
}

// TestRunChild_ChildInOwnProcessGroup asserts the child started through the
// runnerExecCommand seam is isolated into its own process group (Setpgid),
// so a group-wide Ctrl-C SIGINT never reaches it — only the daemon's own
// forwarded SIGTERM does (issue #3538). The child reports its own $$ to a
// file rather than the test reading cmd.Process from outside RunChild:
// exec.Cmd's Process field is written by cmd.Start() with no synchronization
// a test can observe now that nothing in the runner publishes the started
// process, so a bare pointer read here would be a genuine data race.
func TestRunChild_ChildInOwnProcessGroup(t *testing.T) {
	orig := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = orig })
	pidFile := filepath.Join(t.TempDir(), "child-pid")
	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", `echo $$ >"$0"; sleep 0.3`, pidFile)
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := r.RunChild(context.Background(), daemon.ChildRequest{Slot: 0, Kind: daemon.KindDispatch, Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"}); err != nil {
			t.Errorf("RunChild() unexpected error: %v", err)
		}
	}()

	var pid int
	for i := 0; i < 100; i++ {
		if raw, err := os.ReadFile(pidFile); err == nil {
			if n, err := fmt.Sscanf(strings.TrimSpace(string(raw)), "%d", &pid); err == nil && n == 1 {
				break
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("child never started")
	}

	childPgid, err := syscall.Getpgid(pid)
	if err != nil {
		t.Fatalf("Getpgid(child): %v", err)
	}
	selfPgid, err := syscall.Getpgid(os.Getpid())
	if err != nil {
		t.Fatalf("Getpgid(self): %v", err)
	}
	if childPgid == selfPgid {
		t.Errorf("child pgid %d == test process pgid %d, want isolated group", childPgid, selfPgid)
	}

	<-done
}

// TestRunChild_StdinIsDevNull pins RunChild's deliberate choice not to set
// cmd.Stdin: the child's own process group (Setpgid, issue #3538) makes a
// tty read a SIGTTIN stop, so dogfood.sh's mid-loop vault-unlock prompt has
// no home under the daemon (issue #3548), and nothing may silently hand a
// child the daemon's terminal. Capturing the seam's *exec.Cmd is race-free
// here, unlike reading cmd.Process: RunChild writes Stdin, if at all,
// before cmd.Start() and never after.
func TestRunChild_StdinIsDevNull(t *testing.T) {
	orig := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = orig })
	var captured *exec.Cmd
	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		captured = exec.Command("/bin/sh", "-c", "true")
		return captured
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	if _, err := r.RunChild(context.Background(), daemon.ChildRequest{Slot: 0, Kind: daemon.KindDispatch, Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"}); err != nil {
		t.Fatalf("RunChild() unexpected error: %v", err)
	}

	if captured.Stdin != nil {
		t.Errorf("cmd.Stdin = %v, want nil (default /dev/null)", captured.Stdin)
	}
}

func gitRunT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if dir == "" {
		cmd = exec.Command("git", args...)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func writeFileT(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitOutputT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

// TestResolveTip_FetchesWithoutMutatingWorkingTree builds a bare
// "origin" plus two clones: dirConsumer (the operator's checkout under
// test) and dirAdvancer, which pushes a second commit to origin after
// dirConsumer was created. ResolveTip(dirConsumer, ...) must then
// resolve to that second commit while leaving dirConsumer's own HEAD,
// branch, and working tree exactly as they were — a fetch, never a pull.
func TestResolveTip_FetchesWithoutMutatingWorkingTree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	root := t.TempDir()
	bare := filepath.Join(root, "origin.git")
	dirConsumer := filepath.Join(root, "consumer")
	dirAdvancer := filepath.Join(root, "advancer")

	gitRunT(t, "", "init", "--bare", bare)

	gitRunT(t, "", "clone", bare, dirConsumer)
	gitRunT(t, dirConsumer, "checkout", "-B", "main")
	gitRunT(t, dirConsumer, "config", "user.email", "consumer@example.com")
	gitRunT(t, dirConsumer, "config", "user.name", "Consumer")
	writeFileT(t, filepath.Join(dirConsumer, "a.txt"), "a\n")
	gitRunT(t, dirConsumer, "add", "a.txt")
	gitRunT(t, dirConsumer, "commit", "-m", "base")
	gitRunT(t, dirConsumer, "push", "-u", "origin", "main")

	consumerHeadBefore := gitOutputT(t, dirConsumer, "rev-parse", "HEAD")
	consumerBranchBefore := gitOutputT(t, dirConsumer, "rev-parse", "--abbrev-ref", "HEAD")
	consumerStatusBefore := gitOutputT(t, dirConsumer, "status", "--porcelain")

	// Advance origin's tip from a second clone, so dirConsumer's own local
	// main ref is now behind origin/main.
	gitRunT(t, "", "clone", bare, dirAdvancer)
	gitRunT(t, dirAdvancer, "checkout", "main")
	gitRunT(t, dirAdvancer, "config", "user.email", "advancer@example.com")
	gitRunT(t, dirAdvancer, "config", "user.name", "Advancer")
	writeFileT(t, filepath.Join(dirAdvancer, "b.txt"), "b\n")
	gitRunT(t, dirAdvancer, "add", "b.txt")
	gitRunT(t, dirAdvancer, "commit", "-m", "advance")
	gitRunT(t, dirAdvancer, "push", "origin", "main")
	wantTip := strings.TrimSpace(gitOutputT(t, dirAdvancer, "rev-parse", "HEAD"))

	// selfAttr left empty: this test is about the fetch half only, and an
	// empty selfAttr is what keeps ResolveTip from also shelling out to a
	// real `nix eval` against a repo with no flake.
	r := mustHostRunner(t, hostRunnerConfig{repoPath: dirConsumer, appAttr: ".#", baseBranch: "main", nixSystem: "x86_64-linux", env: os.Environ()})
	tip, err := r.ResolveTip(context.Background())
	if err != nil {
		t.Fatalf("ResolveTip() error: %v", err)
	}
	if tip.Revision != wantTip {
		t.Errorf("ResolveTip().Revision = %q, want %q (origin's advanced tip)", tip.Revision, wantTip)
	}

	if headAfter := gitOutputT(t, dirConsumer, "rev-parse", "HEAD"); headAfter != consumerHeadBefore {
		t.Errorf("consumer HEAD moved: before %q, after %q", consumerHeadBefore, headAfter)
	}
	if branchAfter := gitOutputT(t, dirConsumer, "rev-parse", "--abbrev-ref", "HEAD"); branchAfter != consumerBranchBefore {
		t.Errorf("consumer branch changed: before %q, after %q", consumerBranchBefore, branchAfter)
	}
	if statusAfter := gitOutputT(t, dirConsumer, "status", "--porcelain"); statusAfter != consumerStatusBefore {
		t.Errorf("consumer working tree changed: before %q, after %q", consumerStatusBefore, statusAfter)
	}
}

// TestResolveTip_ConcurrentCallsDoNotRace drives several concurrent
// ResolveTip calls on one hostRunner against a repo whose origin/main
// has genuinely advanced since dirConsumer's clone, so each `git fetch` must
// actually move (re-lock) refs/remotes/origin/main rather than finding it
// already at the wanted tip — the scenario where unsynchronized concurrent
// fetches raced that ref lock and interleaved on the FETCH_HEAD file one
// fetch writes and the next rev-parse reads (issue #3539). Every call must
// return the same advanced tip with no error.
func TestResolveTip_ConcurrentCallsDoNotRace(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	root := t.TempDir()
	bare := filepath.Join(root, "origin.git")
	dirConsumer := filepath.Join(root, "consumer")
	dirAdvancer := filepath.Join(root, "advancer")

	gitRunT(t, "", "init", "--bare", bare)

	gitRunT(t, "", "clone", bare, dirConsumer)
	gitRunT(t, dirConsumer, "checkout", "-B", "main")
	gitRunT(t, dirConsumer, "config", "user.email", "consumer@example.com")
	gitRunT(t, dirConsumer, "config", "user.name", "Consumer")
	writeFileT(t, filepath.Join(dirConsumer, "a.txt"), "a\n")
	gitRunT(t, dirConsumer, "add", "a.txt")
	gitRunT(t, dirConsumer, "commit", "-m", "base")
	gitRunT(t, dirConsumer, "push", "-u", "origin", "main")

	// Advance origin's tip from a second clone, so dirConsumer's cached
	// origin/main ref (set up by the clone above) is now stale and every
	// fetch below must genuinely re-lock it.
	gitRunT(t, "", "clone", bare, dirAdvancer)
	gitRunT(t, dirAdvancer, "checkout", "main")
	gitRunT(t, dirAdvancer, "config", "user.email", "advancer@example.com")
	gitRunT(t, dirAdvancer, "config", "user.name", "Advancer")
	writeFileT(t, filepath.Join(dirAdvancer, "b.txt"), "b\n")
	gitRunT(t, dirAdvancer, "add", "b.txt")
	gitRunT(t, dirAdvancer, "commit", "-m", "advance")
	gitRunT(t, dirAdvancer, "push", "origin", "main")
	wantTip := strings.TrimSpace(gitOutputT(t, dirAdvancer, "rev-parse", "HEAD"))

	const n = 5
	// selfAttr left empty: this test is about the fetch half's own
	// concurrency, not ResolveTip's self half.
	r := mustHostRunner(t, hostRunnerConfig{repoPath: dirConsumer, appAttr: ".#", baseBranch: "main", nixSystem: "x86_64-linux", env: os.Environ()})
	var wg sync.WaitGroup
	results := make([]string, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tip, err := r.ResolveTip(context.Background())
			results[i] = tip.Revision
			errs[i] = err
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Errorf("call %d: ResolveTip() error: %v", i, errs[i])
		}
		if results[i] != wantTip {
			t.Errorf("call %d: ResolveTip().Revision = %q, want %q", i, results[i], wantTip)
		}
	}
}

// TestResolveTip_CancelledContext guards against the ctx-discarding bug
// (issue #3538): ResolveTip must wire ctx into the underlying
// git invocations so a caller who cancels (SIGINT/SIGTERM with no child to
// forward to) gets an error back promptly instead of the daemon hanging
// until SIGKILL. The deadline below is the regression tripwire — before the
// fix this test would hang instead of failing.
func TestResolveTip_CancelledContext(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	root := t.TempDir()
	bare := filepath.Join(root, "origin.git")
	dirConsumer := filepath.Join(root, "consumer")

	gitRunT(t, "", "init", "--bare", bare)
	gitRunT(t, "", "clone", bare, dirConsumer)
	gitRunT(t, dirConsumer, "checkout", "-B", "main")
	gitRunT(t, dirConsumer, "config", "user.email", "consumer@example.com")
	gitRunT(t, dirConsumer, "config", "user.name", "Consumer")
	writeFileT(t, filepath.Join(dirConsumer, "a.txt"), "a\n")
	gitRunT(t, dirConsumer, "add", "a.txt")
	gitRunT(t, dirConsumer, "commit", "-m", "base")
	gitRunT(t, dirConsumer, "push", "-u", "origin", "main")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// selfAttr left empty: only the fetch half is under test here.
	r := mustHostRunner(t, hostRunnerConfig{repoPath: dirConsumer, appAttr: ".#", baseBranch: "main", nixSystem: "x86_64-linux", env: os.Environ()})
	done := make(chan error, 1)
	go func() {
		_, err := r.ResolveTip(ctx)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Error("ResolveTip(cancelled ctx) error = nil, want non-nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ResolveTip(cancelled ctx) did not return promptly")
	}
}

// waitForArmed polls until every path in armed exists: each is touched only
// after the corresponding child's `trap` has run, so a signal landing
// before that finds the default disposition and kills the child outright
// (Exit -1, not 7) instead of proving delivery through the trap.
func waitForArmed(t *testing.T, armed ...string) {
	t.Helper()
	for i := 0; i < 1000; i++ {
		ready := true
		for _, path := range armed {
			if _, err := os.Stat(path); err != nil {
				ready = false
			}
		}
		if ready {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("children never started and armed their signal traps: %v", armed)
}

// childExitOnFirstSignal traps SIGTERM and exits 7 on it, so a forwarded
// stop proves the signal was delivered and drained rather than SIGKILLed.
//
// `: >"$0"` truncates the trap-armed marker file passed in as $0, which is
// how waitForArmed tells the trap is installed; the `sleep 0.05` busy loop
// exists so the shell returns from its foreground child often enough to
// run the trap.
const childExitOnFirstSignal = `trap 'exit 7' TERM; : >"$0"; while :; do sleep 0.05; done`

// childExitOnSecondSignal traps both SIGTERM and SIGINT, counts them, and
// exits 7 only on the second — the Stop-then-Abort escalation contract.
// Marker file and busy loop as above.
const childExitOnSecondSignal = `
n=0
trap 'n=$((n+1)); if [ "$n" -ge 2 ]; then exit 7; fi' TERM INT
: >"$0"
while :; do sleep 0.05; done`

// startChild starts a RunChild call on its own goroutine with the given
// Stop/Abort latch and hands back the channels its result or error lands
// on, so each test only has to write the select and its own assertions
// rather than re-declaring the same resultCh/errCh/go func plumbing.
// Cleanup of any child it starts is the caller's job (capture the *exec.Cmd
// in its own exec seam override) — nothing in the runner publishes running
// children any more for a shared helper to reach into.
func startChild(t *testing.T, r *hostRunner, stop, abort <-chan struct{}) (<-chan daemon.ChildResult, <-chan error) {
	t.Helper()
	resultCh := make(chan daemon.ChildResult, 1)
	errCh := make(chan error, 1)
	go func() {
		got, err := r.RunChild(context.Background(), daemon.ChildRequest{Slot: 0, Kind: daemon.KindDispatch, Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", Stop: stop, Abort: abort})
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- got
	}()
	return resultCh, errCh
}

// TestRunChild_StopClosedSignalsSIGTERM drives the exec seam at a script
// that traps SIGTERM and exits with a distinct code, so closing Stop proves
// the signal reached the child specifically (child.Signal(pid) never
// touches the wider process group) and that the child chose to exit on its
// own terms rather than being SIGKILLed — the production half of the "halt
// drains, never kills a Box" AC (issue #3538).
func TestRunChild_StopClosedSignalsSIGTERM(t *testing.T) {
	orig := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = orig })
	dir := t.TempDir()
	armed := filepath.Join(dir, "trap-armed")

	var mu sync.Mutex
	var cmd *exec.Cmd
	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		mu.Lock()
		defer mu.Unlock()
		cmd = exec.Command("/bin/sh", "-c", childExitOnFirstSignal, armed)
		return cmd
	}
	t.Cleanup(func() {
		mu.Lock()
		c := cmd
		mu.Unlock()
		if c != nil && c.Process != nil {
			_ = c.Process.Kill()
		}
	})

	r := mustHostRunner(t, hostRunnerConfig{repoPath: dir, appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	stop := make(chan struct{})
	resultCh, errCh := startChild(t, r, stop, nil)

	waitForArmed(t, armed)

	close(stop)

	select {
	case err := <-errCh:
		t.Fatalf("RunChild() unexpected error: %v", err)
	case got := <-resultCh:
		if got.Exit != 7 {
			t.Errorf("Exit = %d, want 7 (child trapped SIGTERM and exited on its own terms, not SIGKILLed)", got.Exit)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("closing Stop did not cause the child to exit promptly (killed instead of drained, or signal never delivered)")
	}
}

// TestRunChild_StopSignalsEveryConcurrentChild is
// TestRunChild_StopClosedSignalsSIGTERM at three concurrently running
// children: each RunChild call starts its own forwarding goroutine, so a
// design that only signalled one child (or shared state that a second
// RunChild call clobbered) would leave the other two abandoned rather than
// drained.
func TestRunChild_StopSignalsEveryConcurrentChild(t *testing.T) {
	orig := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = orig })
	dir := t.TempDir()

	const nChildren = 3
	armed := make([]string, nChildren)
	for i := range armed {
		armed[i] = filepath.Join(dir, fmt.Sprintf("trap-armed-%d", i))
	}

	var mu sync.Mutex
	var cmds []*exec.Cmd
	var callN int
	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		mu.Lock()
		defer mu.Unlock()
		n := callN
		callN++
		c := exec.Command("/bin/sh", "-c", childExitOnFirstSignal, armed[n])
		cmds = append(cmds, c)
		return c
	}
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range cmds {
			if c.Process != nil {
				_ = c.Process.Kill()
			}
		}
	})

	r := mustHostRunner(t, hostRunnerConfig{repoPath: dir, appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	stop := make(chan struct{})
	resultCh := make(chan daemon.ChildResult, nChildren)
	errCh := make(chan error, nChildren)
	for slot := 0; slot < nChildren; slot++ {
		go func(slot int) {
			got, err := r.RunChild(context.Background(), daemon.ChildRequest{Slot: slot, Kind: daemon.KindDispatch, Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", Stop: stop})
			if err != nil {
				errCh <- err
				return
			}
			resultCh <- got
		}(slot)
	}

	waitForArmed(t, armed...)

	close(stop)

	for i := 0; i < nChildren; i++ {
		select {
		case err := <-errCh:
			t.Fatalf("RunChild() unexpected error: %v", err)
		case got := <-resultCh:
			if got.Exit != 7 {
				t.Errorf("Exit = %d, want 7 (child trapped SIGTERM and exited on its own terms, not SIGKILLed)", got.Exit)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("closing Stop did not cause every child to exit promptly (some abandoned, not drained)")
		}
	}
}

// TestRunChild_ChildStartedAfterStopClosedIsSignalledAtOnce covers a child
// started after Stop has already closed: RunChild's forwarding goroutine
// selects on an already-closed channel, which fires immediately, so the
// child is signalled at once rather than running its Box to completion
// unsignalled.
func TestRunChild_ChildStartedAfterStopClosedIsSignalledAtOnce(t *testing.T) {
	orig := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = orig })
	dir := t.TempDir()
	armed := filepath.Join(dir, "trap-armed")

	var mu sync.Mutex
	var cmd *exec.Cmd
	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		mu.Lock()
		defer mu.Unlock()
		cmd = exec.Command("/bin/sh", "-c", childExitOnFirstSignal, armed)
		return cmd
	}
	t.Cleanup(func() {
		mu.Lock()
		c := cmd
		mu.Unlock()
		if c != nil && c.Process != nil {
			_ = c.Process.Kill()
		}
	})

	r := mustHostRunner(t, hostRunnerConfig{repoPath: dir, appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})

	stop := make(chan struct{})
	close(stop) // closed before RunChild is even called — the extreme end of the race window

	resultCh, errCh := startChild(t, r, stop, nil)

	select {
	case err := <-errCh:
		t.Fatalf("RunChild() unexpected error: %v", err)
	case got := <-resultCh:
		// Not asserted as exactly 7: delivery fires the instant RunChild
		// starts forwarding, which can outrace the shell's own `trap`
		// install (interpreter startup costs real OS time; the send does
		// not), so the process may end via SIGTERM's default action
		// (Exit -1) rather than the handler (Exit 7). Either way the busy
		// loop never exits unsignalled, so any nonzero exit proves the
		// signal reached this child rather than it being abandoned.
		if got.Exit == 0 {
			t.Errorf("Exit = %d, want nonzero (busy loop never exits unsignalled)", got.Exit)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("child started after Stop closed was never signalled (abandoned, not drained)")
	}
}

// TestRunChild_StopThenAbortDeliversDistinctKinds asserts closing Stop then
// Abort delivers two distinct signals to a running child — SIGTERM then
// SIGINT — end to end with no seam override: the child traps both, counts,
// and only exits on the second, the same first-drains/second-aborts
// contract the child launcher enforces on itself (issue #3521), mirrored
// here on the daemon's forwarding side.
func TestRunChild_StopThenAbortDeliversDistinctKinds(t *testing.T) {
	orig := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = orig })
	dir := t.TempDir()
	armed := filepath.Join(dir, "trap-armed")

	var mu sync.Mutex
	var cmd *exec.Cmd
	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		mu.Lock()
		defer mu.Unlock()
		cmd = exec.Command("/bin/sh", "-c", childExitOnSecondSignal, armed)
		return cmd
	}
	t.Cleanup(func() {
		mu.Lock()
		c := cmd
		mu.Unlock()
		if c != nil && c.Process != nil {
			_ = c.Process.Kill()
		}
	})

	r := mustHostRunner(t, hostRunnerConfig{repoPath: dir, appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	stop := make(chan struct{})
	abort := make(chan struct{})
	resultCh, errCh := startChild(t, r, stop, abort)

	waitForArmed(t, armed)

	close(stop)

	// The child must survive Stop alone: give it a beat before escalating,
	// and confirm it hasn't already exited.
	select {
	case got := <-resultCh:
		t.Fatalf("child exited after Stop alone (Exit=%d), want it to survive SIGTERM and only exit once Abort delivers SIGINT", got.Exit)
	case err := <-errCh:
		t.Fatalf("RunChild() unexpected error: %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	close(abort)

	select {
	case err := <-errCh:
		t.Fatalf("RunChild() unexpected error: %v", err)
	case got := <-resultCh:
		if got.Exit != 7 {
			t.Errorf("Exit = %d, want 7 (child observed SIGTERM then SIGINT and exited on the second)", got.Exit)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("closing Abort did not cause the child to exit promptly")
	}
}

// TestRunChild_ChildStartedAfterBothLatchesClosedSeesBothKinds is
// TestRunChild_ChildStartedAfterStopClosedIsSignalledAtOnce with Stop and
// Abort both already closed before RunChild is even called: the forwarding
// goroutine must still send two distinct kinds, in order, not one signal
// twice. It asserts on the *sequence* through the runnerSignal seam,
// because OS-level delivery can't show it: a child born after both closes
// has installed no handler yet, so it can die on the first signal's default
// disposition before the second is even observable from outside. The child
// here is the cheapest one that still drives RunChild's real forwarding
// path (sleep, not a trap script).
func TestRunChild_ChildStartedAfterBothLatchesClosedSeesBothKinds(t *testing.T) {
	origExec := runnerExecCommand
	origSignal := runnerSignal
	t.Cleanup(func() { runnerExecCommand = origExec; runnerSignal = origSignal })
	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", "sleep 0.3")
	}

	var mu sync.Mutex
	var sent []os.Signal
	runnerSignal = func(p *os.Process, sig os.Signal) error {
		mu.Lock()
		sent = append(sent, sig)
		mu.Unlock()
		return nil
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})

	stop := make(chan struct{})
	abort := make(chan struct{})
	close(stop)
	close(abort)

	resultCh, errCh := startChild(t, r, stop, abort)

	select {
	case err := <-errCh:
		t.Fatalf("RunChild() unexpected error: %v", err)
	case got := <-resultCh:
		if got.Exit != 0 {
			t.Errorf("Exit = %d, want 0 (unsignalled sleep 0.3 exits cleanly; runnerSignal is stubbed here, not the real kernel call)", got.Exit)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunChild did not return promptly")
	}

	mu.Lock()
	got := append([]os.Signal(nil), sent...)
	mu.Unlock()
	want := []os.Signal{syscall.SIGTERM, syscall.SIGINT}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("signals sent = %v, want %v (SIGTERM then SIGINT, both latches already closed before start)", got, want)
	}
}

// TestRunChild_ForwardingStopsAfterChildExits pins the done-channel
// contract: once RunChild has returned for a child that exited on its own,
// its forwarding goroutine must already be torn down, so a Stop that closes
// afterward sends nothing. This is new coverage the per-goroutine design
// needs that the old shared children map never required — there was no
// stale map entry to leak a signal through in the first place.
func TestRunChild_ForwardingStopsAfterChildExits(t *testing.T) {
	origExec := runnerExecCommand
	origSignal := runnerSignal
	t.Cleanup(func() { runnerExecCommand = origExec; runnerSignal = origSignal })
	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", "exit 0")
	}

	var mu sync.Mutex
	var sent []os.Signal
	runnerSignal = func(p *os.Process, sig os.Signal) error {
		mu.Lock()
		sent = append(sent, sig)
		mu.Unlock()
		return nil
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})

	stop := make(chan struct{})
	got, err := r.RunChild(context.Background(), daemon.ChildRequest{Slot: 0, Kind: daemon.KindDispatch, Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", Stop: stop})
	if err != nil {
		t.Fatalf("RunChild() unexpected error: %v", err)
	}
	if got.Exit != 0 {
		t.Fatalf("Exit = %d, want 0", got.Exit)
	}

	close(stop)
	// Give a stale goroutine (if the teardown were missing) a beat to fire.
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	n := len(sent)
	mu.Unlock()
	if n != 0 {
		t.Errorf("signals sent after RunChild returned = %d, want 0 (the forwarding goroutine must not outlive its child)", n)
	}
}

// TestRunChild_NilStopAbortRunsNormally asserts a ChildRequest carrying no
// latch at all — the terminal/one-off shape, e.g. a caller that never wires
// Stop/Abort — runs and exits normally rather than the forwarding goroutine
// panicking or blocking on a nil channel forever (a select on nil simply
// never fires).
func TestRunChild_NilStopAbortRunsNormally(t *testing.T) {
	orig := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = orig })
	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", "exit 0")
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	got, err := r.RunChild(context.Background(), daemon.ChildRequest{Slot: 0, Kind: daemon.KindDispatch, Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"})
	if err != nil {
		t.Fatalf("RunChild() unexpected error: %v", err)
	}
	if got.Exit != 0 {
		t.Errorf("Exit = %d, want 0", got.Exit)
	}
}

// TestResolveTip_SelfPathHappyPath points the eval seam at a scripted shell
// command instead of nix, and asserts ResolveTip's self half comes back with
// the trimmed stdout and no error. It needs a real (if minimal) git remote
// so the fetch half ResolveTip runs first actually succeeds.
func TestResolveTip_SelfPathHappyPath(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	orig := runnerEvalCommand
	t.Cleanup(func() { runnerEvalCommand = orig })
	runnerEvalCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", "-c", `printf '/nix/store/abc-daemon\n'`)
	}

	dirConsumer := bareOriginConsumerT(t)
	r := mustHostRunner(t, hostRunnerConfig{repoPath: dirConsumer, appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	tip, err := r.ResolveTip(context.Background())
	if err != nil {
		t.Fatalf("ResolveTip() unexpected error: %v", err)
	}
	if want := "/nix/store/abc-daemon"; tip.SelfPath != want {
		t.Errorf("ResolveTip().SelfPath = %q, want %q", tip.SelfPath, want)
	}
}

// TestResolveTip_SelfPathEvalFailureCarriesStderr asserts a failing
// evaluation's error folds in the captured stderr rather than reducing to a
// bare "exit status 1" — the same shape fetchRevision's git fetch error
// takes — and that ResolveTip wraps it as a *daemon.SelfEvalError, the
// class the pool's backoff reason depends on.
func TestResolveTip_SelfPathEvalFailureCarriesStderr(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	orig := runnerEvalCommand
	t.Cleanup(func() { runnerEvalCommand = orig })
	runnerEvalCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", "-c", `printf 'error: attribute missing\n' >&2; exit 1`)
	}

	dirConsumer := bareOriginConsumerT(t)
	r := mustHostRunner(t, hostRunnerConfig{repoPath: dirConsumer, appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	_, err := r.ResolveTip(context.Background())
	var se *daemon.SelfEvalError
	if !errors.As(err, &se) {
		t.Fatalf("ResolveTip() error = %v, want a *daemon.SelfEvalError", err)
	}
	if !strings.Contains(se.Error(), "attribute missing") {
		t.Errorf("SelfEvalError.Error() = %q, want it to contain the captured stderr", se.Error())
	}
}

// TestResolveTip_EmptySelfAttrSkipsEval asserts that hostRunnerConfig.selfAttr
// == "" (the self check off, per main.go's SPINDRIFT_DAEMON_PROGRAM handling)
// makes ResolveTip skip the eval half entirely: runnerEvalCommand must never
// be invoked, and the returned Tip.SelfPath is empty.
func TestResolveTip_EmptySelfAttrSkipsEval(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	orig := runnerEvalCommand
	t.Cleanup(func() { runnerEvalCommand = orig })
	runnerEvalCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		t.Fatal("runnerEvalCommand invoked, want no evaluation with selfAttr == \"\"")
		return nil
	}

	dirConsumer := bareOriginConsumerT(t)
	r := mustHostRunner(t, hostRunnerConfig{repoPath: dirConsumer, appAttr: ".#", baseBranch: "main", nixSystem: "x86_64-linux", env: os.Environ()})
	tip, err := r.ResolveTip(context.Background())
	if err != nil {
		t.Fatalf("ResolveTip() unexpected error: %v", err)
	}
	if tip.SelfPath != "" {
		t.Errorf("ResolveTip().SelfPath = %q, want empty", tip.SelfPath)
	}
}

// scriptedFetchSeamT builds a runnerFetchCommand replacement that never
// touches a real git remote: the "fetch" subcommand runs onFetch (a hook a
// test can use to block the leader, or to fail), and the "rev-parse"
// subcommand always echoes whatever revision() currently returns, so a test
// can move the "remote" tip between calls by mutating the string a closure
// reads. Recognising the subcommand by args[2] mirrors fetchRevision's own
// call shape: runnerFetchCommand(ctx, "git", "-C", repoPath, sub, ...).
func scriptedFetchSeamT(t *testing.T, revision func() string, onFetch func()) func(ctx context.Context, name string, args ...string) *exec.Cmd {
	t.Helper()
	return func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if len(args) < 3 {
			// t.Errorf, not t.Fatalf: this closure runs as runnerFetchCommand
			// on whatever goroutine calls ResolveTip, which in the
			// coalescing tests is a caller goroutine, not the test
			// goroutine — Fatalf (FailNow) is only safe from the latter.
			t.Errorf("runnerFetchCommand args = %v, want at least 3", args)
			return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 1")
		}
		if args[2] == "fetch" {
			if onFetch != nil {
				onFetch()
			}
			return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 0")
		}
		return exec.CommandContext(ctx, "/bin/sh", "-c", fmt.Sprintf("printf '%s\\n'", revision()))
	}
}

// TestResolveTip_ConcurrentCallersCoalesceIntoOneFetchAndOneEval drives three
// concurrent ResolveTip callers against a leader deliberately held inside
// the fetch seam until both other callers have joined its flight (via
// runnerFlightJoined — no sleeps as synchronisation), and asserts the whole
// group costs exactly one `git fetch` and one `nix eval` despite three
// callers, with every caller receiving the identical Tip.
func TestResolveTip_ConcurrentCallersCoalesceIntoOneFetchAndOneEval(t *testing.T) {
	origFetch, origEval, origJoined := runnerFetchCommand, runnerEvalCommand, runnerFlightJoined
	t.Cleanup(func() {
		runnerFetchCommand, runnerEvalCommand, runnerFlightJoined = origFetch, origEval, origJoined
	})

	var fetchCalls, evalCalls int32
	gate := make(chan struct{})
	leaderBlocked := make(chan struct{})
	joins := make(chan struct{}, 2)

	runnerFetchCommand = scriptedFetchSeamT(t, func() string { return "deadbeef" }, func() {
		atomic.AddInt32(&fetchCalls, 1)
		close(leaderBlocked)
		<-gate
	})
	runnerEvalCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		atomic.AddInt32(&evalCalls, 1)
		return exec.CommandContext(ctx, "/bin/sh", "-c", `printf '/nix/store/abc-daemon\n'`)
	}
	runnerFlightJoined = func() { joins <- struct{}{} }

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})

	const n = 3
	tips := make([]daemon.Tip, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tips[i], errs[i] = r.ResolveTip(context.Background())
		}(i)
	}

	<-leaderBlocked
	<-joins
	<-joins
	close(gate)
	wg.Wait()

	if got := atomic.LoadInt32(&fetchCalls); got != 1 {
		t.Errorf("fetch calls = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&evalCalls); got != 1 {
		t.Errorf("eval calls = %d, want 1", got)
	}
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Errorf("caller %d: ResolveTip() error: %v", i, errs[i])
		}
		if tips[i] != (daemon.Tip{Revision: "deadbeef", SelfPath: "/nix/store/abc-daemon", Moved: false}) {
			t.Errorf("caller %d: ResolveTip() = %+v, want the shared leader Tip", i, tips[i])
		}
	}
}

// TestResolveTip_NoTTLSequentialCallsEachFetch asserts two sequential
// ResolveTip calls — the second arriving after the first's flight has
// already finished and cleared — each trigger their own `git fetch`: there
// is no TTL caching a completed resolution.
func TestResolveTip_NoTTLSequentialCallsEachFetch(t *testing.T) {
	orig := runnerFetchCommand
	t.Cleanup(func() { runnerFetchCommand = orig })

	var fetchCalls int32
	runnerFetchCommand = scriptedFetchSeamT(t, func() string { return "deadbeef" }, func() {
		atomic.AddInt32(&fetchCalls, 1)
	})

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", nixSystem: "x86_64-linux", env: os.Environ()})
	if _, err := r.ResolveTip(context.Background()); err != nil {
		t.Fatalf("first ResolveTip() error: %v", err)
	}
	if _, err := r.ResolveTip(context.Background()); err != nil {
		t.Fatalf("second ResolveTip() error: %v", err)
	}
	if got := atomic.LoadInt32(&fetchCalls); got != 2 {
		t.Errorf("fetch calls = %d, want 2 (no TTL — each call starts its own flight)", got)
	}
}

// TestResolveTip_SelfPathMemoisedPerRevision asserts two resolutions at the
// same revision cost one `nix eval` (the memo serves the second), and a
// resolution at a new revision evaluates again. The
// fetch count is not what this test is about.
func TestResolveTip_SelfPathMemoisedPerRevision(t *testing.T) {
	origFetch, origEval := runnerFetchCommand, runnerEvalCommand
	t.Cleanup(func() { runnerFetchCommand, runnerEvalCommand = origFetch, origEval })

	revision := "deadbeef1"
	runnerFetchCommand = scriptedFetchSeamT(t, func() string { return revision }, nil)
	var evalCalls int32
	runnerEvalCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		atomic.AddInt32(&evalCalls, 1)
		return exec.CommandContext(ctx, "/bin/sh", "-c", `printf '/nix/store/abc-daemon\n'`)
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})

	if _, err := r.ResolveTip(context.Background()); err != nil {
		t.Fatalf("first ResolveTip() error: %v", err)
	}
	if _, err := r.ResolveTip(context.Background()); err != nil {
		t.Fatalf("second ResolveTip() (same revision) error: %v", err)
	}
	if got := atomic.LoadInt32(&evalCalls); got != 1 {
		t.Errorf("eval calls after two same-revision resolutions = %d, want 1", got)
	}

	revision = "deadbeef2"
	if _, err := r.ResolveTip(context.Background()); err != nil {
		t.Fatalf("third ResolveTip() (new revision) error: %v", err)
	}
	if got := atomic.LoadInt32(&evalCalls); got != 2 {
		t.Errorf("eval calls after a new-revision resolution = %d, want 2", got)
	}
}

// TestResolveTip_Moved walks Tip.Moved through every case it must cover:
// false on the first resolution ever, false while the revision
// is unchanged, true on the resolution that first reports a new one, and
// false again on the next one at that same new revision — then confirms
// every caller sharing one flight sees the same value.
func TestResolveTip_Moved(t *testing.T) {
	origFetch, origJoined := runnerFetchCommand, runnerFlightJoined
	t.Cleanup(func() { runnerFetchCommand, runnerFlightJoined = origFetch, origJoined })

	revision := "rev1"
	runnerFetchCommand = scriptedFetchSeamT(t, func() string { return revision }, nil)

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", nixSystem: "x86_64-linux", env: os.Environ()})

	tip, err := r.ResolveTip(context.Background())
	if err != nil {
		t.Fatalf("first ResolveTip() error: %v", err)
	}
	if tip.Moved {
		t.Error("first resolution ever: Moved = true, want false")
	}

	tip, err = r.ResolveTip(context.Background())
	if err != nil {
		t.Fatalf("second ResolveTip() error: %v", err)
	}
	if tip.Moved {
		t.Error("resolution at the unchanged revision: Moved = true, want false")
	}

	revision = "rev2"
	tip, err = r.ResolveTip(context.Background())
	if err != nil {
		t.Fatalf("third ResolveTip() error: %v", err)
	}
	if !tip.Moved {
		t.Error("resolution at a new revision: Moved = false, want true")
	}

	tip, err = r.ResolveTip(context.Background())
	if err != nil {
		t.Fatalf("fourth ResolveTip() error: %v", err)
	}
	if tip.Moved {
		t.Error("resolution at the same new revision: Moved = true, want false")
	}

	// A final shared-flight group at yet another new revision: every joiner
	// must see the same Moved the leader computed.
	revision = "rev3"
	gate := make(chan struct{})
	leaderBlocked := make(chan struct{})
	joins := make(chan struct{}, 2)
	runnerFetchCommand = scriptedFetchSeamT(t, func() string { return revision }, func() {
		close(leaderBlocked)
		<-gate
	})
	runnerFlightJoined = func() { joins <- struct{}{} }

	const n = 3
	tips := make([]daemon.Tip, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tips[i], errs[i] = r.ResolveTip(context.Background())
		}(i)
	}
	<-leaderBlocked
	<-joins
	<-joins
	close(gate)
	wg.Wait()

	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Errorf("caller %d: ResolveTip() error: %v", i, errs[i])
		}
		if !tips[i].Moved {
			t.Errorf("caller %d: Moved = false, want true (shared flight at the new revision)", i)
		}
	}
}

// TestResolveTip_FetchErrorReachesEveryJoiner asserts a failing `git fetch`
// reaches every caller sharing that leader's flight with the identical
// error — the same wrapping fetchRevision produces today — rather than
// only the leader seeing it.
func TestResolveTip_FetchErrorReachesEveryJoiner(t *testing.T) {
	origFetch, origJoined := runnerFetchCommand, runnerFlightJoined
	t.Cleanup(func() { runnerFetchCommand, runnerFlightJoined = origFetch, origJoined })

	gate := make(chan struct{})
	leaderBlocked := make(chan struct{})
	joins := make(chan struct{}, 2)
	runnerFetchCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if len(args) < 3 {
			t.Fatalf("runnerFetchCommand args = %v, want at least 3", args)
		}
		if args[2] != "fetch" {
			t.Fatalf("runnerFetchCommand subcommand = %q, want \"fetch\" (rev-parse should never run after a fetch failure)", args[2])
		}
		close(leaderBlocked)
		<-gate
		return exec.CommandContext(ctx, "/bin/sh", "-c", `printf 'fatal: no such remote origin\n' >&2; exit 1`)
	}
	runnerFlightJoined = func() { joins <- struct{}{} }

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", nixSystem: "x86_64-linux", env: os.Environ()})

	const n = 3
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = r.ResolveTip(context.Background())
		}(i)
	}
	<-leaderBlocked
	<-joins
	<-joins
	close(gate)
	wg.Wait()

	for i := 0; i < n; i++ {
		if errs[i] == nil {
			t.Fatalf("caller %d: ResolveTip() error = nil, want the shared fetch failure", i)
		}
	}
	want := errs[0].Error()
	if !strings.Contains(want, "git fetch origin main") || !strings.Contains(want, "no such remote origin") {
		t.Errorf("ResolveTip() error = %q, want it to name the fetch and carry the stderr", want)
	}
	for i := 1; i < n; i++ {
		if errs[i].Error() != want {
			t.Errorf("caller %d: error = %q, want the same text as caller 0 (%q)", i, errs[i].Error(), want)
		}
	}
}

// TestResolveTip_LeaderPanicReleasesJoiner asserts a panic in the leader's
// resolveTipOnce still releases every joiner instead of stranding it: the
// leader's cleanup (clear r.flight, close flight.done) runs via defer, so it
// still fires while the panic unwinds.
func TestResolveTip_LeaderPanicReleasesJoiner(t *testing.T) {
	origFetch, origJoined := runnerFetchCommand, runnerFlightJoined
	t.Cleanup(func() { runnerFetchCommand, runnerFlightJoined = origFetch, origJoined })

	gate := make(chan struct{})
	leaderBlocked := make(chan struct{})
	joins := make(chan struct{}, 1)
	runnerFetchCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		close(leaderBlocked)
		<-gate
		panic("simulated leader panic")
	}
	runnerFlightJoined = func() { joins <- struct{}{} }

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", nixSystem: "x86_64-linux", env: os.Environ()})

	leaderDone := make(chan struct{})
	var recovered any
	go func() {
		defer close(leaderDone)
		defer func() { recovered = recover() }()
		_, _ = r.ResolveTip(context.Background())
	}()
	<-leaderBlocked

	joinerDone := make(chan struct{})
	go func() {
		defer close(joinerDone)
		_, _ = r.ResolveTip(context.Background())
	}()
	<-joins
	close(gate)

	select {
	case <-joinerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("joiner did not return after the leader panicked — want it released, not hung")
	}
	select {
	case <-leaderDone:
	case <-time.After(5 * time.Second):
		t.Fatal("leader goroutine did not finish unwinding its panic")
	}
	if recovered == nil {
		t.Fatal("leader did not panic as scripted — test setup is broken")
	}
}

// TestResolveTip_SelfEvalFailureDoesNotPoisonMemo asserts a self-eval
// failure still returns Tip{Revision: <fetched revision>} alongside a
// *daemon.SelfEvalError, and leaves the self-path memo untouched: the next
// resolution at that same revision evaluates again rather than serving a
// path it never got.
func TestResolveTip_SelfEvalFailureDoesNotPoisonMemo(t *testing.T) {
	origFetch, origEval := runnerFetchCommand, runnerEvalCommand
	t.Cleanup(func() { runnerFetchCommand, runnerEvalCommand = origFetch, origEval })

	const revision = "deadbeef"
	runnerFetchCommand = scriptedFetchSeamT(t, func() string { return revision }, nil)
	var evalCalls int32
	runnerEvalCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if atomic.AddInt32(&evalCalls, 1) == 1 {
			return exec.CommandContext(ctx, "/bin/sh", "-c", `printf 'error: attribute missing\n' >&2; exit 1`)
		}
		return exec.CommandContext(ctx, "/bin/sh", "-c", `printf '/nix/store/abc-daemon\n'`)
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})

	tip, err := r.ResolveTip(context.Background())
	var se *daemon.SelfEvalError
	if !errors.As(err, &se) {
		t.Fatalf("first ResolveTip() error = %v, want a *daemon.SelfEvalError", err)
	}
	if tip.Revision != revision {
		t.Errorf("first ResolveTip().Revision = %q, want %q", tip.Revision, revision)
	}
	if tip.SelfPath != "" {
		t.Errorf("first ResolveTip().SelfPath = %q, want empty on a failed eval", tip.SelfPath)
	}

	tip, err = r.ResolveTip(context.Background())
	if err != nil {
		t.Fatalf("second ResolveTip() (same revision, retried eval) error: %v", err)
	}
	if want := "/nix/store/abc-daemon"; tip.SelfPath != want {
		t.Errorf("second ResolveTip().SelfPath = %q, want %q", tip.SelfPath, want)
	}
	if got := atomic.LoadInt32(&evalCalls); got != 2 {
		t.Errorf("eval calls = %d, want 2 (a failed eval must not memoise and skip the retry)", got)
	}
}

// TestRunDoctor_ZeroExit points the doctor seam at a scripted shell command
// instead of nix, and asserts a clean exit comes back as (0, nil).
func TestRunDoctor_ZeroExit(t *testing.T) {
	orig := runnerDoctorCommand
	t.Cleanup(func() { runnerDoctorCommand = orig })
	runnerDoctorCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 0")
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	exit, err := r.RunDoctor(context.Background(), "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	if err != nil {
		t.Fatalf("RunDoctor() unexpected error: %v", err)
	}
	if exit != 0 {
		t.Errorf("exit = %d, want 0", exit)
	}
}

// TestRunDoctor_EnvStripsKnobsKeepsSecrets is RunDoctor's half of
// TestRunChild_EnvStripsKnobsKeepsSecrets above: same script-dumps-to-a-file
// route, since RunDoctor sends both streams straight to os.Stderr with no
// existing capture pattern in this file.
func TestRunDoctor_EnvStripsKnobsKeepsSecrets(t *testing.T) {
	orig := runnerDoctorCommand
	t.Cleanup(func() { runnerDoctorCommand = orig })

	dumpFile := filepath.Join(t.TempDir(), "env.out")
	runnerDoctorCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", "-c", fmt.Sprintf(envDumpScript, dumpFile))
	}

	r := mustHostRunner(t, hostRunnerConfig{
		repoPath:   t.TempDir(),
		appAttr:    ".#",
		baseBranch: "main",
		selfAttr:   ".#daemon",
		nixSystem:  "x86_64-linux",
		env:        []string{"MODEL=from-shell", "PATH=/bin:/usr/bin", "GH_TOKEN=super-secret"},
		knobs:      []string{"MODEL"},
	})
	exit, err := r.RunDoctor(context.Background(), "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	if err != nil {
		t.Fatalf("RunDoctor() unexpected error: %v", err)
	}
	if exit != 0 {
		t.Fatalf("exit = %d, want 0", exit)
	}

	dumped, err := os.ReadFile(dumpFile)
	if err != nil {
		t.Fatalf("read dumped env: %v", err)
	}
	assertEnvStripsKnobsKeepsSecrets(t, string(dumped))
}

// TestRunDoctor_NonZeroExitIsNotAnError asserts doctor's required-labels-
// missing exit code (4) comes back as (4, nil): the caller, not the seam,
// classifies what the code means.
func TestRunDoctor_NonZeroExitIsNotAnError(t *testing.T) {
	orig := runnerDoctorCommand
	t.Cleanup(func() { runnerDoctorCommand = orig })
	runnerDoctorCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 4")
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	exit, err := r.RunDoctor(context.Background(), "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	if err != nil {
		t.Fatalf("RunDoctor() unexpected error: %v", err)
	}
	if exit != 4 {
		t.Errorf("exit = %d, want 4", exit)
	}
}

// TestRunDoctor_SeamFailure asserts a seam that cannot even start (a
// non-existent executable) surfaces a non-nil error rather than folding into
// the exit-code path above.
func TestRunDoctor_SeamFailure(t *testing.T) {
	orig := runnerDoctorCommand
	t.Cleanup(func() { runnerDoctorCommand = orig })
	runnerDoctorCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/no/such/executable-doctor-seam")
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	_, err := r.RunDoctor(context.Background(), "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	if err == nil {
		t.Fatal("RunDoctor() error = nil, want non-nil")
	}
}

// TestRunDoctor_ArgvIsDoctorCommand captures the argv the seam receives and
// asserts it is exactly what daemon.DoctorCommand builds: a pinned flakeref
// carrying the revision, ending in "-- doctor", with no --max-jobs or
// --max-parallel (those cap a child's dispatch wave; doctor dispatches
// nothing to cap), and no --verbose or -v — the daemon relies on doctor's
// quiet-by-default report (#3777) rather than requesting the verbose one,
// and a reflect.DeepEqual against DoctorCommand's own argv can't catch
// DoctorCommand itself growing a --verbose. Token-exact matching (not
// substring) guards against a false match inside another argument, e.g. a
// flakeref or revision.
func TestRunDoctor_ArgvIsDoctorCommand(t *testing.T) {
	orig := runnerDoctorCommand
	t.Cleanup(func() { runnerDoctorCommand = orig })

	var gotName string
	var gotArgs []string
	runnerDoctorCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		gotName = name
		gotArgs = append([]string(nil), args...)
		return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 0")
	}

	revision := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	r := mustHostRunner(t, hostRunnerConfig{repoPath: "/repo", appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	if _, err := r.RunDoctor(context.Background(), revision); err != nil {
		t.Fatalf("RunDoctor() unexpected error: %v", err)
	}

	wantCmd, err := daemon.DoctorCommand(daemon.DoctorSpec{RepoPath: "/repo", AppAttr: ".#", Revision: revision})
	if err != nil {
		t.Fatalf("daemon.DoctorCommand() unexpected error: %v", err)
	}

	got := append([]string{gotName}, gotArgs...)
	if !reflect.DeepEqual(got, wantCmd.Argv) {
		t.Errorf("argv = %v, want %v", got, wantCmd.Argv)
	}
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, revision) {
		t.Errorf("argv %v does not carry revision %q", got, revision)
	}
	if !strings.HasSuffix(joined, "-- doctor") {
		t.Errorf("argv %v does not end in %q", got, "-- doctor")
	}
	for _, tok := range got {
		switch tok {
		case "--max-jobs", "--max-parallel", "--verbose", "-v":
			t.Errorf("argv %v carries token %q, want none of --max-jobs, --max-parallel, --verbose, -v", got, tok)
		}
	}
}

// TestRunDoctor_HealthyPreflightEmitsNothing stubs the doctor seam with a
// script that exits 0 printing nothing on either stream, mirroring what
// quiet-by-default doctor (#3777) does on a healthy machine, and asserts the
// daemon's own captured stderr is empty and its stdout stays reserved for
// the event stream. RunDoctor relays both of the child's streams onto the
// daemon's stderr and adds no framing of its own, so a silent child must
// leave both of the daemon's streams silent too.
func TestRunDoctor_HealthyPreflightEmitsNothing(t *testing.T) {
	orig := runnerDoctorCommand
	t.Cleanup(func() { runnerDoctorCommand = orig })
	runnerDoctorCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 0")
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	readStderr := captureStderr(t)
	readStdout := captureStdout(t)
	exit, err := r.RunDoctor(context.Background(), "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	if err != nil {
		t.Fatalf("RunDoctor() unexpected error: %v", err)
	}
	if exit != 0 {
		t.Errorf("exit = %d, want 0", exit)
	}
	if stderr := readStderr(); len(stderr) != 0 {
		t.Errorf("stderr = %q, want empty (a healthy quiet-by-default doctor run emits nothing)", stderr)
	}
	if stdout := readStdout(); len(stdout) != 0 {
		t.Errorf("stdout = %q, want empty (stdout stays reserved for the event stream)", stdout)
	}
}

// TestRunDoctor_RefusedPreflightForwardsFindings stubs the doctor seam with
// scripts mirroring two shapes real doctor refuses in: missing required
// triage labels exit 4 with bare MISSING rows and no remedy line, while an
// unprotected base branch exits 1 with a MISSING row paired with its indented
// remedy line. Each case asserts every line the script emitted lands
// on the daemon's captured stderr — both of the child's streams are relayed
// to the same descriptor — and the returned exit is the child's own with no
// error.
func TestRunDoctor_RefusedPreflightForwardsFindings(t *testing.T) {
	tests := []struct {
		name       string
		script     string
		wantExit   int
		wantStderr []string
	}{
		{
			// doctor's checkLabelSet calls Reporter.Finding directly, so a
			// missing label's row carries no remedy line; the stderr line is
			// ErrRequiredLabelsMissing's own text, which doctorReport prints
			// there. Only the four work-tier labels are Required, hence these
			// two names.
			name: "required labels missing",
			script: `printf '%s\n%s\n' 'MISSING: label "ready-for-agent" missing' 'MISSING: label "agent-in-progress" missing'
printf '%s\n' 'required triage label(s) missing or declined: ready-for-agent, agent-in-progress missing — create them in the repository' >&2
exit 4`,
			wantExit: 4,
			wantStderr: []string{
				`MISSING: label "ready-for-agent" missing`,
				`MISSING: label "agent-in-progress" missing`,
				"required triage label(s) missing or declined: ready-for-agent, agent-in-progress missing — create them in the repository",
			},
		},
		{
			// Reporter.Results is the only caller that pairs a row with a
			// remedy line, and branch-protection is a Required row whose
			// Remedy differs from its probe error, so both lines print. That
			// error wraps no sentinel, so it lands on doctorExitCodeFor's
			// default arm, exit 1 — the connectivity rows wrap ErrConnectivity
			// and exit 3 instead. doctorReport prints it to stderr verbatim.
			name: "unprotected base branch",
			script: `printf '%s\n%s\n' 'MISSING: branch-protection: base branch "main" is not protected' '  remedy: protect main: block direct pushes and require CI status checks'
printf '%s\n' 'base branch "main" is not protected' >&2
exit 1`,
			wantExit: 1,
			wantStderr: []string{
				`MISSING: branch-protection: base branch "main" is not protected`,
				"  remedy: protect main: block direct pushes and require CI status checks",
				`base branch "main" is not protected`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orig := runnerDoctorCommand
			t.Cleanup(func() { runnerDoctorCommand = orig })
			runnerDoctorCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
				return exec.CommandContext(ctx, "/bin/sh", "-c", tt.script)
			}

			r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
			readStderr := captureStderr(t)
			exit, err := r.RunDoctor(context.Background(), "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
			if err != nil {
				t.Fatalf("RunDoctor() unexpected error: %v", err)
			}
			if exit != tt.wantExit {
				t.Errorf("exit = %d, want %d", exit, tt.wantExit)
			}
			stderr := string(readStderr())
			for _, want := range tt.wantStderr {
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr = %q, want it to contain %q", stderr, want)
				}
			}
		})
	}
}

// TestRunDoctor_CancelledContextTearsDownChild asserts a cancelled ctx tears
// the child down rather than waiting it out, mirroring
// TestResolveTip_CancelledContext's shape for a different seam.
func TestRunDoctor_CancelledContextTearsDownChild(t *testing.T) {
	orig := runnerDoctorCommand
	t.Cleanup(func() { runnerDoctorCommand = orig })
	runnerDoctorCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", "-c", "sleep 30")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	done := make(chan error, 1)
	go func() {
		_, err := r.RunDoctor(ctx, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Error("RunDoctor(cancelled ctx) error = nil, want non-nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunDoctor(cancelled ctx) did not return promptly")
	}
}

// TestRunDoctor_UnparseableSpecSkipsSeam asserts an empty revision surfaces
// DoctorCommand's own error without ever invoking the seam.
func TestRunDoctor_UnparseableSpecSkipsSeam(t *testing.T) {
	orig := runnerDoctorCommand
	t.Cleanup(func() { runnerDoctorCommand = orig })
	called := false
	runnerDoctorCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		called = true
		return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 0")
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	_, err := r.RunDoctor(context.Background(), "")
	if err == nil {
		t.Fatal("RunDoctor(empty revision) error = nil, want non-nil")
	}
	if called {
		t.Error("RunDoctor(empty revision) invoked the seam, want it skipped")
	}
}

// TestRunDoctor_SignalKilledIsSeamFailure asserts a doctor ended by a signal
// (Ctrl-C reaching it, or ctx tearing it down) is a seam error, not the
// pseudo-exit-code -1: there is no doctor verdict to classify, and a caller
// handed (-1, nil) would report "doctor exit -1" at a Ctrl-C.
func TestRunDoctor_SignalKilledIsSeamFailure(t *testing.T) {
	orig := runnerDoctorCommand
	t.Cleanup(func() { runnerDoctorCommand = orig })
	runnerDoctorCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", "-c", "kill -TERM $$")
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	exit, err := r.RunDoctor(context.Background(), "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	if err == nil {
		t.Fatalf("RunDoctor() = (%d, nil), want a non-nil error for a signal-killed child", exit)
	}
	if !strings.Contains(err.Error(), "signal") {
		t.Errorf("RunDoctor() error = %q, want it to name the signal that ended the child", err.Error())
	}
}

// TestNewHostRunner_RejectsNilEnv pins the fix for the silent-empty-env bug
// of #3692: a nil cfg.env — an uncaptured environment, not "empty on
// purpose" — is rejected at construction rather than reaching childEnv,
// which can't tell the two apart (both come out non-nil).
func TestNewHostRunner_RejectsNilEnv(t *testing.T) {
	_, err := newHostRunner(hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: nil})
	if err == nil {
		t.Fatal("newHostRunner() = nil error, want a rejection of the nil env")
	}
	if !strings.Contains(err.Error(), "hostRunnerConfig.env is nil") {
		t.Errorf("newHostRunner() error = %q, want it to name the missing input", err.Error())
	}
}

// TestNewHostRunner_AcceptsEmptyEnv asserts the other half of the nil-vs-
// empty distinction: an explicitly empty (non-nil) env is unusual but
// valid, and must not be rejected alongside the nil case above.
func TestNewHostRunner_AcceptsEmptyEnv(t *testing.T) {
	r, err := newHostRunner(hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: []string{}})
	if err != nil {
		t.Fatalf("newHostRunner() unexpected error: %v", err)
	}
	if r == nil {
		t.Fatal("newHostRunner() = nil runner, nil error")
	}
}
