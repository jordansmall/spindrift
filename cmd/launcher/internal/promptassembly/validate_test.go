package promptassembly

import (
	"os"
	"strings"
	"testing"
)

// TestValidateNoGatesActive covers the baseline no-op case: every gate the
// four validateMarkers rows key off of is off (kind "work", box write
// enabled, orchestrator off), so Validate must never reject or warn
// regardless of Prompt/AgentsJSON content.
func TestValidateNoGatesActive(t *testing.T) {
	e := Env{DispatchKind: "work", BoxWriteEnabled: true, OrchestratorEnabled: false}
	result := Result{Prompt: "no markers anywhere", AgentsJSON: ""}

	warnings, err := Validate(e, result, testValidateMarkerRows())
	if err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
	if warnings != nil {
		t.Fatalf("Validate() warnings = %v, want nil", warnings)
	}
}

// TestValidateReadOnlyResearchReject covers the verdict-comment-relay row: a
// research + read-only dispatch whose rendered prompt is missing
// SPINDRIFT_COMMENT must reject.
func TestValidateReadOnlyResearchReject(t *testing.T) {
	e := Env{DispatchKind: "research", BoxWriteEnabled: false}
	result := Result{Prompt: "research stub, no verdict-comment marker here"}

	warnings, err := Validate(e, result, testValidateMarkerRows())
	if err == nil {
		t.Fatal("Validate() error = nil, want non-nil")
	}
	mustContain(t, err.Error(), "SPINDRIFT_COMMENT")
	if warnings != nil {
		t.Fatalf("Validate() warnings = %v, want nil", warnings)
	}
}

// TestValidateReadOnlyResearchPass covers the same gate as above, but with
// SPINDRIFT_COMMENT present -- no reject.
func TestValidateReadOnlyResearchPass(t *testing.T) {
	e := Env{DispatchKind: "research", BoxWriteEnabled: false}
	result := Result{Prompt: "research stub\n\nPost your verdict with SPINDRIFT_COMMENT here"}

	_, err := Validate(e, result, testValidateMarkerRows())
	if err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

// TestValidateOrchestratorEnabledReject covers the reviewer-verdict row: the
// orchestrator on with a rendered review prompt missing VERDICT: must
// reject.
func TestValidateOrchestratorEnabledReject(t *testing.T) {
	e := Env{OrchestratorEnabled: true}
	result := Result{
		Handoff: Handoff{ReviewPromptFile: "reviewer stub, no verdict line here"},
	}

	warnings, err := Validate(e, result, testValidateMarkerRows())
	if err == nil {
		t.Fatal("Validate() error = nil, want non-nil")
	}
	mustContain(t, err.Error(), "VERDICT:")
	if warnings != nil {
		t.Fatalf("Validate() warnings = %v, want nil", warnings)
	}
}

// TestValidateOrchestratorEnabledNoFalsePositive covers the no-false-positive
// acceptance criterion (issue #2249 #3): when Handoff.ReviewPromptFile is
// empty (as when the orchestrator is off, or a research/fix-pass dispatch),
// the reviewer-verdict gate is never active regardless of content.
func TestValidateOrchestratorEnabledNoFalsePositive(t *testing.T) {
	e := Env{OrchestratorEnabled: true, BoxWriteEnabled: true}
	result := Result{Handoff: Handoff{ReviewPromptFile: ""}}

	warnings, err := Validate(e, result, testValidateMarkerRows())
	if err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
	if warnings != nil {
		t.Fatalf("Validate() warnings = %v, want nil", warnings)
	}
}

// TestValidateBoxAccessReadOnlyWarn covers the pr-intent row: read-only,
// non-research, prompt missing SPINDRIFT_PR_INTENT -- advisory only.
func TestValidateBoxAccessReadOnlyWarn(t *testing.T) {
	e := Env{DispatchKind: "work", BoxWriteEnabled: false}
	result := Result{Prompt: "issue stub, no PR-intent marker here"}

	warnings, err := Validate(e, result, testValidateMarkerRows())
	if err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("Validate() warnings = %v, want exactly one entry", warnings)
	}
	mustContain(t, warnings[0], "SPINDRIFT_PR_INTENT")
}

// TestValidateFilerFileRelayWarn covers the issue-intent row: a filer-relay
// dispatch (filer configured, orchestrator on, read-only) whose filer prompt
// (extracted from AgentsJSON) is missing SPINDRIFT_ISSUE_INTENT -- advisory
// only.
func TestValidateFilerFileRelayWarn(t *testing.T) {
	e := Env{
		DispatchKind:        "work",
		BoxWriteEnabled:     false,
		OrchestratorEnabled: true,
		AgentsJSONTemplate:  `{"filer":{"model":"m"}}`,
	}
	result := Result{
		Prompt:     "issue stub",
		AgentsJSON: `{"filer":{"prompt":"no marker here"}}`,
	}

	warnings, err := Validate(e, result, testValidateMarkerRows())
	if err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
	mustContain(t, strings.Join(warnings, "\n"), "SPINDRIFT_ISSUE_INTENT")
}

