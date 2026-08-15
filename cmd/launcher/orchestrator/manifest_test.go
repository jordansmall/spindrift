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
				FileLeases: []string{"a.go", "b.go"},
				DependsOn:  []string{"slice-0"},
			},
			{
				Name: "slice-2",
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

// TestParseManifestLineFindsTokenAnywhereInLine verifies the token doesn't
// have to lead the line, matching RenderTranscript's "[role] " prefix
// (scanPassLog's own tolerance for the same shape).
func TestParseManifestLineFindsTokenAnywhereInLine(t *testing.T) {
	want := SliceManifest{
		Slices: []ManifestSlice{{Name: "slice-1"}},
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

	first := SliceManifest{Slices: []ManifestSlice{{Name: "first"}}}
	second := SliceManifest{Slices: []ManifestSlice{{Name: "second"}, {Name: "third"}}}

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
