package driver

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver/driverkit"
)

// TestNewSelectsOpencodeByName verifies that New("opencode") resolves to the
// opencode strategy, the mirror of TestNewDefaultsToClaude for the non-default
// driver selected explicitly by name.
func TestNewSelectsOpencodeByName(t *testing.T) {
	d, err := New("opencode")
	if err != nil {
		t.Fatalf("New(opencode): %v", err)
	}
	if d.Name() != "opencode" {
		t.Errorf("Name(): got %q, want %q", d.Name(), "opencode")
	}
}

// TestOpencodeDriverHeartbeatWriterForwardsRaw verifies that the opencode
// Driver's heartbeat writer passes all bytes to the raw sink unchanged while
// also emitting a heartbeat line to out, matching opencode.New's contract at
// the Driver seam (issue #2092: topLevelRole ignored).
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

// TestOpencodeDriverHeartbeatWriterIgnoresTopLevelRole verifies that a
// non-empty topLevelRole has no effect on the opencode Driver's heartbeat
// writer: opencode's transcript carries no role attribution (issue #2092),
// so raw is still forwarded unchanged and a heartbeat still emitted.
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

// TestOpencodeDriverResolveExitValidOutcomeNoError_IsZero verifies that the
// opencode Driver's ResolveExit derives 0 from a log carrying a valid
// SPINDRIFT_OUTCOME line with no type:"error" event, even when the passed-in
// exitCode is non-zero — opencode's own exit code is never trustworthy
// (issue #2263).
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

// TestOpencodeDriverResolveExitErrorEvent_IsNonZero verifies that the
// opencode Driver's ResolveExit derives a non-zero code from a log carrying
// a type:"error" event, even when the passed-in exitCode is zero — opencode
// exits 0 even on a mid-run error, so the log is the only trustworthy
// source (issue #2263).
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

// TestOpencodeDriverResolveExitMissingLog_IsNonZero verifies that the
// opencode Driver's ResolveExit derives a non-zero code from a missing log
// file, even when the passed-in exitCode is zero.
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

// TestOpencodeDriverRenderTranscript verifies the opencode Driver's
// RenderTranscript delegates to the opencode subpackage's strategy: joined
// type:"text" part.text bodies, excluding non-text events.
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
