package opencode

import (
	"path/filepath"
	"strings"
	"testing"
)

// The claude Driver's own outcome_fixture_test.go repeats this literal
// verbatim, so both drivers' fixtures are checked against the same line
// (issue #2261 slice 1).
const spindriftOutcomeLine = "SPINDRIFT_OUTCOME issue=42 landing=agent/issue-42 status=ready note=fixture"

// An orchestrator's outcome.ParseAnywhere scan finds the outcome only if
// RenderTranscript emits this line, so this test pins that it does.
func TestOutcomeFixtureRenderTranscript(t *testing.T) {
	path := filepath.Join("testdata", "outcome-fixture.jsonl")

	got, err := RenderTranscript(path)
	if err != nil {
		t.Fatalf("RenderTranscript(%s): %v", path, err)
	}
	if !strings.Contains(got, spindriftOutcomeLine) {
		t.Errorf("RenderTranscript(%s) = %q, want it to contain %q", path, got, spindriftOutcomeLine)
	}
}

// The fixture must keep at least one usage-bearing step_finish event, or
// ExtractUsage reports no usage at all.
func TestOutcomeFixtureExtractUsage(t *testing.T) {
	path := filepath.Join("testdata", "outcome-fixture.jsonl")

	report, err := ExtractUsage(path)
	if err != nil {
		t.Fatalf("ExtractUsage(%s): %v", path, err)
	}
	if !report.Found {
		t.Errorf("ExtractUsage(%s).Found = false, want true", path)
	}
}
