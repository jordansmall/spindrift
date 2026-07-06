package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFilterPassesRawBytesUnchanged verifies that stdin is forwarded to stdout
// byte-for-byte so the launcher's capture channel remains byte-exact.
func TestFilterPassesRawBytesUnchanged(t *testing.T) {
	input := `{"type":"system","session_id":"s1"}` + "\n"
	var stdout bytes.Buffer
	heartbeatFile := filepath.Join(t.TempDir(), "heartbeat.log")
	if err := run("42", heartbeatFile, bytes.NewBufferString(input), &stdout); err != nil {
		t.Fatalf("run: %v", err)
	}
	if stdout.String() != input {
		t.Errorf("stdout: got %q, want %q", stdout.String(), input)
	}
}

// TestFilterWritesHeartbeatToFile verifies that tool_use events in stream-json
// produce coarse heartbeat lines in the output file.
func TestFilterWritesHeartbeatToFile(t *testing.T) {
	input := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"main.go"}}]}}` + "\n"
	var stdout bytes.Buffer
	heartbeatFile := filepath.Join(t.TempDir(), "heartbeat.log")
	if err := run("7", heartbeatFile, bytes.NewBufferString(input), &stdout); err != nil {
		t.Fatalf("run: %v", err)
	}
	got, err := os.ReadFile(heartbeatFile)
	if err != nil {
		t.Fatalf("read heartbeat: %v", err)
	}
	content := string(got)
	if !strings.Contains(content, "#7") {
		t.Errorf("heartbeat missing issue prefix: %q", content)
	}
	if !strings.Contains(content, "Edit") {
		t.Errorf("heartbeat missing tool name: %q", content)
	}
}

// TestFilterHeartbeatIsNotRawJSON verifies that the heartbeat file does not
// contain raw stream-json — its content is human-readable status lines only.
func TestFilterHeartbeatIsNotRawJSON(t *testing.T) {
	input := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}]}}` + "\n" +
		`{"type":"result","num_turns":3,"total_cost_usd":0.01,"duration_ms":1000}` + "\n"
	var stdout bytes.Buffer
	heartbeatFile := filepath.Join(t.TempDir(), "heartbeat.log")
	if err := run("5", heartbeatFile, bytes.NewBufferString(input), &stdout); err != nil {
		t.Fatalf("run: %v", err)
	}
	got, err := os.ReadFile(heartbeatFile)
	if err != nil {
		t.Fatalf("read heartbeat: %v", err)
	}
	content := string(got)
	if strings.Contains(content, `"type":`) {
		t.Errorf("heartbeat file contains raw JSON: %q", content)
	}
}
