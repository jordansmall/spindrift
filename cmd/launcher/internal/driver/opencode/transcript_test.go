package opencode_test

import (
	"testing"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/driver/opencode"
	"spindrift.dev/launcher/internal/passmachine"
)

// TestRenderTranscript_SurfacesTextEvents verifies that RenderTranscript
// surfaces every type:"text" event's part.text, including a SPINDRIFT_OUTCOME
// line and a VERDICT: line — the exact prose the orchestrator's
// scanPassLog/scanReviewLog scan the rendered transcript for.
func TestRenderTranscript_SurfacesTextEvents(t *testing.T) {
	logPath := opencode.WriteLog(t,
		`{"type":"text","part":{"text":"Investigating the issue."}}`,
		`{"type":"error","error":"rate_limit_error"}`,
		`{"type":"text","part":{"text":"VERDICT: APPROVE"}}`,
		`{"type":"text","part":{"text":"SPINDRIFT_OUTCOME issue=42 landing=https://example/pr/1 status=ready note=done"}}`,
	)

	got, err := newDriver(t).RenderTranscript(logPath, driverkit.RenderOptions{})
	if err != nil {
		t.Fatalf("RenderTranscript: %v", err)
	}
	want := "[implementor] Investigating the issue.\n" +
		"[implementor] VERDICT: APPROVE\n" +
		"[implementor] SPINDRIFT_OUTCOME issue=42 landing=https://example/pr/1 status=ready note=done\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestRenderTranscript_MissingFile verifies the missing-log-file contract
// shared with the claude Driver's RenderTranscript.
func TestRenderTranscript_MissingFile(t *testing.T) {
	got, err := newDriver(t).RenderTranscript("/does/not/exist.log", driverkit.RenderOptions{})
	if err != nil {
		t.Fatalf("RenderTranscript: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty string for missing file", got)
	}
}

// TestRenderTranscript_AttributesRole pins the "[role] " prefix on only the
// first physical line of each text event, which is what the verdict scan and
// the findings slice key on (issue #5332). A leading newline is trimmed so it
// cannot hide a VERDICT: first line, and empty events are skipped.
func TestRenderTranscript_AttributesRole(t *testing.T) {
	logPath := opencode.WriteLog(t,
		`{"type":"text","part":{"text":"\n\nVERDICT: BLOCK\n- finding one\n- finding two\n"}}`,
		`{"type":"text","part":{"text":"  \n"}}`,
	)

	got, err := newDriver(t).RenderTranscript(logPath, driverkit.RenderOptions{TopLevelRole: "reviewer"})
	if err != nil {
		t.Fatalf("RenderTranscript: %v", err)
	}
	want := "[reviewer] VERDICT: BLOCK\n- finding one\n- finding two\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestRenderTranscript_ReviewerSubagentVerdictReachesLegacyScan pins the
// end-to-end path: an opencode task call to the reviewer subagent renders as a
// "[role]   -> [reviewer] " result line, which the legacy loop's verdict scan
// (it has no top-level reviewer pass) reads as the subagent's verdict.
func TestRenderTranscript_ReviewerSubagentVerdictReachesLegacyScan(t *testing.T) {
	logPath := opencode.WriteLog(t,
		`{"type":"tool_use","part":{"callID":"c1","tool":"task","state":{"status":"completed","input":{"description":"review","prompt":"p","subagent_type":"reviewer"},"output":"VERDICT: BLOCK\n- fix it"}}}`,
	)

	got, err := newDriver(t).RenderTranscript(logPath, driverkit.RenderOptions{})
	if err != nil {
		t.Fatalf("RenderTranscript: %v", err)
	}
	if res := passmachine.Scan(got, passmachine.KindLegacy); res.Verdict != passmachine.VerdictBlock {
		t.Errorf("Scan(%q) verdict = %v, want VerdictBlock", got, res.Verdict)
	}
}
