package driver

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver/driverkit"
)

func TestNewSelectsOpencodeByName(t *testing.T) {
	d, err := New("opencode")
	if err != nil {
		t.Fatalf("New(opencode): %v", err)
	}
	if d.Name() != "opencode" {
		t.Errorf("Name(): got %q, want %q", d.Name(), "opencode")
	}
}

// The raw sink must stay byte-exact, which is what opencode.New promises at
// the Driver seam (issue #2092).
func TestOpencodeDriverHeartbeatWriterForwardsRaw(t *testing.T) {
	d, err := New("opencode")
	if err != nil {
		t.Fatalf("New(opencode): %v", err)
	}

	var raw, out bytes.Buffer
	w := d.NewHeartbeatWriter(&raw, "77", &out, driverkit.RenderOptions{})

	ndjson := `{"type":"text","part":{"text":"doing the thing"}}` + "\n"
	if _, err := w.Write([]byte(ndjson)); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if raw.String() != ndjson {
		t.Errorf("raw not byte-exact:\ngot:  %q\nwant: %q", raw.String(), ndjson)
	}
	if !strings.Contains(out.String(), "#77") {
		t.Errorf("heartbeat output missing issue prefix: %q", out.String())
	}
	if !strings.Contains(out.String(), "doing the thing") {
		t.Errorf("heartbeat output missing text event's first line: %q", out.String())
	}
}

// opencode's transcript carries no role attribution, so topLevelRole must
// change nothing (issue #2092).
func TestOpencodeDriverHeartbeatWriterIgnoresTopLevelRole(t *testing.T) {
	d, err := New("opencode")
	if err != nil {
		t.Fatalf("New(opencode): %v", err)
	}

	var raw, out bytes.Buffer
	w := d.NewHeartbeatWriter(&raw, "77", &out, driverkit.RenderOptions{TopLevelRole: "reviewer"})

	ndjson := `{"type":"text","part":{"text":"doing the thing"}}` + "\n"
	if _, err := w.Write([]byte(ndjson)); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if raw.String() != ndjson {
		t.Errorf("raw not byte-exact:\ngot:  %q\nwant: %q", raw.String(), ndjson)
	}
	if !strings.Contains(out.String(), "#77") {
		t.Errorf("heartbeat output missing issue prefix: %q", out.String())
	}
}

// The passed-in exitCode is deliberately non-zero: opencode's own exit code
// is never trustworthy, so only the log decides (issue #2263).
func TestOpencodeDriverResolveExitValidOutcomeNoError_IsZero(t *testing.T) {
	d, err := New("opencode")
	if err != nil {
		t.Fatalf("New(opencode): %v", err)
	}

	dir := t.TempDir()
	logPath := filepath.Join(dir, "issue-1.log")
	line := `{"type":"text","part":{"text":"SPINDRIFT_OUTCOME issue=42 landing=https://example/pr/1 status=ready note=done"}}`
	if err := os.WriteFile(logPath, []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := d.ResolveExit(logPath, 17)
	if err != nil {
		t.Fatalf("ResolveExit: %v", err)
	}
	if got != 0 {
		t.Errorf("ResolveExit: got %d, want 0", got)
	}
}

// opencode exits 0 even on a mid-run error, so the passed-in exitCode is 0
// here and the log is the only trustworthy source (issue #2263).
func TestOpencodeDriverResolveExitErrorEvent_IsNonZero(t *testing.T) {
	d, err := New("opencode")
	if err != nil {
		t.Fatalf("New(opencode): %v", err)
	}

	dir := t.TempDir()
	logPath := filepath.Join(dir, "issue-1.log")
	lines := []string{
		`{"type":"text","part":{"text":"SPINDRIFT_OUTCOME issue=42 landing=https://example/pr/1 status=ready note=done"}}`,
		`{"type":"error","error":"boom"}`,
	}
	if err := os.WriteFile(logPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := d.ResolveExit(logPath, 0)
	if err != nil {
		t.Fatalf("ResolveExit: %v", err)
	}
	if got == 0 {
		t.Errorf("ResolveExit: got 0, want non-zero")
	}
}

func TestOpencodeDriverResolveExitMissingLog_IsNonZero(t *testing.T) {
	d, err := New("opencode")
	if err != nil {
		t.Fatalf("New(opencode): %v", err)
	}

	logPath := filepath.Join(t.TempDir(), "does-not-exist.log")
	got, err := d.ResolveExit(logPath, 0)
	if err != nil {
		t.Fatalf("ResolveExit: %v", err)
	}
	if got == 0 {
		t.Errorf("ResolveExit: got 0, want non-zero")
	}
}

// The error event between the two text events pins that RenderTranscript
// joins only text bodies.
func TestOpencodeDriverRenderTranscript(t *testing.T) {
	d, err := New("opencode")
	if err != nil {
		t.Fatalf("New(opencode): %v", err)
	}

	dir := t.TempDir()
	logPath := filepath.Join(dir, "issue-1.log")
	lines := []string{
		`{"type":"text","part":{"text":"Investigating the issue."}}`,
		`{"type":"error","error":"rate_limit_error"}`,
		`{"type":"text","part":{"text":"Filed a fix."}}`,
	}
	if err := os.WriteFile(logPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := d.RenderTranscript(logPath, driverkit.RenderOptions{})
	if err != nil {
		t.Fatalf("RenderTranscript: %v", err)
	}
	want := "Investigating the issue.\nFiled a fix."
	if got != want {
		t.Errorf("RenderTranscript = %q, want %q", got, want)
	}
}
