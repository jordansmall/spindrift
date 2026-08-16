package runstate

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestRunStateRoundTrip verifies WriteRunState followed by ReadRunState
// reproduces every documented field unchanged (issue #1997) -- the schema a
// fresh implementor pass depends on to continue without a transcript.
func TestRunStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run-state.json")
	want := RunState{
		DoneSlices:      []string{"scout", "implement seam A"},
		RemainingSlices: []string{"implement seam B", "land"},
		LastVerdict:     "BLOCK",
		ScoutBriefPath:  "/tmp/brief.md",
	}

	if err := WriteRunState(path, want); err != nil {
		t.Fatalf("WriteRunState: %v", err)
	}
	got, err := ReadRunState(path)
	if err != nil {
		t.Fatalf("ReadRunState: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

// TestRunStateRoundTripIncludesReviewFindings verifies ReviewFindings (issue
// #2037: the code-owned review pass's own Blocking/Non-blocking findings
// text, distinct from the bare LastVerdict word) survives a
// WriteRunState/ReadRunState round trip like every other field.
func TestRunStateRoundTripIncludesReviewFindings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run-state.json")
	want := RunState{
		LastVerdict:    "BLOCK",
		ReviewFindings: "## Blocking\n- run.go:42 -- missing nil check\n\n## Non-blocking\n- none",
	}

	if err := WriteRunState(path, want); err != nil {
		t.Fatalf("WriteRunState: %v", err)
	}
	got, err := ReadRunState(path)
	if err != nil {
		t.Fatalf("ReadRunState: %v", err)
	}
	if got.ReviewFindings != want.ReviewFindings {
		t.Errorf("ReviewFindings = %q, want %q", got.ReviewFindings, want.ReviewFindings)
	}
}

// TestRunStateRoundTripIncludesWorkerFindings verifies WorkerFindings (issue
// #2059: the last parallel worker dispatch's per-slice outcome summary,
// distinct from ReviewFindings) survives a WriteRunState/ReadRunState round
// trip like every other field.
func TestRunStateRoundTripIncludesWorkerFindings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run-state.json")
	want := RunState{
		LastVerdict:    "BLOCK",
		WorkerFindings: "- slice-a: done\n- slice-b: crashed (exit 1)",
	}

	if err := WriteRunState(path, want); err != nil {
		t.Fatalf("WriteRunState: %v", err)
	}
	got, err := ReadRunState(path)
	if err != nil {
		t.Fatalf("ReadRunState: %v", err)
	}
	if got.WorkerFindings != want.WorkerFindings {
		t.Errorf("WorkerFindings = %q, want %q", got.WorkerFindings, want.WorkerFindings)
	}
}

// TestRunStateRoundTripIncludesPassSummaryPath verifies PassSummaryPath
// (issue #2549: the most recent implement/fix pass's own free-form summary
// of what it did and what remains, referenced by path like ScoutBriefPath)
// survives a WriteRunState/ReadRunState round trip like every other field.
func TestRunStateRoundTripIncludesPassSummaryPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run-state.json")
	want := RunState{
		LastVerdict:     "BLOCK",
		PassSummaryPath: "/tmp/pass-summary.md",
	}

	if err := WriteRunState(path, want); err != nil {
		t.Fatalf("WriteRunState: %v", err)
	}
	got, err := ReadRunState(path)
	if err != nil {
		t.Fatalf("ReadRunState: %v", err)
	}
	if got.PassSummaryPath != want.PassSummaryPath {
		t.Errorf("PassSummaryPath = %q, want %q", got.PassSummaryPath, want.PassSummaryPath)
	}
}

// TestRunStateRoundTripIncludesDispositionsPath verifies DispositionsPath
// (issue #2550: the fix pass's own per-finding dispositions file, referenced
// by path like PassSummaryPath) survives a WriteRunState/ReadRunState round
// trip like every other field.
func TestRunStateRoundTripIncludesDispositionsPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run-state.json")
	want := RunState{
		LastVerdict:      "BLOCK",
		DispositionsPath: "/tmp/dispositions.md",
	}

	if err := WriteRunState(path, want); err != nil {
		t.Fatalf("WriteRunState: %v", err)
	}
	got, err := ReadRunState(path)
	if err != nil {
		t.Fatalf("ReadRunState: %v", err)
	}
	if got.DispositionsPath != want.DispositionsPath {
		t.Errorf("DispositionsPath = %q, want %q", got.DispositionsPath, want.DispositionsPath)
	}
}

