package dispatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/testutil"
)

// The heartbeat writer tees the stream, so the log file must still hold the
// runner's bytes exactly.
func TestRun_HeartbeatRawLogExact(t *testing.T) {
	dir := tempLogDir(t)

	streamJSON := eventLine(driverkit.Event{Kind: driverkit.KindToolCall, ToolName: "Edit", ToolTarget: "main.go"}) +
		eventLine(driverkit.Event{Kind: driverkit.KindResult, NumTurns: 3, DurationMs: 1000})

	fr := runner.NewFake()
	fr.WriteToOutput = []byte(streamJSON)

	drv := driverkit.New(testStrategy)
	f, err := NewFactory(Config{}, dir, fr, drv, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	d := f.New("55", "heartbeat test")
	if result := d.Run(); !result.ok() {
		t.Fatalf("Run: want succeeded, got %+v", result)
	}

	logPath := filepath.Join(HostLogDirFor(dir), "issue-55.log")
	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if rest := afterStamp(t, got); rest != streamJSON {
		t.Errorf("log file not byte-exact after the stamp:\ngot:  %q\nwant: %q", rest, streamJSON)
	}
}

func TestRun_HeartbeatEmitsToStdout(t *testing.T) {
	dir := tempLogDir(t)

	streamJSON := eventLine(driverkit.Event{Kind: driverkit.KindModel, Model: "m"}) +
		eventLine(driverkit.Event{Kind: driverkit.KindToolCall, ToolName: "Bash", ToolTarget: "go test ./..."}) +
		eventLine(driverkit.Event{Kind: driverkit.KindResult, NumTurns: 5, DurationMs: 2000})

	fr := runner.NewFake()
	fr.WriteToOutput = []byte(streamJSON)

	drv := driverkit.New(testStrategy)
	f, err := NewFactory(Config{}, dir, fr, drv, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	d := f.New("99", "heartbeat stdout test")
	var result Disposition
	out := testutil.CaptureStdout(t, func() { result = d.Run() })

	if !result.ok() {
		t.Fatalf("Run: want succeeded, got %+v", result)
	}
	if !strings.Contains(out, "#99") {
		t.Errorf("heartbeat missing issue prefix in stdout: %q", out)
	}
	if !strings.Contains(out, "bash") {
		t.Errorf("heartbeat missing tool kind 'bash' in stdout: %q", out)
	}
}

// Run's dispatch-start announce line reaches stdout (issue #1829).
func TestRun_AnnounceEmitsToStdout(t *testing.T) {
	dir := tempLogDir(t)

	fr := runner.NewFake()
	f, err := NewFactory(Config{}, dir, fr, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	d := f.New("99", "run announce test")
	var result Disposition
	out := testutil.CaptureStdout(t, func() { result = d.Run() })

	if !result.ok() {
		t.Fatalf("Run: want succeeded, got %+v", result)
	}
	if !strings.Contains(out, "-> #99: run announce test") {
		t.Errorf("stdout missing run announce line, got %q", out)
	}
}

// Fix's fix-pass announce line reaches stdout (issue #1829).
func TestFix_AnnounceEmitsToStdout(t *testing.T) {
	dir := tempLogDir(t)

	fr := runner.NewFake()
	f, err := NewFactory(Config{}, dir, fr, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	d := f.New("99", "fix announce test")
	var result Disposition
	out := testutil.CaptureStdout(t, func() { result = d.Fix(1, "") })

	if !result.ok() {
		t.Fatalf("Fix: want succeeded, got %+v", result)
	}
	if !strings.Contains(out, "-> #99 (fix-pass-1): fix announce test") {
		t.Errorf("stdout missing fix-pass announce line, got %q", out)
	}
}

// ResolveConflict's announce line reaches stdout (issue #1829).
func TestResolveConflict_AnnounceEmitsToStdout(t *testing.T) {
	dir := tempLogDir(t)

	fr := runner.NewFake()
	f, err := NewFactory(Config{}, dir, fr, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	d := f.New("99", "conflict announce test")
	var resolveErr error
	out := testutil.CaptureStdout(t, func() {
		resolveErr = d.ResolveConflict("https://github.com/owner/repo/pull/1")
	})

	if resolveErr != nil {
		t.Fatalf("ResolveConflict: %v", resolveErr)
	}
	if !strings.Contains(out, "-> #99 (conflict-resolve): conflict announce test") {
		t.Errorf("stdout missing conflict-resolve announce line, got %q", out)
	}
}
