package dispatch

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/runner"
)

// TestRun_SkipsAlreadyClaimedIssueAcrossFactories pins the core fix for issue
// #3885: two Dispatches for the same issue, built from two SEPARATE Factories
// sharing one pwd (the shape of two launcher processes racing the same
// checkout), must not both touch disk. The first Run() holds the claim for
// its whole life; the second must lose before it renames the first's live
// log, re-stamps the run-lineage marker, or opens any new attempt log.
func TestRun_SkipsAlreadyClaimedIssueAcrossFactories(t *testing.T) {
	dir := tempLogDir(t)

	fr1 := runner.NewFake()
	started := make(chan struct{})
	proceed := make(chan struct{})
	fr1.RunFunc = func(box runner.Box) error {
		close(started)
		<-proceed
		box.Output.Write(nonceLineFromEnv(box, "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok")) //nolint:errcheck
		return nil
	}
	f1, err := NewFactory(retryConfig(3, 0, 0), dir, fr1, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory 1: %v", err)
	}
	defer f1.Cleanup()
	d1 := f1.New("1", "t")

	d1Done := make(chan Result, 1)
	go func() { d1Done <- d1.Run() }()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("d1.Run() never reached runner.Run -- claim wiring may be blocking the winner")
	}

	// By now Run's claim, quarantine, and mark-lineage all already happened
	// for d1, and runOnce already created issue-1.log. Remove the marker so a
	// re-stamp by the loser is observable as its reappearance, and record the
	// log dir's contents so any new file from the loser is observable too.
	markerPath := runLineageMarkerPath(dir, "1")
	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("run-lineage marker: want present once d1's first attempt started, stat error: %v", err)
	}
	if err := os.Remove(markerPath); err != nil {
		t.Fatalf("remove marker: %v", err)
	}

	entriesBefore, err := os.ReadDir(filepath.Dir(d1.logPath()))
	if err != nil {
		t.Fatalf("read log dir before d2: %v", err)
	}
	namesBefore := dirEntryNames(entriesBefore)

	fr2 := runner.NewFake()
	f2, err := NewFactory(retryConfig(3, 0, 0), dir, fr2, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory 2: %v", err)
	}
	defer f2.Cleanup()
	d2 := f2.New("1", "t")

	result2 := d2.Run()

	if !result2.AlreadyInFlight {
		t.Fatalf("d2.Run(): want AlreadyInFlight=true, got %+v", result2)
	}
	if len(fr2.RunCalls) != 0 {
		t.Errorf("fr2.RunCalls: want 0, got %d -- the loser must never reach runOnce", len(fr2.RunCalls))
	}

	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Errorf("run-lineage marker: want still absent (loser must not re-stamp it), stat err=%v", err)
	}

	entriesAfter, err := os.ReadDir(filepath.Dir(d1.logPath()))
	if err != nil {
		t.Fatalf("read log dir after d2: %v", err)
	}
	namesAfter := dirEntryNames(entriesAfter)
	if len(namesAfter) != len(namesBefore) {
		t.Errorf("log dir contents changed by the loser: before=%v after=%v", namesBefore, namesAfter)
	}
	for _, n := range namesAfter {
		if strings.Contains(n, "prior-run") {
			t.Errorf("loser quarantined the winner's live log: found %s", n)
		}
	}

	close(proceed)
	result1 := <-d1Done
	if !result1.Success {
		t.Fatalf("d1.Run(): want Success=true, got %+v", result1)
	}

	cur, err := os.ReadFile(d1.logPath())
	if err != nil {
		t.Fatalf("read d1 log after finish: %v", err)
	}
	if !strings.Contains(string(cur), "SPINDRIFT_OUTCOME") {
		t.Errorf("d1's log missing its own outcome line after finishing: %q", cur)
	}
}

// dirEntryNames projects os.DirEntry into a sorted-by-ReadDir name slice for
// a before/after comparison.
func dirEntryNames(entries []os.DirEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Name()
	}
	return out
}

