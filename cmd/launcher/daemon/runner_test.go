package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"spindrift.dev/launcher/internal/daemon"
	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/inputdoc"
	"spindrift.dev/launcher/internal/report"
)

// mustHostRunner is newHostRunner for tests that don't assert on its error.
func mustHostRunner(t *testing.T, cfg hostRunnerConfig) *hostRunner {
	t.Helper()
	r, err := newHostRunner(cfg)
	if err != nil {
		t.Fatalf("newHostRunner() unexpected error: %v", err)
	}
	return r
}

// TestStartChild_ExitCode points the exec seam at a scripted shell command
// instead of nix (this repo's tests never shell out to nix), and asserts a
// non-zero exit comes back as ChildResult.Exit with no error.
func TestStartChild_ExitCode(t *testing.T) {
	orig := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = orig })

	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", "exit 2")
	}

	// Runners pass a real env, never nil (TestNewHostRunner_RejectsNilEnv);
	// the os.Environ() snapshot is load-bearing for children that shell out.
	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	got, err := startAndWait(context.Background(), r, daemon.ChildRequest{Slot: 0, Kind: daemon.KindOf(dispatchkind.Work), Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"})
	if err != nil {
		t.Fatalf("startAndWait() unexpected error: %v", err)
	}
	if got.Exit != 2 {
		t.Errorf("Exit = %d, want 2", got.Exit)
	}
}

// captureStream redirects *target (os.Stdout or os.Stderr) to a pipe and
// returns a func restoring it and returning what was written. Not for
// t.Parallel tests, and never twice on one global: the inner restore would
// hand back the outer capture's writer.
func captureStream(t *testing.T, target **os.File) func() []byte {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("captureStream: pipe: %v", err)
	}
	orig := *target
	*target = w
	// Drained concurrently: StartChild wires a child straight to this
	// descriptor, and TestStartChild_StdoutRelayedUnchanged writes 70 KiB, past
	// a pipe's capacity; unread, the child blocks forever and wedges StartChild.
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

// captureStderr redirects os.Stderr for the caller. StartChild relays a
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

// TestStartChild_OnRecordDeliversBoxAndSettledInOrder asserts a "box" and a
// "settled" record written to fd 3 reach OnRecord in order, intact: a slot's
// only live view of its child (issues #3545, #3627).
func TestStartChild_OnRecordDeliversBoxAndSettledInOrder(t *testing.T) {
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
		Kind:     daemon.KindOf(dispatchkind.Work),
		Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		OnRecord: func(rec daemon.Record) {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, rec)
		},
	}
	if _, err := startAndWait(context.Background(), r, req); err != nil {
		t.Fatalf("startAndWait() unexpected error: %v", err)
	}
	want := []daemon.Record{
		{Event: "box", Key: dispatchkey.Issue("101"), Phase: "initial"},
		{Event: "settled", Key: dispatchkey.Issue("101"), State: "merged"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("OnRecord calls = %+v, want %+v", got, want)
	}
}

// TestStartChild_UnknownEventIgnored asserts an unrecognised event is
// silently ignored, for forward compatibility.
func TestStartChild_UnknownEventIgnored(t *testing.T) {
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
		Slot: 0, Kind: daemon.KindOf(dispatchkind.Work), Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		OnRecord: func(rec daemon.Record) { got = append(got, rec) },
	}
	result, err := startAndWait(context.Background(), r, req)
	if err != nil {
		t.Fatalf("startAndWait() unexpected error: %v", err)
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

// TestStartChild_MalformedLinesReportOnceThenValidArrives asserts two
// malformed lines draw one diagnostic, so a child spamming bad lines cannot
// spam the daemon's log, and a valid record after them still arrives.
func TestStartChild_MalformedLinesReportOnceThenValidArrives(t *testing.T) {
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
		Slot: 0, Kind: daemon.KindOf(dispatchkind.Work), Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		OnRecord: func(rec daemon.Record) { got = append(got, rec) },
	}
	if _, err := startAndWait(context.Background(), r, req); err != nil {
		t.Fatalf("startAndWait() unexpected error: %v", err)
	}
	want := []daemon.Record{{Event: "box", Key: dispatchkey.Issue("7"), Phase: "fix-pass-1"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("OnRecord calls = %+v, want %+v", got, want)
	}
	stderr := string(readStderr())
	if n := strings.Count(stderr, "daemon: read child report:"); n != 1 {
		t.Errorf("stderr diagnostic count = %d, want exactly 1 (got stderr: %q)", n, stderr)
	}
}

// TestStartChild_KindMismatchReportedPastMalformedLine drives a butler child
// that writes a malformed line, then two issue-keyed records it may never
// send: the malformed line's once-only diagnostic must not swallow the
// mismatch, which gets exactly one diagnostic of its own, and no mismatched
// record reaches OnRecord.
func TestStartChild_KindMismatchReportedPastMalformedLine(t *testing.T) {
	orig := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = orig })

	script := `printf '%s\n%s\n%s\n%s\n' 'not json' '{"event":"box","issue":"7"}' '{"event":"box","issue":"8"}' '{"event":"box","chore":"bugs","phase":"initial"}' >&3; exit 0`
	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", script)
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	readStderr := captureStderr(t)
	var got []daemon.Record
	req := daemon.ChildRequest{
		Slot: 0, Kind: daemon.KindOf(dispatchkind.Butler), Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		OnRecord: func(rec daemon.Record) { got = append(got, rec) },
	}
	if _, err := startAndWait(context.Background(), r, req); err != nil {
		t.Fatalf("startAndWait() unexpected error: %v", err)
	}
	want := []daemon.Record{{Event: "box", Key: dispatchkey.Chore("bugs"), Phase: "initial"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("OnRecord calls = %+v, want %+v", got, want)
	}
	stderr := string(readStderr())
	if n := strings.Count(stderr, "daemon: read child report:"); n != 2 {
		t.Errorf("stderr diagnostic count = %d, want exactly 2 (got stderr: %q)", n, stderr)
	}
	if n := strings.Count(stderr, "issue-keyed record"); n != 1 {
		t.Errorf("mismatch diagnostic count = %d, want exactly 1 (got stderr: %q)", n, stderr)
	}
}

// TestStartChild_InvalidIssueRecordCarriesSingleDaemonPrefix pins readReports'
// "daemon: read child report: " as the line's only "daemon: ": ParseRecord's
// own error text must stay unprefixed.
func TestStartChild_InvalidIssueRecordCarriesSingleDaemonPrefix(t *testing.T) {
	orig := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = orig })

	script := `printf '%s\n' '{"event":"box","issue":"abc"}' >&3; exit 0`
	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", script)
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	readStderr := captureStderr(t)
	req := daemon.ChildRequest{Slot: 0, Kind: daemon.KindOf(dispatchkind.Work), Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"}
	if _, err := startAndWait(context.Background(), r, req); err != nil {
		t.Fatalf("startAndWait() unexpected error: %v", err)
	}
	want := `daemon: read child report: record: invalid issue "abc"` + "\n"
	if stderr := string(readStderr()); stderr != want {
		t.Errorf("stderr = %q, want %q", stderr, want)
	}
}

// TestStartChild_ChildCommandErrorCarriesNoDaemonPrefix pins that a
// ChildCommand/appFlakeref build error returned by StartChild carries no
// "daemon: " of its own — runSlot's "run-child: " backoff reason wraps it
// as-is. The empty Revision fails before StartChild reaches any exec seam.
func TestStartChild_ChildCommandErrorCarriesNoDaemonPrefix(t *testing.T) {
	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	req := daemon.ChildRequest{Slot: 0, Kind: daemon.KindOf(dispatchkind.Work)}
	_, err := startAndWait(context.Background(), r, req)
	if err == nil {
		t.Fatalf("startAndWait() error = nil, want non-nil")
	}
	got := err.Error()
	if !strings.HasPrefix(got, "revision must not be empty") {
		t.Errorf("startAndWait() error = %q, want prefix %q", got, "revision must not be empty")
	}
	if n := strings.Count(got, "daemon: "); n != 0 {
		t.Errorf(`startAndWait() error = %q, want zero "daemon: " (got %d)`, got, n)
	}
}

// TestStartChild_OverLongLineDiscardedThenValidArrives asserts a line past
// report.MaxLine is neither buffered without limit nor wedges the reader
// (issue #3627): reported once, and the next record still arrives.
func TestStartChild_OverLongLineDiscardedThenValidArrives(t *testing.T) {
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
		Slot: 0, Kind: daemon.KindOf(dispatchkind.Work), Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		OnRecord: func(rec daemon.Record) { got = append(got, rec) },
	}
	if _, err := startAndWait(context.Background(), r, req); err != nil {
		t.Fatalf("startAndWait() unexpected error: %v", err)
	}
	want := []daemon.Record{{Event: "box", Key: dispatchkey.Issue("9"), Phase: "initial"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("OnRecord calls = %+v, want %+v", got, want)
	}
	stderr := string(readStderr())
	if n := strings.Count(stderr, "daemon: read child report:"); n != 1 {
		t.Errorf("stderr diagnostic count = %d, want exactly 1 (got stderr: %q)", n, stderr)
	}
}

// TestStartChild_SettledLongNoteDeliveredOneRecord pins issue #3627: a ~5KB
// SPINDRIFT_OUTCOME note made the settled line exceed report.MaxLine, so the
// whole record was discarded. The bytes come from the real report.Reporter,
// not hand-rolled JSON, so report.emit's own clipping is what is exercised.
func TestStartChild_SettledLongNoteDeliveredOneRecord(t *testing.T) {
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
	rep.Settled(dispatchkey.Issue("123"), "blocked", note, "", "")
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
		Slot: 0, Kind: daemon.KindOf(dispatchkind.Work), Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		OnRecord: func(rec daemon.Record) { got = append(got, rec) },
	}
	if _, err := startAndWait(context.Background(), r, req); err != nil {
		t.Fatalf("startAndWait() unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("OnRecord calls = %+v, want exactly 1", got)
	}
	if got[0].Event != "settled" || got[0].Key != dispatchkey.Issue("123") || got[0].State != "blocked" {
		t.Errorf("record = %+v, want event=settled issue=123 state=blocked", got[0])
	}
	if stderr := readStderr(); len(stderr) != 0 {
		t.Errorf("stderr = %q, want empty (a within-bound record is not malformed)", stderr)
	}
}

// TestStartChild_StdoutRelayedUnchanged asserts a child's stdout reaches the
// daemon's stderr byte-for-byte, including a line past bufio's 64 KiB
// default, so no scanner line cap sits in between. literalLine matches the
// deleted announce regex, so a relay that still parsed it would show.
func TestStartChild_StdoutRelayedUnchanged(t *testing.T) {
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
		Slot: 0, Kind: daemon.KindOf(dispatchkind.Work), Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		OnRecord: func(rec daemon.Record) {
			t.Errorf("OnRecord called with %+v, want none (nothing written to the report pipe)", rec)
		},
	}
	result, err := startAndWait(context.Background(), r, req)
	if err != nil {
		t.Fatalf("startAndWait() unexpected error: %v", err)
	}
	if result.Exit != 0 {
		t.Errorf("Exit = %d, want 0", result.Exit)
	}
	want := bigLine + literalLine
	if got := string(readStderr()); got != want {
		t.Errorf("relayed stdout does not match byte-for-byte\ngot len=%d\nwant len=%d", len(got), len(want))
	}
}

// TestStartChild_ChildLogTakesBothStreams asserts the Child log holds exactly
// the child's stdout and stderr, interleaved in write order, and the daemon's
// stderr gets none of it. The payload carries a non-UTF-8 byte and no
// trailing newline: nothing may line-buffer or re-encode.
func TestStartChild_ChildLogTakesBothStreams(t *testing.T) {
	orig := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = orig })

	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", `printf 'out\377 '; printf 'err ' >&2; printf 'tail'`)
	}
	want := "out\xff err tail"

	dir := t.TempDir()
	r := mustHostRunner(t, hostRunnerConfig{repoPath: dir, appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	readStderr := captureStderr(t)
	rel := filepath.Join(".spindrift", "logs", "daemon", "20261007T120304.123Z-slot0-dispatch.log")
	result, err := startAndWait(context.Background(), r, daemon.ChildRequest{
		Slot: 0, Kind: daemon.KindOf(dispatchkind.Work), Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", ChildLog: rel,
	})
	if err != nil {
		t.Fatalf("startAndWait() unexpected error: %v", err)
	}
	if result.Exit != 0 {
		t.Errorf("Exit = %d, want 0", result.Exit)
	}
	if got := string(readStderr()); got != "" {
		t.Errorf("daemon stderr = %q, want empty", got)
	}
	logged, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatalf("read Child log: %v", err)
	}
	if string(logged) != want {
		t.Errorf("Child log = %q, want %q", logged, want)
	}
}

const daemonSideHelperEnv = "SPINDRIFT_TEST_DAEMON_SIDE_HELPER"

// TestHelperDaemonSide is not a test: it is the Daemon side of
// TestStartChild_ChildSurvivesDaemonSigkill, re-exec'd as its own process so the
// test can kill it outright. It is a no-op unless daemonSideHelperEnv is set.
func TestHelperDaemonSide(t *testing.T) {
	if os.Getenv(daemonSideHelperEnv) != "1" {
		t.Skip("helper process only")
	}
	done := os.Getenv(daemonSideHelperEnv + "_DONE")
	gate := os.Getenv(daemonSideHelperEnv + "_GATE")
	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", fmt.Sprintf(`echo first; echo err1 >&2; while [ ! -e %q ]; do sleep 0.05; done; echo second; echo err2 >&2; touch %q`, gate, done))
	}
	r := mustHostRunner(t, hostRunnerConfig{repoPath: os.Getenv(daemonSideHelperEnv + "_REPO"), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	_, _ = startAndWait(context.Background(), r, daemon.ChildRequest{
		Slot: 0, Kind: daemon.KindOf(dispatchkind.Work), Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", ChildLog: "child.log",
	})
}

// TestStartChild_ChildSurvivesDaemonSigkill asserts a Daemon killed outright
// mid-run does not take its child down: the child writes straight to the
// Child log, so nothing it writes can hit a pipe whose reader died.
func TestStartChild_ChildSurvivesDaemonSigkill(t *testing.T) {
	repo := t.TempDir()
	done := filepath.Join(repo, "done")
	gate := filepath.Join(repo, "gate")
	logPath := filepath.Join(repo, "child.log")

	helper := exec.Command(os.Args[0], "-test.run=^TestHelperDaemonSide$")
	helper.Env = append(os.Environ(), daemonSideHelperEnv+"=1", daemonSideHelperEnv+"_DONE="+done, daemonSideHelperEnv+"_GATE="+gate, daemonSideHelperEnv+"_REPO="+repo)
	helper.Stdout, helper.Stderr = io.Discard, io.Discard
	if err := helper.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	t.Cleanup(func() { _ = helper.Process.Kill(); _ = helper.Wait() })
	// The child sits in its own process group, out of the helper's kill, so
	// release its gate on any early exit or it polls forever as an orphan.
	t.Cleanup(func() { _ = os.WriteFile(gate, nil, 0o600) })

	deadline := time.Now().Add(10 * time.Second)
	for {
		if b, _ := os.ReadFile(logPath); strings.Contains(string(b), "first") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child never wrote to the Child log")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := helper.Process.Kill(); err != nil {
		t.Fatalf("kill helper: %v", err)
	}
	_ = helper.Wait()
	// The child holds its second write until the Daemon is dead and reaped, so
	// it always lands after the Daemon, which once read this output, is gone.
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	deadline = time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(done); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child died with the Daemon before finishing")
		}
		time.Sleep(10 * time.Millisecond)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if want := "first\nerr1\nsecond\nerr2\n"; string(logged) != want {
		t.Errorf("Child log = %q, want %q", logged, want)
	}
}

