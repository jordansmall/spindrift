package opencode

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/outcome"
)

// The claude Driver's own outcome_fixture_test.go repeats this literal
// verbatim, so both drivers' fixtures are checked against the same line
// (issue #2261 slice 1).
const spindriftOutcomeLine = "SPINDRIFT_OUTCOME issue=42 landing=agent/issue-42 status=ready note=fixture"

// Pins that RenderTranscript emits this line, so the Console transcript
// drill-in shows it. The orchestrator no longer reads the transcript;
// TestOutcomeFixtureResultText covers its path.
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

// TestOutcomeFixtureResultText pins the Go extraction against the same
// fixture tests/driver-registry-outcome-extraction.bats runs the in-box shell
// extractor on, so the two halves cannot drift.
func TestOutcomeFixtureResultText(t *testing.T) {
	path := filepath.Join("testdata", "outcome-fixture.jsonl")

	text, err := ResultText(path)
	if err != nil {
		t.Fatalf("ResultText(%s): %v", path, err)
	}
	if got := outcome.ExtractOutcomeLine(outcome.StripResultText(text)); got != spindriftOutcomeLine {
		t.Errorf("ExtractOutcomeLine(ResultText(%s)) = %q, want %q", path, got, spindriftOutcomeLine)
	}
}

func TestResultText_Envelope(t *testing.T) {
	line := func(s string) string { return fmt.Sprintf(`{"type":"text","part":{"text":"%s"}}`, s) }
	t.Run("missing log is empty", func(t *testing.T) {
		got, err := ResultText(filepath.Join(t.TempDir(), "absent.log"))
		if err != nil || got != "" {
			t.Errorf("ResultText(missing) = %q, %v; want \"\", nil", got, err)
		}
	})
	t.Run("skips non-JSON and drops empty values", func(t *testing.T) {
		path := WriteLog(t, "not json at all", line(""), line("one"), `{"type":"other","result":"x"}`, line("two"))
		got, err := ResultText(path)
		if err != nil {
			t.Fatal(err)
		}
		if want := "one\ntwo\n"; got != want {
			t.Errorf("ResultText = %q, want %q", got, want)
		}
	})
	t.Run("null and non-string values are dropped", func(t *testing.T) {
		null := strings.NewReplacer(`"%s"`, "null").Replace(`{"type":"text","part":{"text":"%s"}}`)
		num := strings.NewReplacer(`"%s"`, "7").Replace(`{"type":"text","part":{"text":"%s"}}`)
		path := WriteLog(t, null, num, line("kept"))
		got, err := ResultText(path)
		if err != nil {
			t.Fatal(err)
		}
		if want := "kept\n"; got != want {
			t.Errorf("ResultText = %q, want %q", got, want)
		}
	})
}
