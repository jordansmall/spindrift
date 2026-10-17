package claude_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/outcome"
)

// spindriftOutcomeLine is the exact SPINDRIFT_OUTCOME line embedded in
// testdata/outcome-fixture.jsonl, shared verbatim with the opencode Driver's own
// outcome_fixture_test.go so both drivers' fixtures are verified against the
// identical literal (issue #2261 slice 1).
const spindriftOutcomeLine = "SPINDRIFT_OUTCOME issue=42 landing=agent/issue-42 status=ready note=fixture"

// TestOutcomeFixtureResultText pins ResultText against the Driver's canonical
// testdata/outcome-fixture.jsonl.
func TestOutcomeFixtureResultText(t *testing.T) {
	path := filepath.Join("testdata", "outcome-fixture.jsonl")

	text, err := newDriver().ResultText(path)
	if err != nil {
		t.Fatalf("ResultText(%s): %v", path, err)
	}
	if got := outcome.ExtractOutcomeLine(outcome.StripResultText(text)); got != spindriftOutcomeLine {
		t.Errorf("ExtractOutcomeLine(ResultText(%s)) = %q, want %q", path, got, spindriftOutcomeLine)
	}

	textPath := filepath.Join(t.TempDir(), "result.txt")
	if err := os.WriteFile(textPath, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	if line, found, err := outcome.LastNearMissOutcomeLine(textPath); err != nil || found {
		t.Errorf("LastNearMissOutcomeLine(ResultText(%s)) = %q, %v, %v; want no near miss", path, line, found, err)
	}
}

func TestResultText_Envelope(t *testing.T) {
	line := func(s string) string { return fmt.Sprintf(`{"type":"result","result":"%s"}`, s) }
	t.Run("missing log is empty", func(t *testing.T) {
		got, err := newDriver().ResultText(filepath.Join(t.TempDir(), "absent.log"))
		if err != nil || got != "" {
			t.Errorf("ResultText(missing) = %q, %v; want \"\", nil", got, err)
		}
	})
	t.Run("skips non-JSON and drops empty values", func(t *testing.T) {
		path := claude.WriteLog(t, "not json at all", line(""), line("one"), `{"type":"other","result":"x"}`, line("two"))
		got, err := newDriver().ResultText(path)
		if err != nil {
			t.Fatal(err)
		}
		if want := "one\ntwo\n"; got != want {
			t.Errorf("ResultText = %q, want %q", got, want)
		}
	})
	t.Run("null and non-string values are dropped", func(t *testing.T) {
		null := strings.NewReplacer(`"%s"`, "null").Replace(`{"type":"result","result":"%s"}`)
		num := strings.NewReplacer(`"%s"`, "7").Replace(`{"type":"result","result":"%s"}`)
		path := claude.WriteLog(t, null, num, line("kept"))
		got, err := newDriver().ResultText(path)
		if err != nil {
			t.Fatal(err)
		}
		if want := "kept\n"; got != want {
			t.Errorf("ResultText = %q, want %q", got, want)
		}
	})
}

func TestResultText_MistypedTotalsKeepText(t *testing.T) {
	path := claude.WriteLog(t,
		`{"type":"result","result":"SPINDRIFT_OUTCOME status=done","num_turns":"3"}`,
		`{"type":"result","result":"second","duration_ms":12.5}`,
	)
	got, err := newDriver().ResultText(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "SPINDRIFT_OUTCOME status=done\nsecond\n"; got != want {
		t.Errorf("ResultText = %q, want %q", got, want)
	}
}
