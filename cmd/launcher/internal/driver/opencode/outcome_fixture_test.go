package opencode

import (
	"fmt"
	"os"
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

	got, err := RenderTranscript(path, "")
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

// TestOutcomeFixtureResultText pins ResultText against the Driver's canonical
// testdata/outcome-fixture.jsonl.
func TestOutcomeFixtureResultText(t *testing.T) {
	path := filepath.Join("testdata", "outcome-fixture.jsonl")

	text, err := ResultText(path)
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
		if want := "two\n"; got != want { // ID-less events are separate messages
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

func TestResultText_LastMessageOnly(t *testing.T) {
	text := func(msg, s string) string {
		return fmt.Sprintf(`{"type":"text","part":{"messageID":%q,"text":%q}}`, msg, s)
	}
	ev := func(typ, msg string) string {
		return fmt.Sprintf(`{"type":%q,"part":{"messageID":%q}}`, typ, msg)
	}
	const decoy = "SPINDRIFT_OUTCOME issue=1 landing=x status=ready note=mid"

	t.Run("mid-pass outcome line does not survive", func(t *testing.T) {
		path := WriteLog(t, text("msg_1", decoy), ev("step_finish", "msg_1"), text("msg_2", "all done, no outcome yet"))
		got, err := ResultText(path)
		if err != nil {
			t.Fatal(err)
		}
		if want := "all done, no outcome yet\n"; got != want {
			t.Errorf("ResultText = %q, want %q", got, want)
		}
		if line := outcome.ExtractOutcomeLine(outcome.StripResultText(got)); line != "" {
			t.Errorf("ExtractOutcomeLine = %q, want none", line)
		}
	})
	t.Run("textless final message yields empty", func(t *testing.T) {
		path := WriteLog(t, text("msg_1", decoy), ev("tool_use", "msg_1"), ev("step_finish", "msg_1"),
			ev("step_start", "msg_2"), ev("step_finish", "msg_2"))
		got, err := ResultText(path)
		if err != nil {
			t.Fatal(err)
		}
		if got != "" {
			t.Errorf("ResultText = %q, want \"\"", got)
		}
	})
	t.Run("parts of the last message join across interleaved events", func(t *testing.T) {
		path := WriteLog(t, text("msg_1", "first"), text("msg_2", "a"), ev("step_finish", "msg_2"), text("msg_2", "b"))
		got, err := ResultText(path)
		if err != nil {
			t.Fatal(err)
		}
		if want := "a\nb\n"; got != want {
			t.Errorf("ResultText = %q, want %q", got, want)
		}
	})
	t.Run("synthetic ResultEvent stays the final message", func(t *testing.T) {
		syn, err := ResultEvent("BLOCK: nope")
		if err != nil {
			t.Fatal(err)
		}
		path := WriteLog(t, text("msg_1", "first"), strings.TrimSuffix(string(syn), "\n"))
		got, err := ResultText(path)
		if err != nil {
			t.Fatal(err)
		}
		if want := "BLOCK: nope\n"; got != want {
			t.Errorf("ResultText = %q, want %q", got, want)
		}
	})
}
