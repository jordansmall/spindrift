package console

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/driver"
)

// TestDrillIn_SinglePass_RendersWithBoundary verifies DrillIn loads the
// initial run's log, renders it through the given Driver, and marks the
// single pass boundary — the base case before any fix/conflict pass exists.
func TestDrillIn_SinglePass_RendersWithBoundary(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "logs", "issue-42.log"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}

	drv, err := driver.New("claude")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}

	msg := DrillIn(drv, dir, "42")
	got, ok := msg.(DrillInMsg)
	if !ok {
		t.Fatalf("DrillIn() = %T, want DrillInMsg", msg)
	}
	if got.Err != nil {
		t.Fatalf("Err = %v, want nil", got.Err)
	}
	want := "=== pass: initial ===\n[implementor] hi\n"
	if got.Rendered != want {
		t.Errorf("Rendered = %q, want %q", got.Rendered, want)
	}
	if got.Raw != "=== pass: initial ===\n"+line {
		t.Errorf("Raw = %q, want the boundary plus the byte-exact log", got.Raw)
	}
}

// TestDrillIn_ControlSequences_StrippedFromRendered verifies ANSI/control
// sequences embedded in untrusted model text reach the rendered pane
// stripped, while the raw byte-exact copy keeps them intact (#721).
func TestDrillIn_ControlSequences_StrippedFromRendered(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	text := "\x1b[31mred\x1b[0m text\x07"
	textJSON, err := json.Marshal(text)
	if err != nil {
		t.Fatal(err)
	}
	line := `{"type":"assistant","message":{"content":[{"type":"text","text":` + string(textJSON) + `}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "logs", "issue-42.log"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}

	drv, err := driver.New("claude")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}

	msg := DrillIn(drv, dir, "42")
	got, ok := msg.(DrillInMsg)
	if !ok {
		t.Fatalf("DrillIn() = %T, want DrillInMsg", msg)
	}
	if got.Err != nil {
		t.Fatalf("Err = %v, want nil", got.Err)
	}
	want := "=== pass: initial ===\n[implementor] red text\n"
	if got.Rendered != want {
		t.Errorf("Rendered = %q, want %q", got.Rendered, want)
	}
	if got.Raw != "=== pass: initial ===\n"+line {
		t.Errorf("Raw = %q, want the boundary plus the byte-exact log, escape sequences untouched", got.Raw)
	}
}

// TestDrillIn_MultiplePasses_ConcatenatesInOrderWithBoundaries verifies an
// initial run plus a fix pass render as one transcript spanning both, in
// chronological order, each with its own boundary marker (#648 AC3).
func TestDrillIn_MultiplePasses_ConcatenatesInOrderWithBoundaries(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	initial := `{"type":"assistant","message":{"content":[{"type":"text","text":"first pass"}]}}` + "\n"
	fix := `{"type":"assistant","message":{"content":[{"type":"text","text":"fix pass"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "logs", "issue-42.log"), []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "logs", "issue-42-fix-1.log"), []byte(fix), 0o644); err != nil {
		t.Fatal(err)
	}

	drv, err := driver.New("claude")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}

	got, ok := DrillIn(drv, dir, "42").(DrillInMsg)
	if !ok {
		t.Fatal("DrillIn() did not return a DrillInMsg")
	}
	want := "=== pass: initial ===\n[implementor] first pass\n=== pass: fix-1 ===\n[implementor] fix pass\n"
	if got.Rendered != want {
		t.Errorf("Rendered = %q, want %q", got.Rendered, want)
	}
}

// TestSidebarTranscriptCache_UnchangedStat_SkipsReDrillIn verifies a second
// Refresh against the same, untouched pass logs returns the cached render
// rather than re-running DrillIn — the Transcript analogue of
// SidebarActivityCache's own stat-based skip (issue #1736).
func TestSidebarTranscriptCache_UnchangedStat_SkipsReDrillIn(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "logs", "issue-42.log"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}

	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	cache := NewSidebarTranscriptCache()

	rendered1, raw1, ok := cache.Refresh(drv, dir, "42")
	if !ok {
		t.Fatal("first Refresh: ok = false, want true")
	}
	want := "=== pass: initial ===\n[implementor] hi\n"
	if rendered1 != want {
		t.Errorf("first Refresh: Rendered = %q, want %q", rendered1, want)
	}

	rendered2, raw2, ok := cache.Refresh(drv, dir, "42")
	if !ok {
		t.Fatal("second Refresh: ok = false, want true")
	}
	if rendered2 != rendered1 || raw2 != raw1 {
		t.Errorf("second Refresh() = (%q, %q), want the cached (%q, %q) unchanged", rendered2, raw2, rendered1, raw1)
	}
}

// TestDrillIn_NoLogsOnDisk_ReturnsErr verifies drilling into an issue with
// no Dispatch history yet surfaces an error instead of an empty transcript
// that could be mistaken for a Dispatch that ran and said nothing.
func TestDrillIn_NoLogsOnDisk_ReturnsErr(t *testing.T) {
	dir := t.TempDir()
	drv, err := driver.New("claude")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}

	got, ok := DrillIn(drv, dir, "999").(DrillInMsg)
	if !ok {
		t.Fatal("DrillIn() did not return a DrillInMsg")
	}
	if got.Err == nil {
		t.Error("Err = nil, want an error for an issue with no logs on disk")
	}
}
