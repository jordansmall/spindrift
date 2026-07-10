package dispatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/runner"
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

	origStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

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
	result := d.Run()

	w.Close()
	os.Stdout = origStdout

	var buf strings.Builder
	tmp := make([]byte, 4096)
	for {
		n, _ := r.Read(tmp)
		if n == 0 {
			break
		}
		buf.Write(tmp[:n])
	}

	if !result.Success {
		t.Fatalf("Run: want Success=true, got %+v", result)
	}

	out := buf.String()
	if !strings.Contains(out, "#99") {
		t.Errorf("heartbeat missing issue prefix in stdout: %q", out)
	}
	if !strings.Contains(out, "bash") {
		t.Errorf("heartbeat missing tool kind 'bash' in stdout: %q", out)
	}
}
