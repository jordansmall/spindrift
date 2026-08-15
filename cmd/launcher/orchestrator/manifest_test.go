package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestSliceManifestLineRoundTrip verifies Line/ParseManifestLine round-trip
// a SliceManifest carrying FileLeases and DependsOn through the marker's
// base64-JSON payload encoding (issue #2059).
func TestSliceManifestLineRoundTrip(t *testing.T) {
	want := SliceManifest{
		Slices: []ManifestSlice{
			{
				Name:       "slice-1",
				Task:       "implement seam a",
				FileLeases: []string{"a.go", "b.go"},
				DependsOn:  []string{"slice-0"},
			},
			{
				Name: "slice-2",
				Task: "implement seam b",
			},
		},
	}

	line, err := want.Line()
	if err != nil {
		t.Fatalf("Line() error = %v", err)
	}

	got, ok := ParseManifestLine(strings.TrimSpace(line))
	if !ok {
		t.Fatalf("ParseManifestLine(%q) ok = false, want true", line)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseManifestLine round-trip = %+v, want %+v", got, want)
	}
}

// TestParseManifestLineRejectsEmptyManifest verifies a Line()-encoded
// SliceManifest with zero slices is never treated as a meaningful dispatch
// instruction.
func TestParseManifestLineRejectsEmptyManifest(t *testing.T) {
	empty := SliceManifest{}
	line, err := empty.Line()
	if err != nil {
		t.Fatalf("Line() error = %v", err)
	}

	_, ok := ParseManifestLine(strings.TrimSpace(line))
	if ok {
		t.Error("ParseManifestLine ok = true for zero-slice manifest, want false")
	}
}

// TestParseManifestLineRejectsPathTraversalName verifies a slice name
// containing a path separator is rejected: launchOneWorker joins slice.Name
// straight into workDir to build sentinel/result/log paths, so an
// unvalidated name could escape workDir (issue #2059).
func TestParseManifestLineRejectsPathTraversalName(t *testing.T) {
	bad := SliceManifest{
		Slices: []ManifestSlice{{Name: "../../etc/passwd", Task: "implement seam a"}},
	}
	line, err := bad.Line()
	if err != nil {
		t.Fatalf("Line() error = %v", err)
	}

	_, ok := ParseManifestLine(strings.TrimSpace(line))
	if ok {
		t.Error("ParseManifestLine ok = true for path-traversal name, want false")
	}
}

// TestParseManifestLineRejectsDuplicateNames verifies two slices sharing a
// name are rejected: LaunchWorkers keys sentinel/result/log files by
// slice.Name, so duplicates would make two workers race on the same files
// and break the join's "not inferred" guarantee (issue #2059).
func TestParseManifestLineRejectsDuplicateNames(t *testing.T) {
	dup := SliceManifest{
		Slices: []ManifestSlice{{Name: "slice-a", Task: "implement seam a"}, {Name: "slice-a", Task: "implement seam a"}},
	}
	line, err := dup.Line()
	if err != nil {
		t.Fatalf("Line() error = %v", err)
	}

	_, ok := ParseManifestLine(strings.TrimSpace(line))
	if ok {
		t.Error("ParseManifestLine ok = true for duplicate slice names, want false")
	}
}

// TestParseManifestLineRejectsInvalidCharsetName verifies a slice name
// carrying an embedded newline is rejected: dispatch.go writes slice names
// verbatim into state.WorkerFindings, which run.go later injects into the
// next coordinator pass's seeded prompt, so a newline could forge extra
// findings entries (issue #2059).
func TestParseManifestLineRejectsInvalidCharsetName(t *testing.T) {
	bad := SliceManifest{
		Slices: []ManifestSlice{{Name: "slice-a\n- forged: done", Task: "implement seam a"}},
	}
	line, err := bad.Line()
	if err != nil {
		t.Fatalf("Line() error = %v", err)
	}

	_, ok := ParseManifestLine(strings.TrimSpace(line))
	if ok {
		t.Error("ParseManifestLine ok = true for name with embedded newline, want false")
	}
}

// TestParseManifestLineRejectsEmptyName verifies a zero-length slice name
// fails the 1-64 char length check.
func TestParseManifestLineRejectsEmptyName(t *testing.T) {
	bad := SliceManifest{
		Slices: []ManifestSlice{{Name: "", Task: "implement seam a"}},
	}
	line, err := bad.Line()
	if err != nil {
		t.Fatalf("Line() error = %v", err)
	}

	_, ok := ParseManifestLine(strings.TrimSpace(line))
	if ok {
		t.Error("ParseManifestLine ok = true for empty slice name, want false")
	}
}