// TestRunStateRoundTripIncludesDispositionsLogPath verifies
// DispositionsLogPath (issue #2550: the per-run, append-only dispositions
// log seedReviewPromptFromState reads, mirroring FindingsLogPath's own
// convention) survives a WriteRunState/ReadRunState round trip like every
// other field.
func TestRunStateRoundTripIncludesDispositionsLogPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run-state.json")
	want := RunState{
		LastVerdict:         "BLOCK",
		DispositionsLogPath: "/tmp/dispositions-log.md",
	}

	if err := WriteRunState(path, want); err != nil {
		t.Fatalf("WriteRunState: %v", err)
	}
	got, err := ReadRunState(path)
	if err != nil {
		t.Fatalf("ReadRunState: %v", err)
	}
	if got.DispositionsLogPath != want.DispositionsLogPath {
		t.Errorf("DispositionsLogPath = %q, want %q", got.DispositionsLogPath, want.DispositionsLogPath)
	}
}

// TestRunStateRoundTripIncludesUnlandedSlices verifies UnlandedSlices (issue
// #2060 review finding: DoneSlices names whose branch integration ended in
// conflict or failure) survives a WriteRunState/ReadRunState round trip like
// every other field.
func TestRunStateRoundTripIncludesUnlandedSlices(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run-state.json")
	want := RunState{
		DoneSlices:     []string{"slice-a"},
		UnlandedSlices: []string{"slice-a"},
	}

	if err := WriteRunState(path, want); err != nil {
		t.Fatalf("WriteRunState: %v", err)
	}
	got, err := ReadRunState(path)
	if err != nil {
		t.Fatalf("ReadRunState: %v", err)
	}
	if !reflect.DeepEqual(got.UnlandedSlices, want.UnlandedSlices) {
		t.Errorf("UnlandedSlices = %v, want %v", got.UnlandedSlices, want.UnlandedSlices)
	}
}

// TestReadRunStateNoFileYetReturnsZeroValue verifies the actual pass-one
// production path (issue #1997): --state-file defaults to a fixed tmp path
// that has never been written, and ReadRunState must treat that as "no
// handoff yet", not an error, or the orchestrator's first invocation on any
// box would fail before ever reaching driver-exec.
func TestReadRunStateNoFileYetReturnsZeroValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "never-written.json")
	got, err := ReadRunState(path)
	if err != nil {
		t.Fatalf("ReadRunState: %v", err)
	}
	if !reflect.DeepEqual(got, RunState{}) {
		t.Errorf("ReadRunState of a missing file = %+v, want zero value", got)
	}
}

// TestReadRunStateEmptyPathReturnsZeroValue verifies an empty path (the
// caller's way of disabling the artifact entirely) is also a no-op read
// rather than an error.
func TestReadRunStateEmptyPathReturnsZeroValue(t *testing.T) {
	got, err := ReadRunState("")
	if err != nil {
		t.Fatalf("ReadRunState: %v", err)
	}
	if !reflect.DeepEqual(got, RunState{}) {
		t.Errorf("ReadRunState(\"\") = %+v, want zero value", got)
	}
}

// TestWriteRunStateIsAtomicViaRename verifies WriteRunState never truncates
// the existing file in place: a reader with the old file already open must
// keep seeing the old, complete content after a new write lands, because the
// new content only becomes visible at path via a rename swap, never via an
// in-place truncate (issue #2008). A kill mid-write must leave either the old
// file or an orphaned temp file -- never a half-written file at path.
func TestWriteRunStateIsAtomicViaRename(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run-state.json")
	old := RunState{LastVerdict: "OLD"}
	if err := WriteRunState(path, old); err != nil {
		t.Fatalf("WriteRunState(old): %v", err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open old file: %v", err)
	}
	defer f.Close()

	newState := RunState{LastVerdict: "NEW"}
	if err := WriteRunState(path, newState); err != nil {
		t.Fatalf("WriteRunState(new): %v", err)
	}

	staleRead, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read via still-open fd: %v", err)
	}
	var staleGot RunState
	if err := json.Unmarshal(staleRead, &staleGot); err != nil {
		t.Fatalf("stale content is not valid JSON (in-place truncate observed): %v", err)
	}
	if !reflect.DeepEqual(staleGot, old) {
		t.Errorf("still-open fd saw %+v, want unchanged old content %+v (in-place truncate observed)", staleGot, old)
	}

	got, err := ReadRunState(path)
	if err != nil {
		t.Fatalf("ReadRunState(new): %v", err)
	}
	if !reflect.DeepEqual(got, newState) {
		t.Errorf("ReadRunState after second write = %+v, want %+v", got, newState)
	}
}

// TestWriteRunStateLeavesNoTempFileOnSuccess verifies the temp file used to
// achieve atomicity doesn't linger next to the target once a write succeeds
// -- only the renamed-into-place run-state file should remain in the
// directory.
func TestWriteRunStateLeavesNoTempFileOnSuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run-state.json")

	if err := WriteRunState(path, RunState{LastVerdict: "OLD"}); err != nil {
		t.Fatalf("WriteRunState: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("dir contents = %v, want only %q (no orphaned temp file)", names, filepath.Base(path))
	}
}