// TestStartChild_ChildLogUncreatableFallsBackToStderr asserts a Child log that
// cannot be created costs the Dispatch nothing: one diagnostic line, then the
// child's output on the daemon's own stderr descriptor.
func TestStartChild_ChildLogUncreatableFallsBackToStderr(t *testing.T) {
	orig := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = orig })

	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", `printf 'hello'`)
	}

	dir := t.TempDir()
	// A regular file where the log's parent directory should go.
	if err := os.WriteFile(filepath.Join(dir, "blocked"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	r := mustHostRunner(t, hostRunnerConfig{repoPath: dir, appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	readStderr := captureStderr(t)
	result, err := startAndWait(context.Background(), r, daemon.ChildRequest{
		Slot: 0, Kind: daemon.KindOf(dispatchkind.Work), Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		ChildLog: filepath.Join("blocked", "x.log"),
	})
	if err != nil {
		t.Fatalf("startAndWait() unexpected error: %v", err)
	}
	if result.Exit != 0 {
		t.Errorf("Exit = %d, want 0", result.Exit)
	}
	got := string(readStderr())
	if !strings.HasSuffix(got, "hello") || !strings.Contains(got, "child log") {
		t.Errorf("daemon stderr = %q, want a child-log diagnostic then %q", got, "hello")
	}
}

// runWithLingeringGrandchild runs StartChild against a fake child that
// backgrounds a grandchild holding the Child log (fd 3 is closed for it so
// the report pipe is not in play), then exits with exitCode. The grandchild is
// killed on cleanup.
func runWithLingeringGrandchild(t *testing.T, exitCode int) (daemon.ChildResult, time.Duration, error) {
	t.Helper()
	orig := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = orig })

	dir := t.TempDir()
	pidFile := filepath.Join(dir, "grandchild.pid")
	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", fmt.Sprintf(`sleep 30 3>&- & echo $! >%q; exit %d`, pidFile, exitCode))
	}
	t.Cleanup(func() {
		b, err := os.ReadFile(pidFile)
		if err != nil {
			return
		}
		if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})

	r := mustHostRunner(t, hostRunnerConfig{repoPath: dir, appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	readStderr := captureStderr(t)
	start := time.Now()
	result, err := startAndWait(context.Background(), r, daemon.ChildRequest{
		Slot: 0, Kind: daemon.KindOf(dispatchkind.Work), Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		ChildLog: "child.log",
	})
	_ = readStderr()
	return result, time.Since(start), err
}

// TestStartChild_ChildLogDoesNotWaitOnGrandchild asserts start+wait returns the
// child's own exit, clean or not, once it exits, even when a grandchild still
// holds the Child log open.
func TestStartChild_ChildLogDoesNotWaitOnGrandchild(t *testing.T) {
	for _, exitCode := range []int{3, 0} {
		t.Run(fmt.Sprintf("exit%d", exitCode), func(t *testing.T) {
			result, d, err := runWithLingeringGrandchild(t, exitCode)
			if err != nil {
				t.Fatalf("startAndWait() unexpected error: %v", err)
			}
			if result.Exit != exitCode {
				t.Errorf("Exit = %d, want %d", result.Exit, exitCode)
			}
			if d > 5*time.Second {
				t.Errorf("start+wait took %v, want it prompt, not held by the grandchild", d)
			}
		})
	}
}

// TestStartChild_CleanExitNoRecords pins the other end of the report-pipe
// seam: a clean exit with nothing written to descriptor 3 reports Exit 0
// and fires OnRecord not at all.
func TestStartChild_CleanExitNoRecords(t *testing.T) {
	orig := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = orig })

	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", `printf 'nothing to see\n'; exit 0`)
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	var got []daemon.Record
	req := daemon.ChildRequest{
		Slot: 0, Kind: daemon.KindOf(dispatchkind.Research), Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		OnRecord: func(rec daemon.Record) { got = append(got, rec) },
	}
	result, err := startAndWait(context.Background(), r, req)
	if err != nil {
		t.Fatalf("startAndWait() unexpected error: %v", err)
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

// assertEnvStripsKnobsKeepsSecrets asserts the knob MODEL was stripped while
// PATH and GH_TOKEN passed through. It never prints a GH_TOKEN value: a child
// exec'd with the raw process env would carry the live token into the log.
func assertEnvStripsKnobsKeepsSecrets(t *testing.T, dumped string) {
	t.Helper()
	got := strings.Split(strings.TrimRight(dumped, "\n"), "\n")
	want := []string{"MODEL=<unset>", "PATH=/bin:/usr/bin", "GH_TOKEN=super-secret"}
	if len(got) != len(want) {
		t.Fatalf("child env dump keys = %v, want %v", envDumpKeys(got, want), envDumpKeys(want, want))
	}
	for i, w := range want {
		if got[i] == w {
			continue
		}
		if key, val, _ := strings.Cut(got[i], "="); key == "GH_TOKEN" && val != "<unset>" {
			t.Errorf("child env line %d = %q, want %q", i, "GH_TOKEN=<redacted>", w)
			continue
		}
		t.Errorf("child env line %d = %q, want %q", i, got[i], w)
	}
}

// envDumpKeys names each dumped line by its key, or "?" for a line whose
// key is not one want expects: a value carrying a newline splits into
// extra lines, and a fragment of a live token must not reach the log.
func envDumpKeys(got, want []string) []string {
	keys := make([]string, len(got))
	for i, line := range got {
		key, _, _ := strings.Cut(line, "=")
		keys[i] = "?"
		for _, w := range want {
			if strings.HasPrefix(w, key+"=") {
				keys[i] = key
				break
			}
		}
	}
	return keys
}

// TestStartChild_EnvStripsKnobsKeepsSecrets has the child dump its env to a
// file, since its stdout goes straight to os.Stderr.
func TestStartChild_EnvStripsKnobsKeepsSecrets(t *testing.T) {
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
	got, err := startAndWait(context.Background(), r, daemon.ChildRequest{Slot: 0, Kind: daemon.KindOf(dispatchkind.Work), Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"})
	if err != nil {
		t.Fatalf("startAndWait() unexpected error: %v", err)
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

// A document key whose value does not count is left out of the strip list, so
// the child inherits the daemon's ambient value; a counting key is still
// stripped.
func TestStartChild_EnvKeepsAmbientForKeyWhoseDocumentValueDoesNotCount(t *testing.T) {
	orig := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = orig })

	dumpFile := filepath.Join(t.TempDir(), "env.out")
	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", fmt.Sprintf(`{ echo "REPO_SLUG=${REPO_SLUG-<unset>}"; echo "MODEL=${MODEL-<unset>}"; } >%q`, dumpFile))
	}
	doc := &inputdoc.Document{Settings: map[string]string{"REPO_SLUG": "", "MODEL": "x"}}

	r := mustHostRunner(t, hostRunnerConfig{
		repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux",
		env:   []string{"REPO_SLUG=o/r", "MODEL=from-shell", "PATH=/bin:/usr/bin"},
		knobs: strippedKeys(doc),
	})
	if _, err := startAndWait(context.Background(), r, daemon.ChildRequest{Slot: 0, Kind: daemon.KindOf(dispatchkind.Work), Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"}); err != nil {
		t.Fatalf("startAndWait() unexpected error: %v", err)
	}
	dumped, err := os.ReadFile(dumpFile)
	if err != nil {
		t.Fatalf("read dumped env: %v", err)
	}
	if got, want := string(dumped), "REPO_SLUG=o/r\nMODEL=<unset>\n"; got != want {
		t.Errorf("child env = %q, want %q", got, want)
	}
}

// TestStartChild_ChildInOwnProcessGroup asserts the child gets its own
// process group, so a group-wide Ctrl-C never reaches it (issue #3538). The
// child reports its own $$: reading cmd.Process from outside would race
// cmd.Start().
func TestStartChild_ChildInOwnProcessGroup(t *testing.T) {
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
		if _, err := startAndWait(context.Background(), r, daemon.ChildRequest{Slot: 0, Kind: daemon.KindOf(dispatchkind.Work), Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"}); err != nil {
			t.Errorf("startAndWait() unexpected error: %v", err)
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

// TestStartChild_StdinIsDevNull pins that StartChild never sets cmd.Stdin: in
// its own process group a tty read stops with SIGTTIN (issues #3538, #3548).
// Reading the captured Cmd's Stdin is race-free, since StartChild would set
// it, if at all, before cmd.Start().
func TestStartChild_StdinIsDevNull(t *testing.T) {
	orig := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = orig })
	var captured *exec.Cmd
	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		captured = exec.Command("/bin/sh", "-c", "true")
		return captured
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	if _, err := startAndWait(context.Background(), r, daemon.ChildRequest{Slot: 0, Kind: daemon.KindOf(dispatchkind.Work), Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"}); err != nil {
		t.Fatalf("startAndWait() unexpected error: %v", err)
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

// TestResolveTip_FetchesWithoutMutatingWorkingTree asserts ResolveTip sees a
// commit dirAdvancer pushed after dirConsumer's clone while leaving the
// consumer's HEAD, branch and working tree untouched: a fetch, never a pull.
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

// TestResolveTip_ConcurrentCallsDoNotRace pins issue #3539: origin/main has
// genuinely advanced, so each fetch must re-lock the ref, where unsynchronized
// fetches raced the lock and interleaved on FETCH_HEAD. Every call must
// return the same advanced tip.
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

// TestResolveTip_CancelledContext pins issue #3538: ResolveTip must pass ctx
// to git, or a cancelled caller hangs. The deadline below is the tripwire.
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

// helperGitSeam returns a runnerFetchCommand fake whose git subcommand sub
// runs script, which backgrounds a sleep that keeps the output pipe open the
// way a git-remote-* helper does, so killing the shell alone does not release
// Wait. The script runs in its own process group, killed at cleanup, so the
// sleep does not outlive the test.
func helperGitSeam(t *testing.T, sub, script string) func(context.Context, string, ...string) *exec.Cmd {
	t.Helper()
	return func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		if len(args) < 3 {
			// t.Errorf, not t.Fatalf: this runs on fetchRevision's goroutine.
			t.Errorf("runnerFetchCommand args = %v, want at least 3", args)
			return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 1")
		}
		switch args[2] { // git -C <repo> <sub> ...
		case sub:
			cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script)
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			t.Cleanup(func() {
				if cmd.Process != nil {
					_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				}
			})
			return cmd
		case "rev-parse":
			return exec.CommandContext(ctx, "/bin/sh", "-c", "printf 0123456789abcdef0123456789abcdef01234567")
		default:
			return exec.CommandContext(ctx, "true")
		}
	}
}

// hangingGitSeam hangs git subcommand sub until its grandchild exits.
func hangingGitSeam(t *testing.T, sub string) func(context.Context, string, ...string) *exec.Cmd {
	return helperGitSeam(t, sub, "sleep 30 & wait")
}

// lingeringGitSeam exits git subcommand sub 0 while its grandchild lingers.
func lingeringGitSeam(t *testing.T, sub string) func(context.Context, string, ...string) *exec.Cmd {
	return helperGitSeam(t, sub, "sleep 10 >&2 & exit 0")
}

// TestFetchRevision_CancelHangingGitReturnsPromptly: a grandchild holding the
// output pipe must not keep fetchRevision blocked past a cancelled ctx (issue
// #5278); exec.CommandContext alone kills only the direct child.
func TestFetchRevision_CancelHangingGitReturnsPromptly(t *testing.T) {
	for _, sub := range []string{"fetch", "rev-parse", "ls-remote"} {
		t.Run(sub, func(t *testing.T) {
			orig := runnerFetchCommand
			t.Cleanup(func() { runnerFetchCommand = orig })
			runnerFetchCommand = hangingGitSeam(t, sub)

			r := mustHostRunner(t, hostRunnerConfig{repoPath: "/repo", appAttr: ".#", baseBranch: "main", featureBranch: "feature-x", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			time.AfterFunc(200*time.Millisecond, cancel)

			done := make(chan error, 1)
			go func() {
				_, err := r.fetchRevision(ctx)
				done <- err
			}()
			select {
			case err := <-done:
				if err == nil {
					t.Error("fetchRevision(cancelled mid-run) error = nil, want non-nil")
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("fetchRevision stuck on a hung git %s after ctx cancel", sub)
			}
		})
	}
}

// TestFetchRevision_ZeroExitWithLingeringHelperSucceeds pins that a git which
// exited 0 but left a helper holding its stderr is a success (exec.ErrWaitDelay).
func TestFetchRevision_ZeroExitWithLingeringHelperSucceeds(t *testing.T) {
	for _, sub := range []string{"fetch", "ls-remote"} {
		t.Run(sub, func(t *testing.T) {
			orig := runnerFetchCommand
			t.Cleanup(func() { runnerFetchCommand = orig })
			runnerFetchCommand = lingeringGitSeam(t, sub)

			r := mustHostRunner(t, hostRunnerConfig{repoPath: "/repo", appAttr: ".#", baseBranch: "main", featureBranch: "feature-x", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
			start := time.Now()
			rev, err := r.fetchRevision(context.Background())
			if err != nil {
				t.Fatalf("fetchRevision error = %v, want nil", err)
			}
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Errorf("fetchRevision took %v, want < 5s despite the lingering helper", elapsed)
			}
			if want := "0123456789abcdef0123456789abcdef01234567"; rev != want {
				t.Errorf("revision = %q, want %q", rev, want)
			}
		})
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

// childExitOnFirstSignal exits 7 on SIGTERM, proving a drain rather than a
// SIGKILL. `: >"$0"` creates the marker waitForArmed polls for; the short
// sleep loop returns to the shell often enough to run the trap.
const childExitOnFirstSignal = `trap 'exit 7' TERM; : >"$0"; while :; do sleep 0.05; done`

// childExitOnSecondSignal traps both SIGTERM and SIGINT, counts them, and
// exits 7 only on the second — the Stop-then-Abort escalation contract.
// Marker file as above. The loop spins on a builtin and never forks: on
// aarch64-darwin /bin/sh is bash 3.2, whose SIGINT bookkeeping around a
// reaped child dropped the INT trap whether the loop waited on `sleep` in
// the foreground or through the `wait` builtin. With no child to reap, the
// trap runs between builtins; the spin lasts only the test's ~200ms.
const childExitOnSecondSignal = `
n=0
trap 'n=$((n+1)); if [ "$n" -ge 2 ]; then exit 7; fi' TERM INT
: >"$0"
while :; do :; done`

// startChild runs StartChild and Wait on a goroutine with the given latch,
// returning the result and error channels. Cleaning up the child is the
// caller's job, via its own exec seam override.
func startChild(t *testing.T, r *hostRunner, stop, abort <-chan struct{}) (<-chan daemon.ChildResult, <-chan error) {
	t.Helper()
	resultCh := make(chan daemon.ChildResult, 1)
	errCh := make(chan error, 1)
	go func() {
		got, err := startAndWait(context.Background(), r, daemon.ChildRequest{Slot: 0, Kind: daemon.KindOf(dispatchkind.Work), Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", Stop: stop, Abort: abort})
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- got
	}()
	return resultCh, errCh
}

// TestStartChild_StopClosedSignalsSIGTERM asserts closing Stop delivers a
// SIGTERM the child drains on, never a kill (issue #3538).
func TestStartChild_StopClosedSignalsSIGTERM(t *testing.T) {
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
		t.Fatalf("startAndWait() unexpected error: %v", err)
	case got := <-resultCh:
		if got.Exit != 7 {
			t.Errorf("Exit = %d, want 7 (child trapped SIGTERM and exited on its own terms, not SIGKILLed)", got.Exit)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("closing Stop did not cause the child to exit promptly (killed instead of drained, or signal never delivered)")
	}
}

// TestStartChild_StopSignalsEveryConcurrentChild is
// TestStartChild_StopClosedSignalsSIGTERM at three concurrent children, so
// shared forwarding state cannot leave two undrained.
func TestStartChild_StopSignalsEveryConcurrentChild(t *testing.T) {
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
			got, err := startAndWait(context.Background(), r, daemon.ChildRequest{Slot: slot, Kind: daemon.KindOf(dispatchkind.Work), Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", Stop: stop})
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
			t.Fatalf("startAndWait() unexpected error: %v", err)
		case got := <-resultCh:
			if got.Exit != 7 {
				t.Errorf("Exit = %d, want 7 (child trapped SIGTERM and exited on its own terms, not SIGKILLed)", got.Exit)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("closing Stop did not cause every child to exit promptly (some abandoned, not drained)")
		}
	}
}

// TestStartChild_ChildStartedAfterStopClosedIsSignalledAtOnce covers a child
// started after Stop closed.
func TestStartChild_ChildStartedAfterStopClosedIsSignalledAtOnce(t *testing.T) {
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
	close(stop) // closed before StartChild is even called — the extreme end of the race window

	resultCh, errCh := startChild(t, r, stop, nil)

	select {
	case err := <-errCh:
		t.Fatalf("startAndWait() unexpected error: %v", err)
	case got := <-resultCh:
		// Not exactly 7: the signal can beat the shell's `trap` install and
		// kill it by default action (-1). The loop never exits unsignalled,
		// so any nonzero exit proves delivery.
		if got.Exit == 0 {
			t.Errorf("Exit = %d, want nonzero (busy loop never exits unsignalled)", got.Exit)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("child started after Stop closed was never signalled (abandoned, not drained)")
	}
}

// TestStartChild_StopThenAbortDeliversDistinctKinds asserts Stop then Abort
// deliver SIGTERM then SIGINT to a real child, the drain-then-abort contract
// of issue #3521.
func TestStartChild_StopThenAbortDeliversDistinctKinds(t *testing.T) {
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
		t.Fatalf("startAndWait() unexpected error: %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	close(abort)

	select {
	case err := <-errCh:
		t.Fatalf("startAndWait() unexpected error: %v", err)
	case got := <-resultCh:
		if got.Exit != 7 {
			t.Errorf("Exit = %d, want 7 (child observed SIGTERM then SIGINT and exited on the second)", got.Exit)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("closing Abort did not cause the child to exit promptly")
	}
}

// TestStartChild_ChildStartedAfterBothLatchesClosedSeesBothKinds asserts two
// distinct kinds, in order, when both latches closed before StartChild. It
// reads them off the runnerSignal seam because a handler-less child can die on
// the first signal before the second is observable. The child is a bare
// sleep, the cheapest that still drives the real forwarding path.
func TestStartChild_ChildStartedAfterBothLatchesClosedSeesBothKinds(t *testing.T) {
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
		t.Fatalf("startAndWait() unexpected error: %v", err)
	case got := <-resultCh:
		if got.Exit != 0 {
			t.Errorf("Exit = %d, want 0 (unsignalled sleep 0.3 exits cleanly; runnerSignal is stubbed here, not the real kernel call)", got.Exit)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("startAndWait did not return promptly")
	}

	mu.Lock()
	got := append([]os.Signal(nil), sent...)
	mu.Unlock()
	want := []os.Signal{syscall.SIGTERM, syscall.SIGINT}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("signals sent = %v, want %v (SIGTERM then SIGINT, both latches already closed before start)", got, want)
	}
}

// TestStartChild_ForwardingStopsAfterChildExits asserts a Stop closing after
// the child exited on its own sends nothing.
func TestStartChild_ForwardingStopsAfterChildExits(t *testing.T) {
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
	got, err := startAndWait(context.Background(), r, daemon.ChildRequest{Slot: 0, Kind: daemon.KindOf(dispatchkind.Work), Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", Stop: stop})
	if err != nil {
		t.Fatalf("startAndWait() unexpected error: %v", err)
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
		t.Errorf("signals sent after startAndWait returned = %d, want 0 (the forwarding goroutine must not outlive its child)", n)
	}
}

// TestStartChild_NilStopAbortRunsNormally asserts a ChildRequest with no
// latch runs and exits normally.
func TestStartChild_NilStopAbortRunsNormally(t *testing.T) {
	orig := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = orig })
	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", "exit 0")
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	got, err := startAndWait(context.Background(), r, daemon.ChildRequest{Slot: 0, Kind: daemon.KindOf(dispatchkind.Work), Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"})
	if err != nil {
		t.Fatalf("startAndWait() unexpected error: %v", err)
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
	se := selfEvalErrorT(t, "error: attribute missing")
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

// scriptedFetchSeamT fakes runnerFetchCommand with no remote: "fetch" runs
// onFetch (to block the leader, or fail) and "rev-parse" echoes revision(),
// so a test moves the tip between calls. args[2] is the subcommand, per
// fetchRevision's "-C", repoPath, sub call shape.
func scriptedFetchSeamT(t *testing.T, revision func() string, onFetch func()) func(ctx context.Context, name string, args ...string) *exec.Cmd {
	t.Helper()
	return func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if len(args) < 3 {
			// Not Fatalf: this may run off the test goroutine.
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

// TestResolveTip_ConcurrentCallersCoalesceIntoOneFetchAndOneEval holds the
// leader in the fetch seam until both others join (runnerFlightJoined), then
// asserts one fetch, one eval, and an identical Tip for all three.
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

// TestResolveTip_SelfPathMemoisedPerRevision asserts one `nix eval` per
// distinct revision.
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

// TestResolveTip_FetchErrorReachesEveryJoiner asserts every caller sharing a
// flight gets the leader's identical fetch error.
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
	exit, _, err := r.RunDoctor(context.Background(), "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	if err != nil {
		t.Fatalf("RunDoctor() unexpected error: %v", err)
	}
	if exit != 0 {
		t.Errorf("exit = %d, want 0", exit)
	}
}

// TestRunDoctor_EnvStripsKnobsKeepsSecrets is RunDoctor's half of
// TestStartChild_EnvStripsKnobsKeepsSecrets.
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
	exit, _, err := r.RunDoctor(context.Background(), "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
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
	exit, _, err := r.RunDoctor(context.Background(), "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
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
	_, _, err := r.RunDoctor(context.Background(), "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	if err == nil {
		t.Fatal("RunDoctor() error = nil, want non-nil")
	}
}

// TestRunDoctor_ArgvIsDoctorCommand asserts the seam's argv is
// DoctorCommand's: a pinned flakeref ending in "-- doctor", without
// --max-jobs/--max-parallel (doctor dispatches nothing) or --verbose/-v (the
// daemon relies on quiet-by-default doctor, #3777). Checked per token, since
// DeepEqual against DoctorCommand would miss it growing a --verbose, and not
// by substring, which could match inside a flakeref or revision.
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
	r := mustHostRunner(t, hostRunnerConfig{repoPath: "/repo", appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: []string{"PATH=/bin"}})
	if _, _, err := r.RunDoctor(context.Background(), revision); err != nil {
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

// TestRunDoctor_ButlerAppendsFlag asserts the runner passes its configured
// doctor flags through to the doctor argv (issue #3920). Which flags a run
// gets is doctorPreflightFlags's job, not the runner's.
func TestRunDoctor_ButlerAppendsFlag(t *testing.T) {
	orig := runnerDoctorCommand
	t.Cleanup(func() { runnerDoctorCommand = orig })

	var gotArgs []string
	runnerDoctorCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		gotArgs = append([]string(nil), args...)
		return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 0")
	}

	revision := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	r := mustHostRunner(t, hostRunnerConfig{repoPath: "/repo", appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: []string{"PATH=/bin"}, doctorFlags: []string{"--butler"}})
	if _, _, err := r.RunDoctor(context.Background(), revision); err != nil {
		t.Fatalf("RunDoctor() unexpected error: %v", err)
	}

	joined := strings.Join(gotArgs, " ")
	if !strings.HasSuffix(joined, "-- doctor --butler") {
		t.Errorf("argv %v does not end in %q", gotArgs, "-- doctor --butler")
	}
}

// TestRunDoctor_NoButlerOmitsFlag asserts the converse: a runner built with
// no doctor flags adds none to the doctor argv.
func TestRunDoctor_NoButlerOmitsFlag(t *testing.T) {
	orig := runnerDoctorCommand
	t.Cleanup(func() { runnerDoctorCommand = orig })

	var gotArgs []string
	runnerDoctorCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		gotArgs = append([]string(nil), args...)
		return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 0")
	}

	revision := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	r := mustHostRunner(t, hostRunnerConfig{repoPath: "/repo", appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	if _, _, err := r.RunDoctor(context.Background(), revision); err != nil {
		t.Fatalf("RunDoctor() unexpected error: %v", err)
	}

	for _, tok := range gotArgs {
		if tok == "--butler" {
			t.Errorf("argv %v carries --butler, want none (no doctor flags)", gotArgs)
		}
	}
}

// TestRunDoctor_HealthyPreflightEmitsNothing asserts a silent doctor (as
// quiet-by-default doctor is when healthy, #3777) leaves both daemon streams
// silent: RunDoctor adds no framing.
func TestRunDoctor_HealthyPreflightEmitsNothing(t *testing.T) {
	orig := runnerDoctorCommand
	t.Cleanup(func() { runnerDoctorCommand = orig })
	runnerDoctorCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 0")
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	readStderr := captureStderr(t)
	readStdout := captureStdout(t)
	exit, _, err := r.RunDoctor(context.Background(), "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
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

// TestRunDoctor_RefusedPreflightForwardsFindings mirrors two real doctor
// refusals (labels missing, exit 4; unprotected base branch, exit 1) and
// asserts every line lands on the daemon's stderr with the child's own exit.
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
printf '%s\n' 'required label(s) missing or declined: ready-for-agent, agent-in-progress missing — create them in the repository' >&2
exit 4`,
			wantExit: 4,
			wantStderr: []string{
				`MISSING: label "ready-for-agent" missing`,
				`MISSING: label "agent-in-progress" missing`,
				"required label(s) missing or declined: ready-for-agent, agent-in-progress missing — create them in the repository",
			},
		},
		{
			// branch-protection's Remedy differs from its probe error, so both
			// lines print, and doctorReport prints the error to stderr. It
			// wraps no sentinel, hence exit 1 (connectivity rows exit 3).
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
			exit, _, err := r.RunDoctor(context.Background(), "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
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
		_, _, err := r.RunDoctor(ctx, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
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
	_, _, err := r.RunDoctor(context.Background(), "")
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
	exit, _, err := r.RunDoctor(context.Background(), "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	if err == nil {
		t.Fatalf("RunDoctor() = (%d, nil), want a non-nil error for a signal-killed child", exit)
	}
	if !strings.Contains(err.Error(), "signal") {
		t.Errorf("RunDoctor() error = %q, want it to name the signal that ended the child", err.Error())
	}
}

// TestNewHostRunner_RejectsNilEnv pins issue #3692: a nil cfg.env is an
// uncaptured environment, rejected before withoutKeys makes it look empty.
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

// TestStartChild_FeatureBranchAppendsBaseBranchArg pins issue #3882: a set
// featureBranch reaches the child argv as --base-branch; unset, none appears.
func TestStartChild_FeatureBranchAppendsBaseBranchArg(t *testing.T) {
	tests := []struct {
		name          string
		featureBranch string
		wantSubstr    string
		wantAbsent    bool
	}{
		{name: "feature branch set", featureBranch: "feature-x", wantSubstr: "--base-branch feature-x"},
		{name: "feature branch unset", wantAbsent: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orig := runnerExecCommand
			t.Cleanup(func() { runnerExecCommand = orig })

			var gotArgv []string
			runnerExecCommand = func(name string, args ...string) *exec.Cmd {
				gotArgv = append([]string{name}, args...)
				return exec.Command("/bin/sh", "-c", "exit 0")
			}

			r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", featureBranch: tt.featureBranch, selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
			if _, err := startAndWait(context.Background(), r, daemon.ChildRequest{Slot: 0, Kind: daemon.KindOf(dispatchkind.Work), Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"}); err != nil {
				t.Fatalf("startAndWait() unexpected error: %v", err)
			}

			joined := strings.Join(gotArgv, " ")
			if tt.wantAbsent {
				if strings.Contains(joined, "--base-branch") {
					t.Errorf("argv %v carries --base-branch, want none (featureBranch unset)", gotArgv)
				}
				return
			}
			if !strings.Contains(joined, tt.wantSubstr) {
				t.Errorf("argv %v does not carry %q", gotArgv, tt.wantSubstr)
			}
		})
	}
}

// TestRunDoctor_FeatureBranchAppendsBaseBranchArg is the doctor preflight's
// sibling of TestStartChild_FeatureBranchAppendsBaseBranchArg above.
func TestRunDoctor_FeatureBranchAppendsBaseBranchArg(t *testing.T) {
	orig := runnerDoctorCommand
	t.Cleanup(func() { runnerDoctorCommand = orig })

	var gotArgv []string
	runnerDoctorCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		gotArgv = append([]string{name}, args...)
		return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 0")
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: "/repo", appAttr: ".#", baseBranch: "main", featureBranch: "feature-x", nixSystem: "x86_64-linux", env: os.Environ()})
	if _, _, err := r.RunDoctor(context.Background(), "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"); err != nil {
		t.Fatalf("RunDoctor() unexpected error: %v", err)
	}

	if !strings.Contains(strings.Join(gotArgv, " "), "--base-branch feature-x") {
		t.Errorf("argv %v does not carry --base-branch feature-x", gotArgv)
	}
}

// TestResolveTip_FetchesBaseBranchNotFeatureBranch pins that ResolveTip's
// fetch and self eval track baseBranch, so a feature-branch merge never
// halts the daemon self-changed (issue #3882). selfAttr is set so the eval
// actually runs.
func TestResolveTip_FetchesBaseBranchNotFeatureBranch(t *testing.T) {
	origFetch := runnerFetchCommand
	t.Cleanup(func() { runnerFetchCommand = origFetch })
	origEval := runnerEvalCommand
	t.Cleanup(func() { runnerEvalCommand = origEval })

	var gotFetchArgs []string
	runnerFetchCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if len(args) >= 3 && args[2] == "fetch" {
			gotFetchArgs = append([]string(nil), args...)
			return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 0")
		}
		return exec.CommandContext(ctx, "/bin/sh", "-c", "printf 'deadbeef\\n'")
	}
	var gotEvalArgs []string
	runnerEvalCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		gotEvalArgs = append([]string{name}, args...)
		return exec.CommandContext(ctx, "/bin/sh", "-c", `printf '/nix/store/abc-daemon\n'`)
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: "/repo", appAttr: ".#", baseBranch: "main", featureBranch: "feature-x", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	if _, err := r.ResolveTip(context.Background()); err != nil {
		t.Fatalf("ResolveTip() unexpected error: %v", err)
	}

	joined := strings.Join(gotFetchArgs, " ")
	if !strings.Contains(joined, "main") {
		t.Errorf("fetch args = %v, want it to name baseBranch %q", gotFetchArgs, "main")
	}
	if strings.Contains(joined, "feature-x") {
		t.Errorf("fetch args = %v, want no featureBranch %q", gotFetchArgs, "feature-x")
	}

	evalJoined := strings.Join(gotEvalArgs, " ")
	if !strings.Contains(evalJoined, "rev=deadbeef") {
		t.Errorf("eval args = %v, want the self-path eval pinned to the base-branch revision %q", gotEvalArgs, "deadbeef")
	}
	if strings.Contains(evalJoined, "feature-x") {
		t.Errorf("eval args = %v, want nothing tied to featureBranch %q", gotEvalArgs, "feature-x")
	}
}

// scriptedFeatureBranchSeamT is scriptedFetchSeamT for the --feature-branch
// check (issue #3883), except "fetch" always succeeds: "ls-remote" runs
// onLsRemote and exits lsRemoteExit (0 exists, 2 no matching ref, else a
// remote error).
func scriptedFeatureBranchSeamT(t *testing.T, revision func() string, lsRemoteExit int, onLsRemote func()) func(ctx context.Context, name string, args ...string) *exec.Cmd {
	t.Helper()
	return func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if len(args) < 3 {
			t.Errorf("runnerFetchCommand args = %v, want at least 3", args)
			return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 1")
		}
		switch args[2] {
		case "fetch":
			return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 0")
		case "ls-remote":
			if onLsRemote != nil {
				onLsRemote()
			}
			return exec.CommandContext(ctx, "/bin/sh", "-c", fmt.Sprintf("exit %d", lsRemoteExit))
		default:
			return exec.CommandContext(ctx, "/bin/sh", "-c", fmt.Sprintf("printf '%s\\n'", revision()))
		}
	}
}

// TestFetchRevision_FeatureBranchGoneReturnsTypedError pins ls-remote's exit
// 2 ("no matching ref") turning into the typed *daemon.FeatureBranchGoneError
// the loop halts the pool on, carrying the configured branch and remote
// through untouched.
func TestFetchRevision_FeatureBranchGoneReturnsTypedError(t *testing.T) {
	orig := runnerFetchCommand
	t.Cleanup(func() { runnerFetchCommand = orig })
	runnerFetchCommand = scriptedFeatureBranchSeamT(t, func() string { return "deadbeef" }, 2, nil)

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", featureBranch: "feature-x", nixSystem: "x86_64-linux", env: os.Environ()})

	_, err := r.fetchRevision(context.Background())
	if err == nil {
		t.Fatal("fetchRevision() error = nil, want a FeatureBranchGoneError")
	}
	var gone *daemon.FeatureBranchGoneError
	if !errors.As(err, &gone) {
		t.Fatalf("fetchRevision() error = %v, want errors.As *daemon.FeatureBranchGoneError", err)
	}
	if gone.Branch != "feature-x" {
		t.Errorf("gone.Branch = %q, want %q", gone.Branch, "feature-x")
	}
	if gone.Remote != "origin" {
		t.Errorf("gone.Remote = %q, want %q", gone.Remote, "origin")
	}
}

// TestFetchRevision_FeatureBranchRemoteErrorIsOrdinary pins that any other
// ls-remote exit (128, an auth/network stand-in) stays an ordinary error, so
// a network blip never halts the campaign.
func TestFetchRevision_FeatureBranchRemoteErrorIsOrdinary(t *testing.T) {
	orig := runnerFetchCommand
	t.Cleanup(func() { runnerFetchCommand = orig })
	runnerFetchCommand = scriptedFeatureBranchSeamT(t, func() string { return "deadbeef" }, 128, nil)

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", featureBranch: "feature-x", nixSystem: "x86_64-linux", env: os.Environ()})

	_, err := r.fetchRevision(context.Background())
	if err == nil {
		t.Fatal("fetchRevision() error = nil, want an ordinary ls-remote error")
	}
	var gone *daemon.FeatureBranchGoneError
	if errors.As(err, &gone) {
		t.Fatalf("fetchRevision() error = %v, want NOT errors.As *daemon.FeatureBranchGoneError (exit 128 is a remote error, not a confirmed absence)", err)
	}
}

// TestFetchRevision_FeatureBranchExistsSucceeds pins the happy path: exit 0
// from ls-remote lets fetchRevision return the resolved revision same as if
// --feature-branch were never set.
func TestFetchRevision_FeatureBranchExistsSucceeds(t *testing.T) {
	orig := runnerFetchCommand
	t.Cleanup(func() { runnerFetchCommand = orig })
	var lsRemoteCalls int32
	runnerFetchCommand = scriptedFeatureBranchSeamT(t, func() string { return "deadbeef" }, 0, func() { atomic.AddInt32(&lsRemoteCalls, 1) })

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", featureBranch: "feature-x", nixSystem: "x86_64-linux", env: os.Environ()})

	revision, err := r.fetchRevision(context.Background())
	if err != nil {
		t.Fatalf("fetchRevision() unexpected error: %v", err)
	}
	if revision != "deadbeef" {
		t.Errorf("fetchRevision() = %q, want %q", revision, "deadbeef")
	}
	if got := atomic.LoadInt32(&lsRemoteCalls); got != 1 {
		t.Errorf("ls-remote calls = %d, want 1", got)
	}
}

// TestFetchRevision_NoFeatureBranchSkipsLsRemote pins that the existence
// check only ever runs when --feature-branch is set: with it empty, no
// ls-remote invocation happens at all.
func TestFetchRevision_NoFeatureBranchSkipsLsRemote(t *testing.T) {
	orig := runnerFetchCommand
	t.Cleanup(func() { runnerFetchCommand = orig })
	var lsRemoteCalls int32
	runnerFetchCommand = scriptedFeatureBranchSeamT(t, func() string { return "deadbeef" }, 0, func() { atomic.AddInt32(&lsRemoteCalls, 1) })

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", nixSystem: "x86_64-linux", env: os.Environ()})

	if _, err := r.fetchRevision(context.Background()); err != nil {
		t.Fatalf("fetchRevision() unexpected error: %v", err)
	}
	if got := atomic.LoadInt32(&lsRemoteCalls); got != 0 {
		t.Errorf("ls-remote calls = %d, want 0 (featureBranch unset)", got)
	}
}

// TestFetchRevision_RealGitMissingFeatureBranchReturnsTypedError pins real
// `git ls-remote --exit-code` behaviour on a missing ref; the other
// feature-branch tests script its exit code through runnerFetchCommand.
func TestFetchRevision_RealGitMissingFeatureBranchReturnsTypedError(t *testing.T) {
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
	// No feature-x branch is ever created or pushed: origin only ever knows
	// about main, so ls-remote's "no matching ref" case is genuine, not
	// stubbed.

	r := mustHostRunner(t, hostRunnerConfig{repoPath: dirConsumer, appAttr: ".#", baseBranch: "main", featureBranch: "feature-x", nixSystem: "x86_64-linux", env: os.Environ()})

	_, err := r.fetchRevision(context.Background())
	if err == nil {
		t.Fatal("fetchRevision() error = nil, want a FeatureBranchGoneError")
	}
	var gone *daemon.FeatureBranchGoneError
	if !errors.As(err, &gone) {
		t.Fatalf("fetchRevision() error = %v, want errors.As *daemon.FeatureBranchGoneError", err)
	}
	if gone.Branch != "feature-x" {
		t.Errorf("gone.Branch = %q, want %q", gone.Branch, "feature-x")
	}
	if gone.Remote != "origin" {
		t.Errorf("gone.Remote = %q, want %q", gone.Remote, "origin")
	}
}

// TestResolveTip_FeatureBranchConcurrentCallersShareSingleLsRemote asserts
// three coalesced callers cost one ls-remote, which rides the fetch's flight.
func TestResolveTip_FeatureBranchConcurrentCallersShareSingleLsRemote(t *testing.T) {
	origFetch, origJoined := runnerFetchCommand, runnerFlightJoined
	t.Cleanup(func() { runnerFetchCommand, runnerFlightJoined = origFetch, origJoined })

	var lsRemoteCalls int32
	gate := make(chan struct{})
	leaderBlocked := make(chan struct{})
	joins := make(chan struct{}, 2)

	runnerFetchCommand = scriptedFeatureBranchSeamT(t, func() string { return "deadbeef" }, 0, func() {
		atomic.AddInt32(&lsRemoteCalls, 1)
		close(leaderBlocked)
		<-gate
	})
	runnerFlightJoined = func() { joins <- struct{}{} }

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", featureBranch: "feature-x", nixSystem: "x86_64-linux", env: os.Environ()})

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

	if got := atomic.LoadInt32(&lsRemoteCalls); got != 1 {
		t.Errorf("ls-remote calls = %d, want 1", got)
	}
	for i, err := range errs {
		if err != nil {
			t.Errorf("caller %d: ResolveTip() error: %v", i, err)
		}
	}
}

// stderrExitCmd stubs a child that prints msg to stderr and exits with code.
func stderrExitCmd(ctx context.Context, msg string, code int) *exec.Cmd {
	return exec.CommandContext(ctx, "/bin/sh", "-c", fmt.Sprintf(`printf '%%s\n' "$1" >&2; exit %d`, code), "sh", msg)
}

// selfEvalErrorT drives ResolveTip against a self eval that fails with the
// given stderr and returns the resulting *daemon.SelfEvalError.
func selfEvalErrorT(t *testing.T, stderr string) *daemon.SelfEvalError {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	orig := runnerEvalCommand
	t.Cleanup(func() { runnerEvalCommand = orig })
	runnerEvalCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return stderrExitCmd(ctx, stderr, 1)
	}

	dirConsumer := bareOriginConsumerT(t)
	r := mustHostRunner(t, hostRunnerConfig{repoPath: dirConsumer, appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	_, err := r.ResolveTip(context.Background())
	var se *daemon.SelfEvalError
	if !errors.As(err, &se) {
		t.Fatalf("ResolveTip() error = %v, want a *daemon.SelfEvalError", err)
	}
	return se
}

// credURLStderr is synthetic child stderr carrying URL userinfo, as git
// prints when a remote URL embeds a token.
const credURLStderr = "fatal: unable to access 'https://user:pass@host.example/repo.git/': Could not resolve host"

func assertCredentialsRedacted(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("error = nil, want a failure carrying stderr")
	}
	msg := err.Error()
	if strings.Contains(msg, "user:pass") || strings.Contains(msg, "pass@") {
		t.Errorf("error = %q, want URL credentials redacted", msg)
	}
	if !strings.Contains(msg, "host.example/repo.git") || !strings.Contains(msg, "Could not resolve host") {
		t.Errorf("error = %q, want the rest of stderr preserved", msg)
	}
}

func TestFetchRevision_FetchErrorRedactsURLCredentials(t *testing.T) {
	orig := runnerFetchCommand
	t.Cleanup(func() { runnerFetchCommand = orig })
	runnerFetchCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return stderrExitCmd(ctx, credURLStderr, 128)
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", nixSystem: "x86_64-linux", env: os.Environ()})
	_, err := r.fetchRevision(context.Background())
	assertCredentialsRedacted(t, err)
}

func TestFetchRevision_LsRemoteErrorRedactsURLCredentials(t *testing.T) {
	orig := runnerFetchCommand
	t.Cleanup(func() { runnerFetchCommand = orig })
	runnerFetchCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if len(args) >= 3 && args[2] == "ls-remote" {
			return stderrExitCmd(ctx, credURLStderr, 128)
		}
		return exec.CommandContext(ctx, "/bin/sh", "-c", `echo deadbeef`)
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", featureBranch: "feature-x", nixSystem: "x86_64-linux", env: os.Environ()})
	_, err := r.fetchRevision(context.Background())
	assertCredentialsRedacted(t, err)
}

func TestResolveTip_SelfPathEvalFailureRedactsURLCredentials(t *testing.T) {
	assertCredentialsRedacted(t, selfEvalErrorT(t, credURLStderr))
}

// TestFetchRevision_RealGitFetchErrorRedactsURLCredentials runs real git
// against a credential-bearing remote URL whose host is rejected locally
// (.invalid, bad hostname), so no network is touched. git stops at the first
// '@' in its diagnostic, so the password's tail ("ss@") is what could leak.
func TestFetchRevision_RealGitFetchErrorRedactsURLCredentials(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	gitRunT(t, "", "init", repo)
	gitRunT(t, repo, "remote", "add", "origin", "https://alice:p@ss@nonexistent.invalid/o/r.git")

	r := mustHostRunner(t, hostRunnerConfig{repoPath: repo, appAttr: ".#", baseBranch: "main", nixSystem: "x86_64-linux", env: os.Environ()})
	_, err := r.fetchRevision(context.Background())
	if err == nil {
		t.Fatal("fetchRevision() error = nil, want a fetch failure")
	}
	if strings.Contains(err.Error(), "ss@") || strings.Contains(err.Error(), "alice") {
		t.Errorf("fetchRevision() error = %q, want URL credentials redacted", err.Error())
	}
	if !strings.Contains(err.Error(), "nonexistent.invalid/o/r.git") {
		t.Errorf("fetchRevision() error = %q, want git's diagnostic preserved", err.Error())
	}
}

// TestRunDoctor_ResearchAppendsFlag asserts a "--research" doctor flag reaches
// the preflight doctor argv, and its absence omits it.
func TestRunDoctor_ResearchAppendsFlag(t *testing.T) {
	orig := runnerDoctorCommand
	t.Cleanup(func() { runnerDoctorCommand = orig })

	var gotArgs []string
	runnerDoctorCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		gotArgs = append([]string(nil), args...)
		return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 0")
	}

	revision := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	for _, withFlag := range []bool{true, false} {
		var flags []string
		if withFlag {
			flags = []string{"--research"}
		}
		r := mustHostRunner(t, hostRunnerConfig{repoPath: "/repo", appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ(), doctorFlags: flags})
		if _, _, err := r.RunDoctor(context.Background(), revision); err != nil {
			t.Fatalf("RunDoctor() unexpected error: %v", err)
		}
		if got := slices.Contains(gotArgs, "--research"); got != withFlag {
			t.Errorf("withFlag=%v: argv %v, --research present = %v", withFlag, gotArgs, got)
		}
	}
}

// TestRunDoctor_CapturesRequiredLabelsLine asserts the doctor line carrying
// doctor.ErrRequiredLabelsMissing comes back beside exit 4, so the halt can
// name the missing labels.
func TestRunDoctor_CapturesRequiredLabelsLine(t *testing.T) {
	orig := runnerDoctorCommand
	t.Cleanup(func() { runnerDoctorCommand = orig })
	want := doctor.ErrRequiredLabelsMissing.Error() + ": agent-butler-finding missing — create them in the repository"
	runnerDoctorCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", "-c", "echo noise; echo '"+want+"' >&2; echo more; exit 4")
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	exit, line, err := r.RunDoctor(context.Background(), "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	if err != nil {
		t.Fatalf("RunDoctor() unexpected error: %v", err)
	}
	if exit != 4 || line != want {
		t.Errorf("RunDoctor() = (%d, %q), want (4, %q)", exit, line, want)
	}
}

// prebuildHarness scripts the three commands Prebuild runs: the drv eval
// (runnerEvalCommand), `nix build` and the smoke test (both through
// runnerPrebuildCommand, told apart by the program name). Each script gets the
// real argv as "$@"; the two that the tests assert on append it to a file.
type prebuildHarness struct {
	r        *hostRunner
	stateDir string
	buildLog string
	smokeLog string
}

func newPrebuildHarness(t *testing.T, evalScript, buildScript, smokeScript string) *prebuildHarness {
	t.Helper()
	origEval, origBuild := runnerEvalCommand, runnerPrebuildCommand
	t.Cleanup(func() { runnerEvalCommand, runnerPrebuildCommand = origEval, origBuild })

	h := &prebuildHarness{stateDir: t.TempDir()}
	h.buildLog = filepath.Join(h.stateDir, "build.argv")
	h.smokeLog = filepath.Join(h.stateDir, "smoke.argv")
	runnerEvalCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", append([]string{"-c", evalScript, "sh"}, args...)...)
	}
	runnerPrebuildCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		script, log := smokeScript, h.smokeLog
		if name == "nix" {
			script, log = buildScript, h.buildLog
		}
		return exec.CommandContext(ctx, "/bin/sh", append([]string{"-c", `printf '%s\n' "$@" >` + log + "\n" + script, "sh"}, args...)...)
	}
	h.r = mustHostRunner(t, hostRunnerConfig{
		repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux",
		env: os.Environ(), stateDir: h.stateDir,
		selfArgs: []string{"--feature-branch", "feat", "dispatch"},
	})
	return h
}

func (h *prebuildHarness) read(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

const prebuildSelfPath = "/nix/store/q75-spindrift-daemon/bin/spindrift-daemon"

func TestPrebuild_Success(t *testing.T) {
	h := newPrebuildHarness(t,
		"echo /nix/store/il4-spindrift-daemon.drv",
		"echo /nix/store/q75-spindrift-daemon",
		"echo 'daemon: check-only: input and config are valid' >&2")

	if err := h.r.Prebuild(context.Background(), daemon.Tip{Revision: "abc", SelfPath: prebuildSelfPath}); err != nil {
		t.Fatalf("Prebuild() = %v, want nil", err)
	}

	wantBuild := daemon.SelfBuildCommand("/nix/store/il4-spindrift-daemon.drv", filepath.Join(h.stateDir, prebuildGCRoot))[1:]
	if got := h.read(t, h.buildLog); !reflect.DeepEqual(got, wantBuild) {
		t.Errorf("nix build argv = %v, want %v", got, wantBuild)
	}
	wantSmoke := []string{"--feature-branch", "feat", "dispatch", "--check-only"}
	if got := h.read(t, h.smokeLog); !reflect.DeepEqual(got, wantSmoke) {
		t.Errorf("smoke-test argv = %v, want %v (no --input: the successor uses its own)", got, wantSmoke)
	}
}

func TestRootSelf_RootsTheRunningBuildAndDropsTheCandidateRoot(t *testing.T) {
	h := newPrebuildHarness(t, "", "", "")
	next := filepath.Join(h.stateDir, prebuildGCRoot)
	if err := os.Symlink("/nix/store/bbb-new", next); err != nil {
		t.Fatal(err)
	}
	var warn bytes.Buffer
	h.r.rootSelf(context.Background(), "/nix/store/aaa-old/bin/daemon", &warn)

	want := daemon.SelfRootCommand("/nix/store/aaa-old", filepath.Join(h.stateDir, currentGCRoot))[1:]
	if got := h.read(t, h.buildLog); !reflect.DeepEqual(got, want) {
		t.Errorf("nix argv = %v, want %v", got, want)
	}
	if _, err := os.Lstat(next); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("candidate root still present (err %v), want it removed", err)
	}
	if warn.Len() != 0 {
		t.Errorf("warning = %q, want none", warn.String())
	}
}

func TestRootSelf_FailureOnlyWarnsAndKeepsTheCandidateRoot(t *testing.T) {
	h := newPrebuildHarness(t, "", "echo boom >&2; exit 1", "")
	next := filepath.Join(h.stateDir, prebuildGCRoot)
	if err := os.Symlink("/nix/store/bbb-new", next); err != nil {
		t.Fatal(err)
	}
	var warn bytes.Buffer
	h.r.rootSelf(context.Background(), "/nix/store/bbb-new/bin/daemon", &warn)

	if !strings.Contains(warn.String(), "/nix/store/bbb-new") || !strings.Contains(warn.String(), "boom") {
		t.Errorf("warning = %q, want it to name the path and the nix failure", warn.String())
	}
	if _, err := os.Lstat(next); err != nil {
		t.Errorf("candidate root = %v, want it kept: it may be the only root of the running build", err)
	}
}

func TestRootSelf_OutsideTheStoreRootsNothing(t *testing.T) {
	for _, program := range []string{"", "/usr/bin/daemon", "/nix/store/a/../b/bin/d", "/nix/store/"} {
		h := newPrebuildHarness(t, "", "", "")
		var warn bytes.Buffer
		h.r.rootSelf(context.Background(), program, &warn)
		if _, err := os.Stat(h.buildLog); err == nil {
			t.Errorf("program %q: nix ran, want no rooting", program)
		}
		if warn.Len() != 0 {
			t.Errorf("program %q: warning = %q, want none", program, warn.String())
		}
	}
}

func TestPrebuild_Failures(t *testing.T) {
	cases := []struct {
		name                       string
		eval, build, smoke         string
		wantStage                  daemon.PrebuildStage
		wantInErr                  string
		wantSmokeRan, wantBuildRan bool
	}{
		{
			name:      "eval fails",
			eval:      "echo 'eval boom' >&2; exit 1",
			build:     "echo /nix/store/q75-spindrift-daemon",
			smoke:     "exit 0",
			wantStage: daemon.PrebuildStageBuild, wantInErr: "eval boom",
		},
		{
			name:      "eval prints nothing",
			eval:      "exit 0",
			build:     "echo /nix/store/q75-spindrift-daemon",
			smoke:     "exit 0",
			wantStage: daemon.PrebuildStageBuild,
		},
		{
			name:      "nix build fails",
			eval:      "echo /nix/store/il4-spindrift-daemon.drv",
			build:     "echo 'build boom' >&2; exit 1",
			smoke:     "exit 0",
			wantStage: daemon.PrebuildStageBuild, wantInErr: "build boom", wantBuildRan: true,
		},
		{
			name:      "out path does not hold the program",
			eval:      "echo /nix/store/il4-spindrift-daemon.drv",
			build:     "echo /nix/store/zzz-other",
			smoke:     "exit 0",
			wantStage: daemon.PrebuildStageBuild, wantInErr: "/nix/store/zzz-other", wantBuildRan: true,
		},
		{
			name:      "smoke test fails",
			eval:      "echo /nix/store/il4-spindrift-daemon.drv",
			build:     "echo /nix/store/q75-spindrift-daemon",
			smoke:     "echo 'daemon: bad config' >&2; exit 1",
			wantStage: daemon.PrebuildStageSmokeTest, wantInErr: "daemon: bad config", wantBuildRan: true, wantSmokeRan: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newPrebuildHarness(t, tc.eval, tc.build, tc.smoke)
			err := h.r.Prebuild(context.Background(), daemon.Tip{Revision: "abc", SelfPath: prebuildSelfPath})
			var pe *daemon.PrebuildError
			if !errors.As(err, &pe) {
				t.Fatalf("Prebuild() = %v, want a *daemon.PrebuildError", err)
			}
			if pe.Stage != tc.wantStage {
				t.Errorf("Stage = %q, want %q (err: %v)", pe.Stage, tc.wantStage, err)
			}
			if !strings.Contains(err.Error(), tc.wantInErr) {
				t.Errorf("err = %q, want it to contain %q", err, tc.wantInErr)
			}
			for _, c := range []struct {
				name string
				path string
				want bool
			}{{"nix build", h.buildLog, tc.wantBuildRan}, {"smoke test", h.smokeLog, tc.wantSmokeRan}} {
				if _, statErr := os.Stat(c.path); (statErr == nil) != c.want {
					t.Errorf("%s ran = %v, want %v", c.name, statErr == nil, c.want)
				}
			}
		})
	}
}

func TestPrebuild_SmokeTestHangTimesOut(t *testing.T) {
	orig := prebuildSmokeTimeout
	prebuildSmokeTimeout = 100 * time.Millisecond
	t.Cleanup(func() { prebuildSmokeTimeout = orig })

	h := newPrebuildHarness(t,
		"echo /nix/store/il4-spindrift-daemon.drv",
		"echo /nix/store/q75-spindrift-daemon",
		"exec sleep 30")

	start := time.Now()
	err := h.r.Prebuild(context.Background(), daemon.Tip{Revision: "abc", SelfPath: prebuildSelfPath})
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("Prebuild took %v, want it bounded by the smoke timeout", elapsed)
	}
	var pe *daemon.PrebuildError
	if !errors.As(err, &pe) || pe.Stage != daemon.PrebuildStageSmokeTest {
		t.Fatalf("Prebuild() = %v, want a smoke-test *daemon.PrebuildError", err)
	}
	if !strings.Contains(err.Error(), "timed out after") {
		t.Errorf("err = %q, want it to say the smoke test timed out", err)
	}
}

func TestPrebuild_BuildStepHangTimesOut(t *testing.T) {
	for _, tc := range []struct {
		name, eval, build string
	}{
		{"eval", "exec sleep 30", "echo /nix/store/q75-spindrift-daemon"},
		{"build", "echo /nix/store/il4-spindrift-daemon.drv", "exec sleep 30"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			orig := prebuildBuildTimeout
			prebuildBuildTimeout = 100 * time.Millisecond
			t.Cleanup(func() { prebuildBuildTimeout = orig })

			h := newPrebuildHarness(t, tc.eval, tc.build, "true")

			start := time.Now()
			err := h.r.Prebuild(context.Background(), daemon.Tip{Revision: "abc", SelfPath: prebuildSelfPath})
			if elapsed := time.Since(start); elapsed > 10*time.Second {
				t.Errorf("Prebuild took %v, want it bounded by the build timeout", elapsed)
			}
			var pe *daemon.PrebuildError
			if !errors.As(err, &pe) || pe.Stage != daemon.PrebuildStageBuild {
				t.Fatalf("Prebuild() = %v, want a build-stage *daemon.PrebuildError", err)
			}
			if !strings.Contains(err.Error(), "timed out after") {
				t.Errorf("err = %q, want it to say the build timed out", err)
			}
		})
	}
}

// TestPrebuild_BuildTimeoutSpansEvalAndBuild pins one bound over both steps:
// each sleep fits the timeout alone, but not together.
func TestPrebuild_BuildTimeoutSpansEvalAndBuild(t *testing.T) {
	orig := prebuildBuildTimeout
	prebuildBuildTimeout = time.Second
	t.Cleanup(func() { prebuildBuildTimeout = orig })

	h := newPrebuildHarness(t,
		"sleep 0.6; echo /nix/store/il4-spindrift-daemon.drv",
		"sleep 0.6; echo /nix/store/q75-spindrift-daemon",
		"true")

	err := h.r.Prebuild(context.Background(), daemon.Tip{Revision: "abc", SelfPath: prebuildSelfPath})
	var pe *daemon.PrebuildError
	if !errors.As(err, &pe) || pe.Stage != daemon.PrebuildStageBuild {
		t.Fatalf("Prebuild() = %v, want a build-stage *daemon.PrebuildError", err)
	}
	if !strings.Contains(err.Error(), "nix build") || !strings.Contains(err.Error(), "timed out after") {
		t.Errorf("err = %q, want nix build to time out on the shared bound", err)
	}
}

func TestPrebuild_StopDuringBuildIsNotATimeout(t *testing.T) {
	for _, tc := range []struct {
		name string
		ctx  func() (context.Context, context.CancelFunc)
	}{
		{"cancel", func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			time.AfterFunc(100*time.Millisecond, cancel)
			return ctx, cancel
		}},
		// The caller's own deadline expiring is DeadlineExceeded on buildCtx
		// too; only the parent check keeps it from reading as the build bound.
		{"parent deadline", func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 100*time.Millisecond)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newPrebuildHarness(t,
				"echo /nix/store/il4-spindrift-daemon.drv",
				"exec sleep 30",
				"true")

			ctx, cancel := tc.ctx()
			defer cancel()
			err := h.r.Prebuild(ctx, daemon.Tip{Revision: "abc", SelfPath: prebuildSelfPath})
			var pe *daemon.PrebuildError
			if !errors.As(err, &pe) || pe.Stage != daemon.PrebuildStageBuild {
				t.Fatalf("Prebuild() = %v, want a build-stage *daemon.PrebuildError", err)
			}
			if strings.Contains(err.Error(), "timed out after") {
				t.Errorf("err = %q, want a stop not reported as a timeout", err)
			}
		})
	}
}

func TestPrebuildStderr_CutsOnRuneBoundary(t *testing.T) {
	// "é" is two bytes and starts at odd offsets here, so a cut at an even
	// offset lands mid-character.
	var b bytes.Buffer
	b.WriteString("x" + strings.Repeat("é", prebuildStderrTail) + "y")
	got := prebuildStderr(&b)
	if !utf8.ValidString(got) {
		t.Errorf("prebuildStderr() = %q, want valid UTF-8", got)
	}
	if !strings.HasPrefix(got, "...") || len(got) > prebuildStderrTail+len("...") {
		t.Errorf("prebuildStderr() has len %d, want a ... prefix and at most %d tail bytes", len(got), prebuildStderrTail)
	}
}

// startAndWait is StartChild then Wait(nil): the whole run of one child, for
// tests that assert on its outcome rather than on the start/wait split.
func startAndWait(ctx context.Context, r *hostRunner, req daemon.ChildRequest) (daemon.ChildResult, error) {
	h, err := r.StartChild(ctx, req)
	if err != nil {
		return daemon.ChildResult{}, err
	}
	return h.Wait(nil)
}

// doneHandle is a child handle that has already finished with a zero result.
type doneHandle struct{}

func (doneHandle) Wait(<-chan struct{}) (daemon.ChildResult, error) { return daemon.ChildResult{}, nil }
func (doneHandle) State() daemon.ChildState                         { return daemon.ChildState{} }

// TestStartChild_AbandonedWaitLeavesChildRunning pins ADR 0063's contract:
// abandoning a Wait signals, reaps and closes nothing, and a later Wait
// resumes cleanly, even mid report line.
func TestStartChild_AbandonedWaitLeavesChildRunning(t *testing.T) {
	orig := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = orig })

	dir := t.TempDir()
	marker, goFile := filepath.Join(dir, "marker"), filepath.Join(dir, "go")
	script := fmt.Sprintf(`trap 'echo signalled >%[1]q; exit 9' TERM INT
printf '%%s\n%%s' '{"event":"box","issue":"101","phase":"initial"}' '{"event":"settled","issue":"101",' >&3
while [ ! -e %[2]q ]; do sleep 0.05; done
printf '%%s\n' '"state":"merged"}' >&3
exit 3`, marker, goFile)
	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", script)
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})

	var mu sync.Mutex
	var got []daemon.Record
	firstRecord := make(chan struct{})
	stop := make(chan struct{})
	req := daemon.ChildRequest{
		Slot: 0, Kind: daemon.KindOf(dispatchkind.Work), Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		Stop: stop,
		OnRecord: func(rec daemon.Record) {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, rec)
			if len(got) == 1 {
				close(firstRecord)
			}
		},
	}
	handle, err := r.StartChild(context.Background(), req)
	if err != nil {
		t.Fatalf("StartChild() unexpected error: %v", err)
	}
	pid := handle.(*hostChildHandle).proc.Pid
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	abandon := make(chan struct{})
	waited := make(chan error, 1)
	go func() {
		_, err := handle.Wait(abandon)
		waited <- err
	}()
	waitFor := func(what string, c <-chan struct{}) {
		t.Helper()
		select {
		case <-c:
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for %s", what)
		}
	}
	waitFor("first record", firstRecord)
	// The record and the half line leave the child in one pipe write (atomic
	// under PIPE_BUF) and the reader takes both in one read, so once the record
	// is delivered the fragment is already held by the Wait's reader.
	close(abandon)
	select {
	case err := <-waited:
		if !errors.Is(err, daemon.ErrWaitAbandoned) {
			t.Fatalf("abandoned Wait error = %v, want ErrWaitAbandoned", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("abandoned Wait did not return")
	}

	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("child not alive after abandon: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("child was signalled by an abandoned Wait")
	}

	if err := os.WriteFile(goFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := handle.Wait(nil)
	if err != nil {
		t.Fatalf("resumed Wait() unexpected error: %v", err)
	}
	if result.Exit != 3 {
		t.Errorf("resumed Exit = %d, want 3", result.Exit)
	}
	want := []daemon.Record{
		{Event: "box", Key: dispatchkey.Issue("101"), Phase: "initial"},
		{Event: "settled", Key: dispatchkey.Issue("101"), State: "merged"},
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("OnRecord calls = %+v, want %+v", got, want)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("child was signalled during the resumed Wait")
	}
}

// TestStartChild_AbandonedWaitDoesNotResendSIGTERM pins that a Wait resumed
// after an abandon does not forward a stop it already forwarded: the child's
// stopsignal.Relay counts deliveries, not kinds, so a second SIGTERM would
// escalate a drain to an abort (issue #3538).
func TestStartChild_AbandonedWaitDoesNotResendSIGTERM(t *testing.T) {
	origExec, origSignal := runnerExecCommand, runnerSignal
	t.Cleanup(func() { runnerExecCommand = origExec; runnerSignal = origSignal })

	dir := t.TempDir()
	goFile, ready := filepath.Join(dir, "go"), filepath.Join(dir, "ready")
	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", fmt.Sprintf("trap ':' TERM INT; touch %q; while [ ! -e %q ]; do sleep 0.05; done", ready, goFile))
	}
	var mu sync.Mutex
	var sigs []os.Signal
	runnerSignal = func(p *os.Process, sig os.Signal) error {
		mu.Lock()
		sigs = append(sigs, sig)
		mu.Unlock()
		return p.Signal(sig)
	}
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(sigs)
	}
	waitCount := func(n int, within time.Duration) bool {
		deadline := time.Now().Add(within)
		for time.Now().Before(deadline) {
			if count() >= n {
				return true
			}
			time.Sleep(10 * time.Millisecond)
		}
		return count() >= n
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	stop := make(chan struct{})
	handle, err := r.StartChild(context.Background(), daemon.ChildRequest{Slot: 0, Kind: daemon.KindOf(dispatchkind.Work), Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", Stop: stop})
	if err != nil {
		t.Fatalf("StartChild() unexpected error: %v", err)
	}
	pid := handle.(*hostChildHandle).proc.Pid
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	abandon := make(chan struct{})
	waited := make(chan error, 1)
	go func() {
		_, err := handle.Wait(abandon)
		waited <- err
	}()
	waitForArmed(t, ready)
	close(stop)
	if !waitCount(1, 5*time.Second) {
		t.Fatal("stop closed but no SIGTERM was forwarded")
	}
	close(abandon)
	select {
	case err := <-waited:
		if !errors.Is(err, daemon.ErrWaitAbandoned) {
			t.Fatalf("abandoned Wait error = %v, want ErrWaitAbandoned", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("abandoned Wait did not return")
	}

	type outcome struct {
		res daemon.ChildResult
		err error
	}
	resumed := make(chan outcome, 1)
	go func() {
		res, err := handle.Wait(nil)
		resumed <- outcome{res, err}
	}()
	// A negative check has no event to wait on; the window is how long a
	// resent signal would have to show up in.
	if waitCount(2, 300*time.Millisecond) {
		t.Fatalf("resumed Wait forwarded %d signals, want the one SIGTERM only", count())
	}
	if err := os.WriteFile(goFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case o := <-resumed:
		if o.err != nil || o.res.Exit != 0 {
			t.Fatalf("resumed Wait = (%+v, %v), want clean exit", o.res, o.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("resumed Wait did not return")
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(sigs, []os.Signal{syscall.SIGTERM}) {
		t.Errorf("signals = %v, want exactly [SIGTERM]", sigs)
	}
}

// TestReadReports_DiscardContinuesAcrossDeadline pins that a read deadline
// landing inside an over-long line leaves the reader still discarding: the
// line's tail, even when it looks like a record, is not parsed on resume.
func TestReadReports_DiscardContinuesAcrossDeadline(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pr.Close(); _ = pw.Close() })

	var got []daemon.Record
	onRecord := func(rec daemon.Record) { got = append(got, rec) }
	kind := daemon.KindOf(dispatchkind.Work)
	var st reportState

	// Written from a goroutine so the write never gates the read.
	go func() { _, _ = pw.Write(bytes.Repeat([]byte("x"), 2*report.MaxLine)) }()
	_ = pr.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if err := readReports(pr, kind, onRecord, &st); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("first readReports error = %v, want deadline", err)
	}

	_ = pr.SetReadDeadline(time.Time{})
	// The first line is the over-long line's tail; the second is a real record.
	_, _ = pw.WriteString(`{"event":"box","issue":"102","phase":"initial"}` + "\n")
	_, _ = pw.WriteString(`{"event":"box","issue":"103","phase":"initial"}` + "\n")
	_ = pw.Close()
	if err := readReports(pr, kind, onRecord, &st); !errors.Is(err, io.EOF) {
		t.Fatalf("resumed readReports error = %v, want EOF", err)
	}
	want := []daemon.Record{{Event: "box", Key: dispatchkey.Issue("103"), Phase: "initial"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("records = %+v, want %+v (the over-long line's tail must be discarded)", got, want)
	}
}

// TestStartChild_AlreadyAbandonedWaitReturnsAtOnce covers an abandon channel
// that is closed before Wait starts: the child is left untouched.
func TestStartChild_AlreadyAbandonedWaitReturnsAtOnce(t *testing.T) {
	orig := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = orig })
	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", "sleep 30")
	}
	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	handle, err := r.StartChild(context.Background(), daemon.ChildRequest{Slot: 0, Kind: daemon.KindOf(dispatchkind.Work), Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"})
	if err != nil {
		t.Fatalf("StartChild() unexpected error: %v", err)
	}
	pid := handle.(*hostChildHandle).proc.Pid
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	abandon := make(chan struct{})
	close(abandon)
	if _, err := handle.Wait(abandon); !errors.Is(err, daemon.ErrWaitAbandoned) {
		t.Fatalf("Wait(closed abandon) error = %v, want ErrWaitAbandoned", err)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Errorf("child not alive after abandon: %v", err)
	}
}

// startAbandonedChild starts a /bin/sh child that writes one complete record
// and then half a line to its report pipe and idles, and returns its handle
// after a Wait on it was abandoned with both read.
func startAbandonedChild(t *testing.T, r *hostRunner) *hostChildHandle {
	t.Helper()
	return startAbandonedScript(t, r, `{"event":"box","issue":"101","phase":"initial"}`+"\n"+`{"event":"settled","issue":"101",`,
		`while :; do sleep 0.05; done`)
}

// startAbandonedScript runs a child (report pipe on fd 3) that writes initial
// and then runs script, and returns its handle once a Wait on it was abandoned
// after the first record. initial goes out as one write (cat of a file): sh
// line-buffers printf, so a record and a trailing fragment would be two
// writes, and the abandon could land before the fragment was read.
func startAbandonedScript(t *testing.T, r *hostRunner, initial, script string) *hostChildHandle {
	t.Helper()
	initialFile := filepath.Join(t.TempDir(), "initial")
	if err := os.WriteFile(initialFile, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	script = fmt.Sprintf("cat %s >&3\n%s", initialFile, script)
	orig := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = orig })
	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", script)
	}
	firstRecord := make(chan struct{})
	var once sync.Once
	handle, err := r.StartChild(context.Background(), daemon.ChildRequest{
		Slot: 0, Kind: daemon.KindOf(dispatchkind.Work), Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		OnRecord: func(daemon.Record) { once.Do(func() { close(firstRecord) }) },
	})
	if err != nil {
		t.Fatalf("StartChild() unexpected error: %v", err)
	}
	h := handle.(*hostChildHandle)
	t.Cleanup(func() { _ = h.proc.Kill(); _, _ = h.proc.Wait() })

	abandon := make(chan struct{})
	waited := make(chan error, 1)
	go func() {
		_, err := h.Wait(abandon)
		waited <- err
	}()
	select {
	case <-firstRecord:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the first record")
	}
	close(abandon)
	select {
	case err := <-waited:
		if !errors.Is(err, daemon.ErrWaitAbandoned) {
			t.Fatalf("abandoned Wait error = %v, want ErrWaitAbandoned", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("abandoned Wait did not return")
	}
	return h
}

// fdOf is fileDescriptor for a test.
func fdOf(t *testing.T, f *os.File) int {
	t.Helper()
	fd, err := fileDescriptor(f)
	if err != nil {
		t.Fatal(err)
	}
	return fd
}

// simulateExec models the exec between two daemon images inside one test
// process: the successor sees the report descriptor as a bare inheritable
// number with the old image's *os.File (and its epoll registration, which
// would make a second File on the same number unpollable) gone.
func simulateExec(t *testing.T, h *hostChildHandle, st daemon.ChildState) daemon.ChildState {
	t.Helper()
	fd, err := syscall.Dup(st.ReportFD)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.reportRead.Close(); err != nil {
		t.Fatal(err)
	}
	st.ReportFD = fd
	return st
}

// inheritableFDs lists this process's open descriptors lacking FD_CLOEXEC.
func inheritableFDs(t *testing.T) []int {
	t.Helper()
	d, err := os.Open("/proc/self/fd")
	if err != nil {
		t.Skipf("no /proc/self/fd: %v", err)
	}
	names, err := d.Readdirnames(-1)
	_ = d.Close() // closed first so its own descriptor reads as EBADF below and drops out
	if err != nil {
		t.Fatal(err)
	}
	var fds []int
	for _, n := range names {
		fd, err := strconv.Atoi(n)
		if err != nil {
			continue
		}
		if cloexec, err := isCloseOnExec(fd); err == nil && !cloexec {
			fds = append(fds, fd)
		}
	}
	slices.Sort(fds)
	return fds
}

// TestStartChild_StateAfterAbandonNamesChild pins what a handoff reads off an
// abandoned handle (ADR 0063): the child's pid, its report pipe's fd, and the
// report line the abandoned read had only half received.
func TestStartChild_StateAfterAbandonNamesChild(t *testing.T) {
	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	h := startAbandonedChild(t, r)

	st := h.State()
	if st.PID != h.proc.Pid {
		t.Errorf("State().PID = %d, want %d", st.PID, h.proc.Pid)
	}
	if want := fdOf(t, h.reportRead); st.ReportFD != want {
		t.Errorf("State().ReportFD = %d, want %d", st.ReportFD, want)
	}
	if want := `{"event":"settled","issue":"101",`; string(st.Partial) != want {
		t.Errorf("State().Partial = %q, want %q", st.Partial, want)
	}
	if st.Discarding {
		t.Error("State().Discarding = true, want false")
	}
}

// TestStartChild_StateWithClosedReportPipeKeepsFDSentinel pins that a report
// pipe whose descriptor cannot be read leaves ReportFD at -1: the handoff
// rejects a negative fd, while a 0 would pass as stdin.
func TestStartChild_StateWithClosedReportPipeKeepsFDSentinel(t *testing.T) {
	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	h := startAbandonedChild(t, r)
	if err := h.reportRead.Close(); err != nil {
		t.Fatal(err)
	}

	readStderr := captureStderr(t)
	if st := h.State(); st.ReportFD != -1 {
		t.Errorf("State().ReportFD = %d, want -1", st.ReportFD)
	}
	if got := string(readStderr()); !strings.Contains(got, "daemon: read report pipe fd:") {
		t.Errorf("stderr = %q, want the report pipe fd diagnostic", got)
	}
}

// TestInheritChild_ContinuesASplitLineOnASecondRunner hands a child with half
// a record read off to a second runner instance: the carried fragment is
// completed by the rest of the line, later records and the exit reach the
// second runner's callback, and an abandoned Wait on the inherited handle works.
func TestInheritChild_ContinuesASplitLineOnASecondRunner(t *testing.T) {
	cfg := hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()}
	goFile := filepath.Join(t.TempDir(), "go")
	first := startAbandonedScript(t, mustHostRunner(t, cfg), `{"event":"box","issue":"101","phase":"initial"}`+"\n"+`{"event":"box","issue":"101",`,
		fmt.Sprintf(`while [ ! -e %s ]; do sleep 0.05; done
printf '%%s\n%%s\n' '"phase":"fix-pass-1"}' '{"event":"box","issue":"101","phase":"fix-pass-2"}' >&3
exit 3`, goFile))
	st := simulateExec(t, first, first.State())

	var mu sync.Mutex
	var got []daemon.Record
	handle, err := mustHostRunner(t, cfg).InheritChild(context.Background(), daemon.ChildRequest{
		Kind: daemon.KindOf(dispatchkind.Work),
		OnRecord: func(r daemon.Record) {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, r)
		},
	}, st)
	if err != nil {
		t.Fatalf("InheritChild() unexpected error: %v", err)
	}

	// The child is still waiting on the go-file, so this Wait can only
	// return through the read deadline — which needs a pollable descriptor.
	abandon := make(chan struct{})
	close(abandon)
	if _, err := handle.Wait(abandon); !errors.Is(err, daemon.ErrWaitAbandoned) {
		t.Fatalf("Wait(closed abandon) error = %v, want ErrWaitAbandoned", err)
	}

	if err := os.WriteFile(goFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := handle.Wait(nil)
	if err != nil {
		t.Fatalf("Wait() unexpected error: %v", err)
	}
	if res.Exit != 3 {
		t.Errorf("Wait() exit = %d, want 3", res.Exit)
	}
	want := []daemon.Record{
		{Event: "box", Key: dispatchkey.Issue("101"), Phase: "fix-pass-1"},
		{Event: "box", Key: dispatchkey.Issue("101"), Phase: "fix-pass-2"},
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("second runner records = %+v, want %+v", got, want)
	}
}

// TestInheritChild_DropsAForgedOrStaleHandoff pins that an entry failing
// validation is refused with its reason and takes nothing over: the descriptor it
// names stays open (another entry may name it) but is close-on-exec again.
func TestInheritChild_DropsAForgedOrStaleHandoff(t *testing.T) {
	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	req := daemon.ChildRequest{Kind: daemon.KindOf(dispatchkind.Work)}

	live := exec.Command("sleep", "30")
	if err := live.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = live.Process.Kill(); _ = live.Wait() })
	reaped := exec.Command("/bin/sh", "-c", "exit 0")
	if err := reaped.Run(); err != nil {
		t.Fatal(err)
	}

	// inheritable returns a pipe's read end as a successor would find it:
	// open, with close-on-exec cleared by the previous image.
	inheritable := func(t *testing.T) (*os.File, int) {
		t.Helper()
		pr, pw, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = pr.Close(); _ = pw.Close() })
		fd := fdOf(t, pr)
		if err := setCloseOnExec(fd, false); err != nil {
			t.Fatal(err)
		}
		return pr, fd
	}

	t.Run("pid that is not a child", func(t *testing.T) {
		for name, pid := range map[string]int{"init": 1, "reaped": reaped.Process.Pid} {
			pr, fd := inheritable(t)
			h, err := r.InheritChild(context.Background(), req, daemon.ChildState{PID: pid, ReportFD: fd})
			if err == nil || !strings.Contains(err.Error(), "is not a child of this process") {
				t.Errorf("%s: InheritChild() = (%v, %v), want a not-a-child error", name, h, err)
			}
			if slices.Contains(inheritableFDs(t), fd) {
				t.Errorf("%s: fd %d still inheritable after a refused entry", name, fd)
			}
			if _, err := pr.Stat(); err != nil {
				t.Errorf("%s: report pipe unusable after a refused entry: %v", name, err)
			}
		}
	})

	t.Run("report fd is not a pipe", func(t *testing.T) {
		f, err := os.CreateTemp(t.TempDir(), "regular")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		fd := fdOf(t, f)
		if err := setCloseOnExec(fd, false); err != nil {
			t.Fatal(err)
		}
		h, err := r.InheritChild(context.Background(), req, daemon.ChildState{PID: live.Process.Pid, ReportFD: fd})
		if err == nil || !strings.Contains(err.Error(), "is not a pipe") {
			t.Errorf("InheritChild() = (%v, %v), want a not-a-pipe error", h, err)
		}
		if slices.Contains(inheritableFDs(t), fd) {
			t.Errorf("fd %d still inheritable after a refused entry", fd)
		}
		if _, err := f.Stat(); err != nil {
			t.Errorf("file unusable after a refused entry: %v", err)
		}
	})

	t.Run("standard stream", func(t *testing.T) {
		before := inheritableFDs(t)
		for fd := 0; fd <= 2; fd++ {
			h, err := r.InheritChild(context.Background(), req, daemon.ChildState{PID: live.Process.Pid, ReportFD: fd})
			if err == nil || !strings.Contains(err.Error(), "standard stream") {
				t.Errorf("fd %d: InheritChild() = (%v, %v), want a standard-stream error", fd, h, err)
			}
		}
		if got := inheritableFDs(t); !slices.Equal(got, before) {
			t.Errorf("inheritable descriptors = %v after refused standard streams, want %v", got, before)
		}
	})

	t.Run("closed descriptor", func(t *testing.T) {
		f, err := os.CreateTemp(t.TempDir(), "closed")
		if err != nil {
			t.Fatal(err)
		}
		fd := fdOf(t, f)
		_ = f.Close()
		h, err := r.InheritChild(context.Background(), req, daemon.ChildState{PID: live.Process.Pid, ReportFD: fd})
		if err == nil || !strings.Contains(err.Error(), "report fd") {
			t.Errorf("InheritChild() = (%v, %v), want a report-fd error", h, err)
		}
	})
}

// setSignalSettle overrides how long execSuccessor waits for a stop signal
// to land after ignoring them, for this test only.
func setSignalSettle(t testing.TB, d time.Duration) {
	t.Helper()
	orig := signalSettle
	t.Cleanup(func() { signalSettle = orig })
	signalSettle = d
}

// execReadyRunner is a runner whose last Prebuild and latest fetched tip
// agree on prebuildSelfPath, so execTarget accepts it until a test spoils one
// side. It has no settle wait unless the test sets one.
func execReadyRunner(t *testing.T, mut func(*hostRunnerConfig)) *hostRunner {
	t.Helper()
	setSignalSettle(t, 0)
	cfg := hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ(), selfArgs: []string{"dispatch"}}
	if mut != nil {
		mut(&cfg)
	}
	r := mustHostRunner(t, cfg)
	r.prebuilt = daemon.Tip{Revision: "abc", SelfPath: prebuildSelfPath}
	r.lastRevision, r.haveLastRevision = "abc", true
	r.memoRevision, r.memoPath, r.haveMemo = "abc", prebuildSelfPath, true
	return r
}

func TestExecTarget(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*hostRunner)
		cfg     func(*hostRunnerConfig)
		wantErr string
	}{
		{name: "accepted"},
		{name: "accepted with a feature branch", cfg: func(c *hostRunnerConfig) { c.featureBranch = "feat" }},
		{name: "no prebuild", mutate: func(r *hostRunner) { r.prebuilt = daemon.Tip{} }, wantErr: "no prebuilt"},
		{name: "outside the store", mutate: func(r *hostRunner) { r.prebuilt.SelfPath, r.memoPath = "/usr/bin/daemon", "/usr/bin/daemon" }, wantErr: "/nix/store/"},
		{name: "unclean path", mutate: func(r *hostRunner) {
			r.prebuilt.SelfPath = "/nix/store/q75/../zzz/bin/d"
			r.memoPath = r.prebuilt.SelfPath
		}, wantErr: "not clean"},
		{name: "tip moved on", mutate: func(r *hostRunner) { r.lastRevision = "def" }, wantErr: "latest fetched tip"},
		{name: "latest eval failed", mutate: func(r *hostRunner) { r.haveMemo = false }, wantErr: "latest fetched tip"},
		{name: "latest tip builds elsewhere", mutate: func(r *hostRunner) { r.memoPath = "/nix/store/other/bin/d" }, wantErr: "/nix/store/other/bin/d"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := execReadyRunner(t, tc.cfg)
			if tc.mutate != nil {
				tc.mutate(r)
			}
			tip, err := r.execTarget()
			if tc.wantErr == "" {
				if err != nil || tip.SelfPath != prebuildSelfPath {
					t.Fatalf("execTarget() = %+v, %v, want %s", tip, err, prebuildSelfPath)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("execTarget() error = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

// TestExecTarget_FeatureBranchNeverChoosesThePath drives a real ResolveTip with
// a feature branch whose tip would evaluate to a different daemon than the
// base tip's: only the base tip is ever evaluated, so a prebuild of the
// feature branch's path is refused.
func TestExecTarget_FeatureBranchNeverChoosesThePath(t *testing.T) {
	const (
		baseRev  = "1111111111111111111111111111111111111111"
		featRev  = "2222222222222222222222222222222222222222"
		baseSelf = "/nix/store/base-daemon/bin/daemon"
		featSelf = "/nix/store/feat-daemon/bin/daemon"
	)
	origFetch, origEval := runnerFetchCommand, runnerEvalCommand
	t.Cleanup(func() { runnerFetchCommand, runnerEvalCommand = origFetch, origEval })
	sh := func(ctx context.Context, script string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", "-c", script)
	}
	runnerFetchCommand = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		switch {
		case slices.Contains(args, "rev-parse"):
			return sh(ctx, "echo "+baseRev)
		case slices.Contains(args, "ls-remote"):
			return sh(ctx, "echo "+featRev+" refs/heads/feat")
		}
		return sh(ctx, "exit 0")
	}
	var evalArgs [][]string
	runnerEvalCommand = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		evalArgs = append(evalArgs, args)
		for _, a := range args {
			switch {
			case strings.Contains(a, baseRev):
				return sh(ctx, "echo "+baseSelf)
			case strings.Contains(a, featRev):
				return sh(ctx, "echo "+featSelf)
			}
		}
		return sh(ctx, "exit 1")
	}
	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", featureBranch: "feat", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ(), selfArgs: []string{"dispatch"}})
	tip, err := r.ResolveTip(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tip.Revision != baseRev || tip.SelfPath != baseSelf {
		t.Fatalf("ResolveTip() = %+v, want the base tip %s at %s", tip, baseRev, baseSelf)
	}
	if len(evalArgs) != 1 || strings.Contains(strings.Join(evalArgs[0], " "), featRev) {
		t.Fatalf("eval argv = %v, want one eval of the base tip only", evalArgs)
	}

	r.prebuilt = daemon.Tip{Revision: featRev, SelfPath: featSelf}
	if _, err := r.execTarget(); err == nil || !strings.Contains(err.Error(), baseSelf) {
		t.Fatalf("execTarget() with the feature branch's path = %v, want a refusal naming %s", err, baseSelf)
	}
	r.prebuilt = daemon.Tip{Revision: baseRev, SelfPath: baseSelf}
	if got, err := r.execTarget(); err != nil || got.SelfPath != baseSelf {
		t.Fatalf("execTarget() with the base tip's path = %+v, %v, want it accepted", got, err)
	}
}

func TestPrebuild_RecordsTheTipOnlyOnSuccess(t *testing.T) {
	h := newPrebuildHarness(t, "exit 1", "echo /nix/store/q75-spindrift-daemon", "exit 0")
	tip := daemon.Tip{Revision: "abc", SelfPath: prebuildSelfPath}
	if err := h.r.Prebuild(context.Background(), tip); err == nil {
		t.Fatal("Prebuild() = nil, want the eval failure")
	}
	if h.r.prebuilt != (daemon.Tip{}) {
		t.Errorf("prebuilt after a failed Prebuild = %+v, want zero", h.r.prebuilt)
	}
	ok := newPrebuildHarness(t, "echo /nix/store/il4-spindrift-daemon.drv", "echo /nix/store/q75-spindrift-daemon", "exit 0")
	if err := ok.r.Prebuild(context.Background(), tip); err != nil {
		t.Fatal(err)
	}
	if ok.r.prebuilt != tip {
		t.Errorf("prebuilt = %+v, want %+v", ok.r.prebuilt, tip)
	}
}

// stubSelfExec installs fn as the exec seam for the test.
func stubSelfExec(t *testing.T, fn func(argv0 string, argv, envv []string) error) {
	t.Helper()
	orig := runnerSelfExec
	t.Cleanup(func() { runnerSelfExec = orig })
	runnerSelfExec = fn
}

// handoffWithChild abandons a child on r and returns a one-slot Handoff naming
// it by the handle's own (close-on-exec) report fd, plus an instance lock file.
func handoffWithChild(t *testing.T, r *hostRunner) (daemon.Handoff, *hostChildHandle, *os.File) {
	t.Helper()
	h := startAbandonedChild(t, r)
	lock, err := os.CreateTemp(t.TempDir(), "lock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	return daemon.Handoff{Slots: []daemon.HandoffSlot{{Slot: 0, Kind: daemon.KindOf(dispatchkind.Work), Child: h.State()}}}, h, lock
}

// rollbackTargetRooted points the runner's .current root at storePath, as an
// earlier rootSelf would have.
func rollbackTargetRooted(t *testing.T, r *hostRunner, storePath string) {
	t.Helper()
	if err := os.Symlink(storePath, filepath.Join(r.stateDir, currentGCRoot)); err != nil {
		t.Fatal(err)
	}
}

func TestExecSuccessor_RefusesWithoutARootOnTheRollbackTarget(t *testing.T) {
	requireInPlaceRestart(t)
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, r *hostRunner)
	}{
		{"no root", func(*testing.T, *hostRunner) {}},
		{"root elsewhere", func(t *testing.T, r *hostRunner) { rollbackTargetRooted(t, r, "/nix/store/zzz-other") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := execReadyRunner(t, func(c *hostRunnerConfig) { c.stateDir = t.TempDir() })
			tc.setup(t, r)
			ho, _, lock := handoffWithChild(t, r)
			execed := false
			stubSelfExec(t, func(string, []string, []string) error { execed = true; return nil })
			err := r.execSuccessor(ho, lock, nil, "/nix/store/aaa-old/bin/daemon")
			if err == nil || !strings.Contains(err.Error(), "/nix/store/aaa-old") {
				t.Fatalf("execSuccessor() = %v, want a refusal naming the unrooted build", err)
			}
			if execed {
				t.Error("exec happened though the rollback target was not rooted")
			}
		})
	}
}

func TestExecSuccessor_HandsDescriptorsAndDocumentToTheSuccessor(t *testing.T) {
	requireInPlaceRestart(t)
	r := execReadyRunner(t, func(c *hostRunnerConfig) { c.stateDir = t.TempDir() })
	rollbackTargetRooted(t, r, "/nix/store/aaa-old")
	ho, _, lock := handoffWithChild(t, r)
	ho.Stop = true
	if len(r.handles) != 1 {
		t.Fatalf("registered handles = %d, want 1", len(r.handles))
	}
	origReport := ho.Slots[0].Child.ReportFD

	var gotArgv0 string
	var gotArgv, gotEnv []string
	var doc daemon.Handoff
	var docErr error
	var docFD int
	stubSelfExec(t, func(argv0 string, argv, envv []string) error {
		gotArgv0, gotArgv, gotEnv = argv0, argv, envv
		docFD, docErr = strconv.Atoi(argv[len(argv)-1])
		if docErr != nil {
			return nil
		}
		var buf bytes.Buffer
		chunk := make([]byte, 4096)
		for {
			n, err := syscall.Read(docFD, chunk)
			if n <= 0 || err != nil {
				break
			}
			buf.Write(chunk[:n])
		}
		doc, docErr = daemon.DecodeHandoff(&buf)
		return nil
	})

	fdsBefore := inheritableFDs(t)
	err := r.execSuccessor(ho, lock, nil, "/nix/store/aaa-old/bin/daemon")
	if err != nil {
		t.Fatalf("execSuccessor() = %v, want nil", err)
	}
	fdsAtExec := inheritableFDs(t)
	if docErr != nil {
		t.Fatalf("handoff document: %v", docErr)
	}
	if len(doc.Slots) != 1 {
		t.Fatalf("document slots = %d, want 1", len(doc.Slots))
	}
	dups := []int{docFD, doc.LockFD, doc.Slots[0].Child.ReportFD}
	t.Cleanup(func() {
		for _, fd := range dups {
			_ = syscall.Close(fd)
		}
	})

	if gotArgv0 != prebuildSelfPath {
		t.Errorf("exec target = %q, want %s", gotArgv0, prebuildSelfPath)
	}
	if doc.FromPath != "/nix/store/aaa-old/bin/daemon" || doc.ToPath != prebuildSelfPath || doc.Revision == "" || doc.Revision != r.prebuilt.Revision {
		t.Errorf("document from/to/revision = %q/%q/%q, want the old path, %s and the prebuilt revision %q", doc.FromPath, doc.ToPath, doc.Revision, prebuildSelfPath, r.prebuilt.Revision)
	}
	wantArgv := []string{prebuildSelfPath, "dispatch", "--handoff-fd", strconv.Itoa(docFD)}
	if !slices.Equal(gotArgv, wantArgv) {
		t.Errorf("argv = %v, want %v", gotArgv, wantArgv)
	}
	if !slices.Equal(gotEnv, r.env) {
		t.Error("env differs from the runner's captured environment")
	}
	wantFDs := append(slices.Clone(fdsBefore), dups...)
	slices.Sort(wantFDs)
	if !slices.Equal(fdsAtExec, wantFDs) {
		t.Errorf("inheritable fds at exec = %v, want %v (before %v + exactly the named dups %v)", fdsAtExec, wantFDs, fdsBefore, dups)
	}
	for _, fd := range dups {
		if cloexec, err := isCloseOnExec(fd); err != nil || cloexec {
			t.Errorf("dup fd %d close-on-exec = %v, %v, want inheritable", fd, cloexec, err)
		}
	}
	for _, c := range []struct {
		name      string
		orig, dup int
	}{{"lock", fdOf(t, lock), doc.LockFD}, {"report", origReport, doc.Slots[0].Child.ReportFD}} {
		if c.orig == c.dup {
			t.Errorf("%s: document names the original fd %d", c.name, c.orig)
		}
		if cloexec, err := isCloseOnExec(c.orig); err != nil || !cloexec {
			t.Errorf("%s: original fd %d close-on-exec = %v, %v, want true", c.name, c.orig, cloexec, err)
		}
		var a, b syscall.Stat_t
		if syscall.Fstat(c.orig, &a) != nil || syscall.Fstat(c.dup, &b) != nil || a.Ino != b.Ino || a.Dev != b.Dev {
			t.Errorf("%s: dup %d is not the same file as %d", c.name, c.dup, c.orig)
		}
	}
	if !doc.Stop || doc.Slots[0].Child.PID != ho.Slots[0].Child.PID || !bytes.Equal(doc.Slots[0].Child.Partial, ho.Slots[0].Child.Partial) {
		t.Errorf("document = %+v does not carry the handoff %+v", doc, ho)
	}
	if ho.LockFD != 0 || ho.Slots[0].Child.ReportFD != origReport {
		t.Error("the caller's handoff was modified")
	}
	if len(r.handles) != 0 {
		t.Errorf("registered handles after exec = %d, want 0 (the successor owns them)", len(r.handles))
	}
}

func TestExecSuccessor_RefusesWithoutExec(t *testing.T) {
	requireInPlaceRestart(t)
	called := false
	stubSelfExec(t, func(string, []string, []string) error { called = true; return nil })

	stopped := make(chan struct{})
	close(stopped)
	r := execReadyRunner(t, nil)
	lock, err := os.CreateTemp(t.TempDir(), "lock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	if err := r.execSuccessor(daemon.Handoff{}, lock, stopped, "/self"); !errors.Is(err, errStopBeforeExec) {
		t.Errorf("execSuccessor(stop closed) = %v, want errStopBeforeExec", err)
	}

	r.prebuilt = daemon.Tip{}
	if err := r.execSuccessor(daemon.Handoff{}, lock, nil, "/self"); err == nil {
		t.Error("execSuccessor() without a prebuild = nil, want an error")
	}
	if called {
		t.Error("the exec seam was called")
	}
}

func TestExecSuccessor_OversizedDocumentIsRefused(t *testing.T) {
	requireInPlaceRestart(t)
	stubSelfExec(t, func(string, []string, []string) error { t.Error("exec called"); return nil })
	r := execReadyRunner(t, nil)
	lock, err := os.CreateTemp(t.TempDir(), "lock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	before := inheritableFDs(t)
	err = r.execSuccessor(daemon.Handoff{Breaker: make([]time.Time, 1<<14)}, lock, nil, "/self")
	if err == nil || !strings.Contains(err.Error(), "exceeds the pipe's capacity") {
		t.Fatalf("execSuccessor() = %v, want a capacity refusal", err)
	}
	if after := inheritableFDs(t); !slices.Equal(after, before) {
		t.Errorf("inheritable fds after = %v, want %v", after, before)
	}
}

func TestWaitOut_ReapsUnclaimedChildren(t *testing.T) {
	r := execReadyRunner(t, nil)
	h := startAbandonedChild(t, r)
	if len(r.handles) != 1 {
		t.Fatalf("registered handles = %d, want 1", len(r.handles))
	}
	_ = h.proc.Kill()
	done := make(chan struct{})
	go func() { waitOut(r.takeUnclaimed(), nil, nil); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("waitOut did not return")
	}
	if len(r.handles) != 0 {
		t.Errorf("registered handles after = %d, want 0", len(r.handles))
	}
	if _, err := h.proc.Wait(); err == nil {
		t.Error("child was not reaped by waitOut")
	}
}

func TestInheritChild_ClaimsAPreInheritedHandle(t *testing.T) {
	cfg := hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()}
	first := startAbandonedChild(t, mustHostRunner(t, cfg))
	st := simulateExec(t, first, first.State())
	if !slices.Contains(inheritableFDs(t), st.ReportFD) {
		t.Fatalf("fd %d is not inheritable before inheritHandle", st.ReportFD)
	}

	inherited, err := inheritHandle(st)
	if err != nil {
		t.Fatalf("inheritHandle() unexpected error: %v", err)
	}
	if slices.Contains(inheritableFDs(t), st.ReportFD) {
		t.Errorf("fd %d still inheritable after inheritHandle", st.ReportFD)
	}
	cfg.inherited = map[int]*hostChildHandle{st.ReportFD: inherited}
	r := mustHostRunner(t, cfg)
	got, err := r.InheritChild(context.Background(), daemon.ChildRequest{Kind: daemon.KindOf(dispatchkind.Work)}, st)
	if err != nil {
		t.Fatalf("InheritChild() unexpected error: %v", err)
	}
	if got.(*hostChildHandle) != inherited {
		t.Error("InheritChild() did not reuse the inherited handle")
	}
	if len(r.handles) != 0 {
		t.Errorf("registered handles = %d, want 0 once claimed", len(r.handles))
	}
}

// requireInPlaceRestart skips a test of the exec path where it is not built.
func requireInPlaceRestart(t testing.TB) {
	t.Helper()
	if !inPlaceRestartSupported {
		t.Skip("in-place restart is supported only on linux amd64/arm64")
	}
}

// TestSweepLateMerges points the sweep's exec seam at a scripted shell command:
// a zero exit is success, a non-zero one an error naming the exit, and the
// command is the pinned hidden verb under an env that is no pool child's.
func TestSweepLateMerges(t *testing.T) {
	orig := runnerSweepCommand
	t.Cleanup(func() { runnerSweepCommand = orig })
	var argv []string
	var script string
	runnerSweepCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		argv = append([]string{name}, args...)
		return exec.CommandContext(ctx, "/bin/sh", "-c", script)
	}
	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: []string{"PATH=/bin", daemon.PoolChildEnv + "=1"}})

	script = "exit 0"
	if err := r.SweepLateMerges(context.Background(), "abc"); err != nil {
		t.Fatalf("SweepLateMerges() = %v, want nil on exit 0", err)
	}
	if got := argv[len(argv)-1]; got != daemon.LateMergeSweepVerb {
		t.Errorf("argv = %v, want it to end in %s", argv, daemon.LateMergeSweepVerb)
	}
	if !slices.ContainsFunc(argv, func(a string) bool { return strings.Contains(a, "rev=abc") }) {
		t.Errorf("argv = %v, want the flakeref pinned to rev abc", argv)
	}

	script = "echo boom >&2; exit 3"
	err := r.SweepLateMerges(context.Background(), "abc")
	if err == nil || !strings.Contains(err.Error(), "exit status 3") {
		t.Fatalf("SweepLateMerges() = %v, want an error naming exit status 3", err)
	}
}

// The sweep runs in its own process group so a terminal Ctrl-C halts the pool
// without also killing a running sweep.
func TestSweepLateMerges_ChildInOwnProcessGroup(t *testing.T) {
	orig := runnerSweepCommand
	t.Cleanup(func() { runnerSweepCommand = orig })
	var cmd *exec.Cmd
	runnerSweepCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		cmd = exec.CommandContext(ctx, "/bin/sh", "-c", "exit 0")
		return cmd
	}
	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	if err := r.SweepLateMerges(context.Background(), "abc"); err != nil {
		t.Fatalf("SweepLateMerges() = %v, want nil", err)
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Errorf("SysProcAttr = %+v, want Setpgid so a group-wide SIGINT misses the sweep", cmd.SysProcAttr)
	}
}

// Cancelling a sweep's ctx kills its whole process group: with Setpgid, a
// bare CommandContext kill reaches only the direct PID and orphans whatever
// the sweep forked (a Connector subprocess). The grandchild holds a pipe's
// write end, so the read hits EOF only once it is gone.
func TestSweepLateMerges_CancelKillsGrandchild(t *testing.T) {
	orig := runnerSweepCommand
	t.Cleanup(func() { runnerSweepCommand = orig })
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()
	runnerSweepCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", `sleep 30 & echo up >&3; wait`)
		cmd.ExtraFiles = []*os.File{pw}
		return cmd
	}
	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- r.SweepLateMerges(ctx, "abc") }()

	_ = pr.SetReadDeadline(time.Now().Add(10 * time.Second))
	buf := make([]byte, 3)
	if _, err := io.ReadFull(pr, buf); err != nil {
		t.Fatalf("sweep never reported its grandchild started: %v", err)
	}
	_ = pw.Close()
	cancel()
	if _, err := io.ReadAll(pr); err != nil {
		t.Fatalf("grandchild outlived the cancelled sweep: %v", err)
	}
	if err := <-errc; err == nil {
		t.Error("SweepLateMerges() = nil, want an error for a torn-down sweep")
	}
}

// A sweep that outlives its ctx is torn down rather than waited on.
func TestSweepLateMerges_CancelledContextTearsDownChild(t *testing.T) {
	orig := runnerSweepCommand
	t.Cleanup(func() { runnerSweepCommand = orig })
	runnerSweepCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", "-c", "exec sleep 30")
	}
	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := r.SweepLateMerges(ctx, "abc"); err == nil {
		t.Fatal("SweepLateMerges() = nil, want an error for a torn-down sweep")
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("SweepLateMerges took %v, want it cut short by ctx", d)
	}
}
