package dispatch

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/report"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/testutil"
)

func TestDispatch_Run_EmitsBoxRecordAndUnchangedHumanLine(t *testing.T) {
	readRecords := testutil.InstallPipeReporter(t)

	fr := runner.NewFake()
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, fakeDriver{}, RealClock())

	out := testutil.CaptureStdout(t, func() { d.Run() })

	want := "    -> #1: t\n"
	if out != want {
		t.Fatalf("human announce line: got %q, want %q", out, want)
	}

	recs := readRecords()
	if len(recs) != 1 {
		t.Fatalf("records: got %d, want 1: %+v", len(recs), recs)
	}
	if recs[0].Event != "box" || recs[0].Issue != "1" || recs[0].Phase != "initial" {
		t.Errorf("record = %+v, want event=box issue=1 phase=initial", recs[0])
	}
}

func TestDispatch_Fix_EmitsBoxRecordWithFixPassPhase(t *testing.T) {
	readRecords := testutil.InstallPipeReporter(t)

	fr := runner.NewFake()
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, fakeDriver{}, RealClock())

	out := testutil.CaptureStdout(t, func() { d.Fix(2, "") })

	want := "    -> #1 (fix-pass-2): t\n"
	if out != want {
		t.Fatalf("human announce line: got %q, want %q", out, want)
	}

	recs := readRecords()
	if len(recs) != 1 {
		t.Fatalf("records: got %d, want 1: %+v", len(recs), recs)
	}
	if recs[0].Event != "box" || recs[0].Issue != "1" || recs[0].Phase != "fix-pass-2" {
		t.Errorf("record = %+v, want event=box issue=1 phase=fix-pass-2", recs[0])
	}
}

func TestDispatch_ResolveConflict_EmitsBoxRecordWithConflictResolvePhase(t *testing.T) {
	readRecords := testutil.InstallPipeReporter(t)

	fr := runner.NewFake()
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, fakeDriver{}, RealClock())

	out := testutil.CaptureStdout(t, func() { d.ResolveConflict("https://example.com/pr/1") })

	want := "    -> #1 (conflict-resolve): t\n"
	if out != want {
		t.Fatalf("human announce line: got %q, want %q", out, want)
	}

	recs := readRecords()
	if len(recs) != 1 {
		t.Fatalf("records: got %d, want 1: %+v", len(recs), recs)
	}
	if recs[0].Event != "box" || recs[0].Issue != "1" || recs[0].Phase != "conflict-resolve" {
		t.Errorf("record = %+v, want event=box issue=1 phase=conflict-resolve", recs[0])
	}
}

// TestFD3ClosedAcrossDispatchSpawn closes the gap TestFromEnv_CloseOnExec
// (report_test.go) leaves: that test proves FromEnv's own close-on-exec
// call, but never runs Dispatch code, so a regression that let a Box (or
// anything Dispatch's runner spawns) inherit the report pipe's write end
// would keep the daemon's readReports blocked past child exit with nothing
// here to catch it (issue #3627). It re-execs the test binary as a helper
// process wired the way runner.go really wires a child — SPINDRIFT_REPORT_FD
// via ExtraFiles, which os/exec explicitly clears FD_CLOEXEC on — because
// fd inheritance across a real fork+exec can't be reproduced by calling
// FromEnv in-process.
func TestFD3ClosedAcrossDispatchSpawn(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer r.Close()

	cmd := exec.Command(os.Args[0], "-test.run=^TestFD3ClosedHelperProcess$", "-test.v")
	cmd.Env = append(os.Environ(),
		"GO_WANT_FD3_HELPER_PROCESS=1",
		"SPINDRIFT_REPORT_FD=3",
	)
	cmd.ExtraFiles = []*os.File{w}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	// Close this process's copy of the write end right after Start: the
	// helper (and, if the bug this test guards against reappeared, anything
	// it spawns) holds the only copies that matter from here on. Without
	// this the scanner below never sees EOF, even once the helper exits.
	w.Close()

	scanner := bufio.NewScanner(r)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan report pipe: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("helper process failed: %v\nstderr:\n%s", err, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("helper process did not exit within 10s; stderr so far:\n%s", stderr.String())
	}

	for _, line := range lines {
		if strings.Contains(line, "leaked") {
			t.Fatalf("scripted subprocess wrote to fd 3: %q (all lines: %v)", line, lines)
		}
	}

	var sawBox bool
	for _, line := range lines {
		var rec report.Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if rec.Event == "box" && rec.Issue == "1" && rec.Phase == "initial" {
			sawBox = true
		}
	}
	if !sawBox {
		t.Fatalf("no box record observed on the report pipe; lines: %v\nstderr:\n%s", lines, stderr.String())
	}
}

// TestFD3ClosedHelperProcess is not a real test: it is the re-exec target
// for TestFD3ClosedAcrossDispatchSpawn, gated by GO_WANT_FD3_HELPER_PROCESS
// so an ordinary `go test` run treats it as a no-op (the standard Go
// helper-process pattern, as used by os/exec's own tests) rather than
// running Dispatch code as its own OS process with fd 3 wired up.
func TestFD3ClosedHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_FD3_HELPER_PROCESS") != "1" {
		return
	}

	rep := report.FromEnv(os.Getenv, os.Stderr)
	if rep == nil {
		t.Fatal("report.FromEnv returned nil")
	}
	report.Install(rep) // same call main.go makes before any subcommand runs

	var writeErr error
	fr := runner.NewFake()
	fr.RunFunc = func(runner.Box) error {
		// This is the subprocess a real Box's launch stands in for. If fd 3
		// survived into it, this write lands on the report pipe as raw text
		// ("leaked"), not a JSON record, which the parent scans for
		// directly rather than trusting only this process's exit status.
		writeErr = exec.Command("/bin/sh", "-c", "echo leaked >&3").Run()
		return nil
	}

	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, fakeDriver{}, RealClock())
	d.Run()

	if writeErr == nil {
		t.Fatal("scripted subprocess write to fd 3 succeeded, want failure")
	}
}