// TestReadRunStateParsesPreExtractionFixture verifies a literal JSON fixture
// -- hand-written here rather than produced via WriteRunState, standing in
// for a state file a prior pass wrote before this package existed -- still
// loads correctly (AC2): every field the pre-extraction orchestrator wrote
// round-trips, not just the ones exercised elsewhere in this file.
func TestReadRunStateParsesPreExtractionFixture(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run-state.json")
	fixture := `{
  "done_slices": ["scout", "implement seam A"],
  "remaining_slices": ["implement seam B", "land"],
  "last_verdict": "BLOCK",
  "scout_brief_path": "/tmp/brief.md",
  "pass_summary_path": "/tmp/pass-summary.md",
  "review_findings": "## Blocking\n- run.go:42 -- missing nil check",
  "terminal_land": true,
  "cap_fired": "max slices reached"
}`
	if err := os.WriteFile(path, []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ReadRunState(path)
	if err != nil {
		t.Fatalf("ReadRunState: %v", err)
	}
	want := RunState{
		DoneSlices:      []string{"scout", "implement seam A"},
		RemainingSlices: []string{"implement seam B", "land"},
		LastVerdict:     "BLOCK",
		ScoutBriefPath:  "/tmp/brief.md",
		PassSummaryPath: "/tmp/pass-summary.md",
		ReviewFindings:  "## Blocking\n- run.go:42 -- missing nil check",
		TerminalLand:    true,
		CapFired:        "max slices reached",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReadRunState(fixture) = %+v, want %+v", got, want)
	}
}

// TestReadRunStateCorruptFileReturnsError verifies a state file that exists
// but fails to parse as JSON (a partial write from a killed prior pass, or
// hand-edited garbage) surfaces as an error rather than silently discarding
// whatever handoff data the file was supposed to carry.
func TestReadRunStateCorruptFileReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run-state.json")
	if err := os.WriteFile(path, []byte("{not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRunState(path); err == nil {
		t.Error("ReadRunState of a corrupt file: got nil error, want one")
	}
}

// TestRunStateIsEmpty verifies IsEmpty (issue #2552) reports true only for
// the zero value, and false the moment any single field -- including
// PassSummaryPath, the newest implementor-facing one -- carries a handoff.
// DoneSlices/RemainingSlices are deliberately absent from the non-empty
// cases: they are dispatch-internal bookkeeping only (issue #2059's dedup
// mechanism in orchestrator/dispatch.go), never rendered by
// seedPromptFromState, so on their own they must not gate IsEmpty's only
// caller into seeding a "Run-state handoff" block with no handoff to show
// (issue #2549).
func TestRunStateIsEmpty(t *testing.T) {
	if !(RunState{}).IsEmpty() {
		t.Error("IsEmpty() of the zero value = false, want true")
	}
	if !(RunState{DoneSlices: []string{"scout"}, RemainingSlices: []string{"land"}}).IsEmpty() {
		t.Error("IsEmpty() of a state with only DoneSlices/RemainingSlices set = false, want true")
	}
	// DispositionsPath joins DoneSlices/RemainingSlices in the "set but
	// still IsEmpty" case (issue #2550 review finding): seedPromptFromState
	// -- IsEmpty's only caller -- never renders it, so including it in the
	// non-empty check would make IsEmpty return false for a state with
	// nothing seedPromptFromState would actually put in the "Run-state
	// handoff" section. seedReviewPromptFromState's own, narrower check
	// governs the round-N review prompt DispositionsPath actually seeds.
	if !(RunState{DispositionsPath: "/tmp/dispositions.md"}).IsEmpty() {
		t.Error("IsEmpty() of a state with only DispositionsPath set = false, want true")
	}
	// DispositionsLogPath, same reasoning: only seedReviewPromptFromState
	// reads it.
	if !(RunState{DispositionsLogPath: "/tmp/dispositions-log.md"}).IsEmpty() {
		t.Error("IsEmpty() of a state with only DispositionsLogPath set = false, want true")
	}
	nonEmpty := []RunState{
		{LastVerdict: "BLOCK"},
		{ScoutBriefPath: "/tmp/brief.md"},
		{PassSummaryPath: "/tmp/pass-summary.md"},
		{ReviewFindings: "some finding"},
		{WorkerFindings: "slice-a: done"},
		{FindingsLogPath: "/tmp/findings.md"},
		{TerminalLand: true},
		{UnlandedSlices: []string{"slice-a"}},
	}
	for _, s := range nonEmpty {
		if s.IsEmpty() {
			t.Errorf("IsEmpty() of %+v = true, want false", s)
		}
	}
}
