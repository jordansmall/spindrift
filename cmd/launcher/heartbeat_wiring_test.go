package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/runner"
)

// heartbeatOutput captures the launcher's heartbeat lines. runOne writes
// heartbeat output to os.Stdout by default; the tests inject a pipe.
// We test the log-file byte-exact constraint and that the raw output reaches
// the log.

// TestRunOneHeartbeatRawLogExact verifies that bytes written by the runner to
// box.Output reach the log file byte-for-byte even when the heartbeat writer
// is active.
func TestRunOneHeartbeatRawLogExact(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}

	streamJSON := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"main.go"}}]}}` + "\n" +
		`{"type":"result","num_turns":3,"total_cost_usd":0.01,"duration_ms":1000}` + "\n"

	fr := runner.NewFake()
	fr.WriteToOutput = []byte(streamJSON)

	c := config{}
	iss := issue{number: "55", title: "heartbeat test"}
	if err := runOne(c, dir, fr, iss); err != nil {
		t.Fatalf("runOne: %v", err)
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

// TestRunOneHeartbeatEmitsToStdout verifies that when a box writes stream-json
// events, heartbeat lines appear on the launcher's stdout (captured via pipe).
func TestRunOneHeartbeatEmitsToStdout(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Capture stdout.
	origStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	streamJSON := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}]}}` + "\n" +
		`{"type":"result","num_turns":5,"duration_ms":2000}` + "\n"

	fr := runner.NewFake()
	fr.WriteToOutput = []byte(streamJSON)

	c := config{}
	iss := issue{number: "99", title: "heartbeat stdout test"}
	runErr := runOne(c, dir, fr, iss)

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

	if runErr != nil {
		t.Fatalf("runOne: %v", runErr)
	}

	out := buf.String()
	if !strings.Contains(out, "#99") {
		t.Errorf("heartbeat missing issue prefix in stdout: %q", out)
	}
	if !strings.Contains(out, "Bash") {
		t.Errorf("heartbeat missing tool name in stdout: %q", out)
	}
}
