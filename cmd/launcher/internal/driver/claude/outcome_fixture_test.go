package claude_test

import (
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver/driverkit"
)

// TestOutcomeFixtureRenderTranscript verifies that RenderTranscript on the
// canonical outcome fixture surfaces the SPINDRIFT_OUTCOME line, so the
// Console transcript drill-in shows it. The orchestrator no longer reads the
// transcript; TestOutcomeFixtureResultText covers its path.
func TestOutcomeFixtureRenderTranscript(t *testing.T) {
	path := filepath.Join("testdata", "outcome-fixture.jsonl")

	got, err := newDriver(t).RenderTranscript(path, driverkit.RenderOptions{})
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

	report, err := newDriver(t).ExtractUsage(path)
	if err != nil {
		t.Fatalf("ExtractUsage(%s): %v", path, err)
	}
	if !report.Found {
		t.Errorf("ExtractUsage(%s).Found = false, want true", path)
	}
}