// TestValidateDataDrivenSeverity is the data-driven proof (issue #2318):
// patching the pr-intent row's Severity to "reject" (with the same gate-
// active-marker-missing scenario TestValidateBoxAccessReadOnlyWarn exercises)
// must flip Validate's outcome to a reject, proving it dispatches on
// row.Severity/row.When data rather than a hardcoded per-id switch.
func TestValidateDataDrivenSeverity(t *testing.T) {
	e := Env{DispatchKind: "work", BoxWriteEnabled: false}
	result := Result{Prompt: "issue stub, no PR-intent marker here"}

	rows := testValidateMarkerRows()
	for i := range rows {
		if rows[i].ID == "pr-intent" {
			rows[i].Severity = "reject"
		}
	}

	warnings, err := Validate(e, result, rows)
	if err == nil {
		t.Fatal("Validate() error = nil, want non-nil (severity patched to reject)")
	}
	mustContain(t, err.Error(), "SPINDRIFT_PR_INTENT")
	if warnings != nil {
		t.Fatalf("Validate() warnings = %v, want nil", warnings)
	}
}

// TestLoadValidateMarkersParsesAllRows round-trips testdata/validate-markers.json
// -- the hand transcription of lib/prompt-contract.nix's validateMarkers
// registry -- into []ValidateMarkerRow and asserts the decoded fields match.
func TestLoadValidateMarkersParsesAllRows(t *testing.T) {
	f, err := os.Open("testdata/validate-markers.json")
	if err != nil {
		t.Fatalf("open testdata/validate-markers.json: %v", err)
	}
	defer f.Close()

	rows, err := LoadValidateMarkers(f)
	if err != nil {
		t.Fatalf("LoadValidateMarkers: %v", err)
	}

	want := testValidateMarkerRows()
	if len(rows) != len(want) {
		t.Fatalf("len(rows) = %d, want %d", len(rows), len(want))
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Errorf("rows[%d] = %+v, want %+v", i, rows[i], want[i])
		}
	}
}

// TestLoadValidateMarkersMalformed covers the error path: invalid JSON must
// return a non-nil, wrapped error, never panic.
func TestLoadValidateMarkersMalformed(t *testing.T) {
	f, err := os.Open("testdata/malformed.json")
	if err != nil {
		t.Fatalf("open testdata/malformed.json: %v", err)
	}
	defer f.Close()

	if _, err := LoadValidateMarkers(f); err == nil {
		t.Fatal("LoadValidateMarkers(malformed) = nil error, want non-nil")
	}
}

// TestLoadValidateMarkersFileMalformed exercises LoadValidateMarkersFile's
// own error path alongside LoadValidateMarkers's.
func TestLoadValidateMarkersFileMalformed(t *testing.T) {
	if _, err := LoadValidateMarkersFile("testdata/malformed.json"); err == nil {
		t.Fatal("LoadValidateMarkersFile(malformed) = nil error, want non-nil")
	}
}

// TestLoadValidateMarkersFileNonexistent covers a nonexistent path: a
// wrapped, non-nil error, never a panic.
func TestLoadValidateMarkersFileNonexistent(t *testing.T) {
	if _, err := LoadValidateMarkersFile("testdata/does-not-exist.json"); err == nil {
		t.Fatal("LoadValidateMarkersFile(nonexistent) = nil error, want non-nil")
	}
}

// testValidateMarkerRows returns the four validateMarkers rows in
// lib/prompt-contract.nix's own order, for tests that don't need to load
// them from testdata/validate-markers.json.
func testValidateMarkerRows() []ValidateMarkerRow {
	return []ValidateMarkerRow{
		{ID: "verdict-comment-relay", Marker: "SPINDRIFT_COMMENT", Carrier: "fragment-body", Severity: "reject", When: "readOnlyResearch"},
		{ID: "reviewer-verdict", Marker: "VERDICT:", Carrier: "subagent-first-line", Severity: "reject", When: "orchestratorEnabled"},
		{ID: "pr-intent", Marker: "SPINDRIFT_PR_INTENT", Carrier: "fragment-body", Severity: "warn", When: "boxAccessReadOnly"},
		{ID: "issue-intent", Marker: "SPINDRIFT_ISSUE_INTENT", Carrier: "fragment-body", Severity: "warn", When: "filerFileRelay"},
	}
}

// mustContain is a small helper asserting substr appears in s, for error
// messages the exact byte-for-byte text of which is verified elsewhere by
// the entrypoint.sh message comparisons this package's Validate doc comment
// links to.
func mustContain(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Fatalf("%q does not contain %q", s, substr)
	}
}
