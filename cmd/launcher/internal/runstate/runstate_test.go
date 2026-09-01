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

// ReviewFindings (issue #2037) is the review pass's own Blocking/Non-blocking
// findings text, distinct from the bare LastVerdict word.
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

// PassSummaryPath (issue #2549) points at the most recent implement/fix pass's
// free-form summary of what it did and what remains.
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

// DispositionsPath (issue #2550) points at the fix pass's per-finding
// dispositions file.
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

// DispositionsLogPath (issue #2550) is the per-run, append-only dispositions
// log seedReviewPromptFromState reads, mirroring FindingsLogPath's convention.
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

// ReviewedCommitAnchor (issue #2551) is the git commit SHA the orchestrator's
// repo workdir was at when the most recent review pass ran.
func TestRunStateRoundTripIncludesReviewedCommitAnchor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run-state.json")
	want := RunState{
		LastVerdict:          "BLOCK",
		ReviewedCommitAnchor: "0123456789abcdef0123456789abcdef01234567",
	}

	if err := WriteRunState(path, want); err != nil {
		t.Fatalf("WriteRunState: %v", err)
	}
	got, err := ReadRunState(path)
	if err != nil {
		t.Fatalf("ReadRunState: %v", err)
	}
	if got.ReviewedCommitAnchor != want.ReviewedCommitAnchor {
		t.Errorf("ReviewedCommitAnchor = %q, want %q", got.ReviewedCommitAnchor, want.ReviewedCommitAnchor)
	}
}

// DecisionsPath (issue #2695) points at the implement/fix pass's per-decision
// file.
func TestRunStateRoundTripIncludesDecisionsPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run-state.json")
	want := RunState{
		LastVerdict:   "BLOCK",
		DecisionsPath: "/tmp/decisions.md",
	}

	if err := WriteRunState(path, want); err != nil {
		t.Fatalf("WriteRunState: %v", err)
	}
	got, err := ReadRunState(path)
	if err != nil {
		t.Fatalf("ReadRunState: %v", err)
	}
	if got.DecisionsPath != want.DecisionsPath {
		t.Errorf("DecisionsPath = %q, want %q", got.DecisionsPath, want.DecisionsPath)
	}
}

// DecisionsLogPath (issue #2695) is the per-run, append-only decisions log
// seedPromptFromState reads, mirroring DispositionsLogPath's convention.
func TestRunStateRoundTripIncludesDecisionsLogPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run-state.json")
	want := RunState{
		LastVerdict:      "BLOCK",
		DecisionsLogPath: "/tmp/decisions-log.md",
	}

	if err := WriteRunState(path, want); err != nil {
		t.Fatalf("WriteRunState: %v", err)
	}
	got, err := ReadRunState(path)
	if err != nil {
		t.Fatalf("ReadRunState: %v", err)
	}
	if got.DecisionsLogPath != want.DecisionsLogPath {
		t.Errorf("DecisionsLogPath = %q, want %q", got.DecisionsLogPath, want.DecisionsLogPath)
	}
}

// The pass-one production path (issue #1997): --state-file defaults to a fixed
// tmp path that has never been written. Treating that as an error would fail
// the orchestrator's first invocation on any box before it reached driver-exec.
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

// An empty path is the caller's way of disabling the artifact entirely.
func TestReadRunStateEmptyPathReturnsZeroValue(t *testing.T) {
	got, err := ReadRunState("")
	if err != nil {
		t.Fatalf("ReadRunState: %v", err)
	}
	if !reflect.DeepEqual(got, RunState{}) {
		t.Errorf("ReadRunState(\"\") = %+v, want zero value", got)
	}
}

// A reader holding the old file open must keep seeing the old, complete
// content after a new write lands, because the new content only becomes
// visible at path via a rename swap (issue #2008). A kill mid-write must leave
// either the old file or an orphaned temp file -- never a half-written file.
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

// The fixture is hand-written rather than produced via WriteRunState: it
// stands in for a state file a prior pass wrote before this package existed,
// so every field the pre-extraction orchestrator wrote must still load.
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

// A partial write from a killed prior pass must surface as an error rather
// than silently discarding the handoff data the file was meant to carry.
func TestReadRunStateCorruptFileReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run-state.json")
	if err := os.WriteFile(path, []byte("{not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRunState(path); err == nil {
		t.Error("ReadRunState of a corrupt file: got nil error, want one")
	}
}

// DoneSlices/RemainingSlices are deliberately absent from the non-empty cases
// (issue #2552): they are dispatch-internal bookkeeping only (issue #2059's
// dedup mechanism in orchestrator/dispatch.go), never rendered by
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
	// ReviewedCommitAnchor, same reasoning: only seedReviewPromptFromState
	// (issue #2551) reads it.
	if !(RunState{ReviewedCommitAnchor: "0123456789abcdef0123456789abcdef01234567"}).IsEmpty() {
		t.Error("IsEmpty() of a state with only ReviewedCommitAnchor set = false, want true")
	}
	// DecisionsPath/DecisionsLogPath, same reasoning (issue #2695):
	// seedPromptFromState does its own fresh read of DecisionsLogPath's file
	// before its early-return, so neither field renders off IsEmpty alone.
	if !(RunState{DecisionsPath: "/tmp/decisions.md"}).IsEmpty() {
		t.Error("IsEmpty() of a state with only DecisionsPath set = false, want true")
	}
	if !(RunState{DecisionsLogPath: "/tmp/decisions-log.md"}).IsEmpty() {
		t.Error("IsEmpty() of a state with only DecisionsLogPath set = false, want true")
	}
	nonEmpty := []RunState{
		{LastVerdict: "BLOCK"},
		{ScoutBriefPath: "/tmp/brief.md"},
		{PassSummaryPath: "/tmp/pass-summary.md"},
		{ReviewFindings: "some finding"},
		{FindingsLogPath: "/tmp/findings.md"},
		{TerminalLand: true},
	}
	for _, s := range nonEmpty {
		if s.IsEmpty() {
			t.Errorf("IsEmpty() of %+v = true, want false", s)
		}
	}
}