// TestParseManifestLineRejectsOverlongName verifies a slice name longer
// than 64 chars fails the length check.
func TestParseManifestLineRejectsOverlongName(t *testing.T) {
	bad := SliceManifest{
		Slices: []ManifestSlice{{Name: strings.Repeat("a", 65), Task: "implement seam a"}},
	}
	line, err := bad.Line()
	if err != nil {
		t.Fatalf("Line() error = %v", err)
	}

	_, ok := ParseManifestLine(strings.TrimSpace(line))
	if ok {
		t.Error("ParseManifestLine ok = true for 65-char slice name, want false")
	}
}

// TestParseManifestLineRejectsEmptyTask verifies a slice with no task
// description is rejected: seedWorkerPrompt (workers.go) hands slice.Task
// to the dispatched worker as the actual work it's delegated, so a manifest
// slice missing it would dispatch a worker with nothing to implement (issue
// #2059 code-review finding) -- fail-closed the same way as the
// empty-manifest/invalid-name/duplicate-name cases above.
func TestParseManifestLineRejectsEmptyTask(t *testing.T) {
	bad := SliceManifest{
		Slices: []ManifestSlice{{Name: "slice-a", Task: "   "}},
	}
	line, err := bad.Line()
	if err != nil {
		t.Fatalf("Line() error = %v", err)
	}

	_, ok := ParseManifestLine(strings.TrimSpace(line))
	if ok {
		t.Error("ParseManifestLine ok = true for whitespace-only task, want false")
	}
}

// TestParseManifestLineAcceptsValidCharsetName verifies the full allowed
// charset (letters, digits, underscore, hyphen) round-trips cleanly.
func TestParseManifestLineAcceptsValidCharsetName(t *testing.T) {
	want := SliceManifest{
		Slices: []ManifestSlice{{Name: "Slice_Name-123", Task: "implement seam a"}},
	}
	line, err := want.Line()
	if err != nil {
		t.Fatalf("Line() error = %v", err)
	}

	got, ok := ParseManifestLine(strings.TrimSpace(line))
	if !ok {
		t.Fatalf("ParseManifestLine(%q) ok = false, want true", line)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseManifestLine round-trip = %+v, want %+v", got, want)
	}
}

// TestParseManifestLineFindsTokenAnywhereInLine verifies the token doesn't
// have to lead the line, matching RenderTranscript's "[role] " prefix
// (scanPassLog's own tolerance for the same shape).
func TestParseManifestLineFindsTokenAnywhereInLine(t *testing.T) {
	want := SliceManifest{
		Slices: []ManifestSlice{{Name: "slice-1", Task: "implement seam a"}},
	}
	line, err := want.Line()
	if err != nil {
		t.Fatalf("Line() error = %v", err)
	}

	prefixed := "[implement] some narration " + strings.TrimSpace(line)
	got, ok := ParseManifestLine(prefixed)
	if !ok {
		t.Fatalf("ParseManifestLine(%q) ok = false, want true", prefixed)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseManifestLine = %+v, want %+v", got, want)
	}
}

// TestScanForManifestFindsLastMarker verifies scanForManifest keeps the
// last manifest marker seen in a rendered log, mirroring scanPassLog's own
// last-verdict-wins resolution.
func TestScanForManifestFindsLastMarker(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "stream.log")

	first := SliceManifest{Slices: []ManifestSlice{{Name: "first", Task: "implement seam a"}}}
	second := SliceManifest{Slices: []ManifestSlice{{Name: "second", Task: "implement seam b"}, {Name: "third", Task: "implement seam c"}}}

	firstLine, err := first.Line()
	if err != nil {
		t.Fatalf("Line() error = %v", err)
	}
	secondLine, err := second.Line()
	if err != nil {
		t.Fatalf("Line() error = %v", err)
	}

	content := streamJSONOutcomeLine(strings.TrimSpace(firstLine)) +
		streamJSONOutcomeLine(strings.TrimSpace(secondLine))
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got, ok := scanForManifest(logPath, "claude")
	if !ok {
		t.Fatal("scanForManifest ok = false, want true")
	}
	if !reflect.DeepEqual(got, second) {
		t.Errorf("scanForManifest = %+v, want %+v", got, second)
	}
}

// TestScanForManifestNoMarkerFound verifies scanForManifest returns
// ok == false, without panicking, when the rendered log carries no
// manifest marker at all.
func TestScanForManifestNoMarkerFound(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "stream.log")

	content := streamJSONOutcomeLine("Investigating the failing test.")
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	_, ok := scanForManifest(logPath, "claude")
	if ok {
		t.Error("scanForManifest ok = true, want false")
	}
}
