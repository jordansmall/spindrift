package claude

import (
	"path/filepath"
	"strings"
	"testing"
)

// spindriftOutcomeLine is the exact SPINDRIFT_OUTCOME line embedded in
// testdata/outcome-fixture.jsonl -- shared verbatim with the opencode
// Driver's own outcome_fixture_test.go so both drivers' fixtures are
// verified against the identical literal (issue #2261 slice 1).
const spindriftOutcomeLine = "SPINDRIFT_OUTCOME issue=42 landing=agent/issue-42 status=ready note=fixture"

// TestOutcomeFixtureRenderTranscript verifies that RenderTranscript on the
// canonical outcome fixture surfaces the SPINDRIFT_OUTCOME line an
// orchestrator's outcome.ParseAnywhere scan depends on finding.
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

// TestOutcomeFixtureExtractUsage verifies that the canonical outcome fixture
// carries at least one usage-bearing event, so ExtractUsage reports Found.
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