// nonceLineFromEnv mirrors nonceLine (testhelpers_test.go) but reads the
// nonce out of the Box env directly, for a RunFunc that has no *Dispatch in
// scope.
func nonceLineFromEnv(box runner.Box, line string) []byte {
	return []byte(line + " nonce=" + box.Env["RUN_NONCE"] + "\n")
}

// TestClaimIssue_HeldUntilClose pins issue #4364: Run()'s claim outlives Run
// so the caller's settle (CI poll, Fix, ResolveConflict, merge) stays
// exclusive, and only Close releases it, on both the success and the
// terminal-failure path.
func TestClaimIssue_HeldUntilClose(t *testing.T) {
	check := func(t *testing.T, dir string, d *Dispatch) {
		t.Helper()
		if _, err := ClaimIssue(dir, "1"); !errors.Is(err, ErrIssueClaimed) {
			t.Fatalf("ClaimIssue after Run, before Close: want ErrIssueClaimed, got %v", err)
		}
		d.Close()
		release, err := ClaimIssue(dir, "1")
		if err != nil {
			t.Fatalf("ClaimIssue after Close: want it to succeed, got %v", err)
		}
		release()
		d.Close() // idempotent; must neither panic nor drop someone else's claim
	}

	t.Run("success", func(t *testing.T) {
		dir := tempLogDir(t)
		fr := runner.NewFake()
		fr.RunFunc = func(box runner.Box) error {
			box.Output.Write(nonceLineFromEnv(box, "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok")) //nolint:errcheck
			return nil
		}
		f, err := NewFactory(retryConfig(3, 0, 0), dir, fr, fakeDriver{}, RealClock())
		if err != nil {
			t.Fatalf("NewFactory: %v", err)
		}
		defer f.Cleanup()
		d := f.New("1", "t")

		if result := d.Run(); !result.Success {
			t.Fatalf("Run: want Success=true, got %+v", result)
		}
		check(t, dir, d)
	})

	t.Run("failure", func(t *testing.T) {
		dir := tempLogDir(t)
		fr := runner.NewFake()
		fr.RunErrs = []error{boxErr}
		f, err := NewFactory(retryConfig(1, 0, 0), dir, fr, fakeDriver{}, RealClock())
		if err != nil {
			t.Fatalf("NewFactory: %v", err)
		}
		defer f.Cleanup()
		d := f.New("1", "t")

		if result := d.Run(); result.Success {
			t.Fatalf("Run: want Success=false for a terminal failure, got %+v", result)
		}
		check(t, dir, d)
	})
}

// TestRun_SecondRunLosesWhileFirstUnclosed pins the settle-window race of
// issue #4364: once Dispatch A's Run returned (but A is not closed, so its
// caller is still settling), a second Run for the issue must report
// AlreadyInFlight without launching a Box or touching the log dir.
func TestRun_SecondRunLosesWhileFirstUnclosed(t *testing.T) {
	dir := tempLogDir(t)
	fr1 := runner.NewFake()
	fr1.RunFunc = func(box runner.Box) error {
		box.Output.Write(nonceLineFromEnv(box, "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok")) //nolint:errcheck
		return nil
	}
	f1, err := NewFactory(retryConfig(3, 0, 0), dir, fr1, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory 1: %v", err)
	}
	defer f1.Cleanup()
	d1 := f1.New("1", "t")
	if result := d1.Run(); !result.Success {
		t.Fatalf("d1.Run(): want Success=true, got %+v", result)
	}

	entriesBefore, err := os.ReadDir(HostLogDirFor(dir))
	if err != nil {
		t.Fatalf("read log dir before d2: %v", err)
	}
	namesBefore := dirEntryNames(entriesBefore)

	fr2 := runner.NewFake()
	f2, err := NewFactory(retryConfig(3, 0, 0), dir, fr2, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory 2: %v", err)
	}
	defer f2.Cleanup()
	d2 := f2.New("1", "t")
	defer d2.Close()

	if result := d2.Run(); !result.AlreadyInFlight {
		t.Fatalf("d2.Run() while d1 unclosed: want AlreadyInFlight=true, got %+v", result)
	}
	if len(fr2.RunCalls) != 0 {
		t.Errorf("fr2.RunCalls: want 0, got %d", len(fr2.RunCalls))
	}
	entriesAfter, err := os.ReadDir(HostLogDirFor(dir))
	if err != nil {
		t.Fatalf("read log dir after d2: %v", err)
	}
	if after := dirEntryNames(entriesAfter); strings.Join(after, ",") != strings.Join(namesBefore, ",") {
		t.Errorf("log dir changed by the loser: before=%v after=%v", namesBefore, after)
	}

	d1.Close()
	release, err := ClaimIssue(dir, "1")
	if err != nil {
		t.Fatalf("ClaimIssue after d1.Close: %v", err)
	}
	release()
}

