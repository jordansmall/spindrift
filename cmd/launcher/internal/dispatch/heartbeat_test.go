package dispatch

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/testutil"
)

// TestRun_HeartbeatRawLogExact verifies that bytes written by the runner to
// box.Output reach the log file byte-for-byte even when the heartbeat writer
// is active.
func TestRun_HeartbeatRawLogExact(t *testing.T) {
	dir := tempLogDir(t)

	streamJSON := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"main.go"}}]}}` + "\n" +
		`{"type":"result","num_turns":3,"total_cost_usd":0.01,"duration_ms":1000}` + "\n"

	fr := runner.NewFake()
	fr.WriteToOutput = []byte(streamJSON)

	drv, err := driver.New("claude")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	f, err := NewFactory(Config{}, dir, fr, drv, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	d := f.New("55", "heartbeat test")
	if result := d.Run(); !result.Success {
		t.Fatalf("Run: want Success=true, got %+v", result)
	}

	logPath := filepath.Join(dir, "logs", "issue-55.log")
	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if string(got) != streamJSON {
		t.Errorf("log file not byte-exact:\ngot:  %q\nwant: %q", string(got), streamJSON)
	}
}

// TestRun_HeartbeatEmitsToStdout verifies that when a box writes stream-json
// events, heartbeat lines appear on stdout (captured via pipe).
func TestRun_HeartbeatEmitsToStdout(t *testing.T) {
	dir := tempLogDir(t)

	streamJSON := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}]}}` + "\n" +
		`{"type":"result","num_turns":5,"duration_ms":2000}` + "\n"

	fr := runner.NewFake()
	fr.WriteToOutput = []byte(streamJSON)

	drv, err := driver.New("claude")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	f, err := NewFactory(Config{}, dir, fr, drv, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	d := f.New("99", "heartbeat stdout test")
	var result Result
	out := testutil.CaptureStdout(t, func() { result = d.Run() })

	if !result.Success {
		t.Fatalf("Run: want Success=true, got %+v", result)
	}
	if !strings.Contains(out, "#99") {
		t.Errorf("heartbeat missing issue prefix in stdout: %q", out)
	}
	if !strings.Contains(out, "bash") {
		t.Errorf("heartbeat missing tool kind 'bash' in stdout: %q", out)
	}
}

// TestRun_HeartbeatSuppressedWhenDiscardConfigured verifies that a Factory
// with its heartbeat sink set to io.Discard (the console entry point, issue
// #1583) writes no heartbeat lines (role headers or tool-count lines) to
// stdout, while the log file still captures the full raw stream untouched.
// Console's own non-heartbeat "-> #NN: title" announce line (box.go's
// dispatchWithRetry callers) is out of this issue's scope and deliberately
// not asserted against here.
func TestRun_HeartbeatSuppressedWhenDiscardConfigured(t *testing.T) {
	dir := tempLogDir(t)

	streamJSON := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}]}}` + "\n" +
		`{"type":"result","num_turns":5,"duration_ms":2000}` + "\n"

	fr := runner.NewFake()
	fr.WriteToOutput = []byte(streamJSON)

	drv, err := driver.New("claude")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	f, err := NewFactory(Config{}, dir, fr, drv, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()
	f.SetHeartbeatOut(io.Discard)

	d := f.New("99", "heartbeat discard test")
	var result Result
	out := testutil.CaptureStdout(t, func() { result = d.Run() })

	if !result.Success {
		t.Fatalf("Run: want Success=true, got %+v", result)
	}
	if strings.Contains(out, "\xe2\x94\x80\xe2\x94\x80") {
		t.Errorf("stdout should carry no heartbeat role header when discarded, got %q", out)
	}
	if strings.Contains(out, "bash") {
		t.Errorf("stdout should carry no heartbeat tool-count line when discarded, got %q", out)
	}

	logPath := filepath.Join(dir, "logs", "issue-99.log")
	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if string(got) != streamJSON {
		t.Errorf("log file not byte-exact:\ngot:  %q\nwant: %q", string(got), streamJSON)
	}
}

// TestFactory_SetHeartbeatOutPanicsAfterNew verifies the ordering contract on
// SetHeartbeatOut (issue #1594) is enforced at runtime: calling it after New()
// has already copied cfg into a Dispatch must panic rather than silently
// racing or affecting only Dispatches constructed afterward.
func TestFactory_SetHeartbeatOutPanicsAfterNew(t *testing.T) {
	dir := tempLogDir(t)

	fr := runner.NewFake()
	drv, err := driver.New("claude")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	f, err := NewFactory(Config{}, dir, fr, drv, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	f.New("1", "first dispatch")

	defer func() {
		if recover() == nil {
			t.Errorf("SetHeartbeatOut after New: want panic, got none")
		}
	}()
	f.SetHeartbeatOut(io.Discard)
}