// spindriftClaimHelperEnv gates TestHelperClaimAndBlock: unset, it is a no-op
// under a normal `go test` run; set, the parent below re-execs the test
// binary with it to get a genuine second OS process racing ClaimIssue,
// covering the real cross-process case flock (unlike an in-process mutex)
// exists for.
const spindriftClaimHelperEnv = "SPINDRIFT_CLAIM_HELPER"

// TestHelperClaimAndBlock is not a real test: it is the child-process body
// for TestClaimIssue_CrossProcess, invoked via `-test.run` with
// spindriftClaimHelperEnv set. It claims the issue named by
// SPINDRIFT_CLAIM_PWD/SPINDRIFT_CLAIM_NUMBER, announces readiness on stdout,
// and blocks on stdin until the parent kills it -- process death, not a
// graceful release call, is the scenario under test.
func TestHelperClaimAndBlock(t *testing.T) {
	if os.Getenv(spindriftClaimHelperEnv) != "1" {
		t.Skip("helper process for TestClaimIssue_CrossProcess; not a standalone test")
	}
	pwd := os.Getenv("SPINDRIFT_CLAIM_PWD")
	number := os.Getenv("SPINDRIFT_CLAIM_NUMBER")

	release, err := ClaimIssue(pwd, number)
	if err != nil {
		fmt.Println("ERR:" + err.Error())
		return
	}
	defer release()

	fmt.Println("READY")
	// Blocks until the parent closes/kills stdin (or the process is killed
	// outright); either way this call never returns on its own.
	_, _ = io.Copy(io.Discard, os.Stdin)
}

// TestClaimIssue_CrossProcess is the genuine cross-process case: a second OS
// process (not just a second goroutine) holds the claim while the parent
// asserts ClaimIssue reports ErrIssueClaimed, then kills the child and
// asserts the claim frees up immediately -- a crash must never leave a
// blocking claim behind (issue #3885).
func TestClaimIssue_CrossProcess(t *testing.T) {
	dir := tempLogDir(t)

	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperClaimAndBlock$", "-test.v")
	cmd.Env = append(os.Environ(),
		spindriftClaimHelperEnv+"=1",
		"SPINDRIFT_CLAIM_PWD="+dir,
		"SPINDRIFT_CLAIM_NUMBER=99",
	)
	stdinW, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper process: %v", err)
	}
	t.Cleanup(func() {
		_ = stdinW.Close()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	scanner := bufio.NewScanner(stdout)
	ready := make(chan string, 1)
	go func() {
		for scanner.Scan() {
			line := scanner.Text()
			if line == "READY" || strings.HasPrefix(line, "ERR:") {
				ready <- line
				return
			}
		}
		ready <- ""
	}()

	select {
	case line := <-ready:
		if line != "READY" {
			t.Fatalf("helper process did not report READY, got %q", line)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("helper process never reported READY")
	}

	if _, err := ClaimIssue(dir, "99"); !errors.Is(err, ErrIssueClaimed) {
		t.Fatalf("ClaimIssue while helper process holds the claim: want ErrIssueClaimed, got %v", err)
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill helper process: %v", err)
	}
	_, _ = cmd.Process.Wait()

	release, err := ClaimIssue(dir, "99")
	if err != nil {
		t.Fatalf("ClaimIssue after helper process was killed: want it to succeed, got %v", err)
	}
	release()
}
