package opencode

import (
	"errors"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/testutil"
	"spindrift.dev/launcher/internal/usage"
)

// TestBreakdownByModel_DedupByMessageID confirms that when two step_finish
// lines share the same non-empty messageID (an opencode re-emit of one
// message's part, mirroring claude-code's multi-content-block re-emit), the
// breakdown counts that message's usage once, not once per line. A third,
// distinct messageID is always counted. The expected totals are the deduped
// sum (msg_a once + msg_b once), not the naive inflated sum over all three
// lines.
func TestBreakdownByModel_DedupByMessageID(t *testing.T) {
	lines := []string{
		`{"type":"step_finish","part":{"messageID":"msg_a","modelID":"gpt-5","tokens":{"input":100,"output":50,"reasoning":10,"cache":{"write":20,"read":200}}}}`,
		`{"type":"step_finish","part":{"messageID":"msg_a","modelID":"gpt-5","tokens":{"input":100,"output":50,"reasoning":10,"cache":{"write":20,"read":200}}}}`,
		`{"type":"step_finish","part":{"messageID":"msg_b","modelID":"gpt-5","tokens":{"input":40,"output":20,"reasoning":0,"cache":{"write":10,"read":80}}}}`,
	}
	path := WriteLog(t, lines...)

	got, err := breakdownByModel(path)
	if err != nil {
		t.Fatalf("breakdownByModel: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1: %+v", len(got), got)
	}

	m := got[0]
	if m.Model != "gpt-5" {
		t.Fatalf("got[0].Model = %q, want %q", m.Model, "gpt-5")
	}
	if m.UncachedInputTokens != 140 {
		t.Errorf("UncachedInputTokens = %d, want 140", m.UncachedInputTokens)
	}
	if m.OutputTokens != 80 {
		t.Errorf("OutputTokens = %d, want 80", m.OutputTokens)
	}
	if m.CacheReadInputTokens != 280 {
		t.Errorf("CacheReadInputTokens = %d, want 280", m.CacheReadInputTokens)
	}
	if m.CacheWrite5mTokens != 30 {
		t.Errorf("CacheWrite5mTokens = %d, want 30", m.CacheWrite5mTokens)
	}
	if m.CacheWrite1hTokens != 0 {
		t.Errorf("CacheWrite1hTokens = %d, want 0", m.CacheWrite1hTokens)
	}
}

// TestBreakdownByModel_UnknownModel confirms a step_finish with no modelID
// buckets under "unknown" rather than being dropped or panicking.
func TestBreakdownByModel_UnknownModel(t *testing.T) {
	line := `{"type":"step_finish","part":{"messageID":"msg_1","tokens":{"input":5,"output":2}}}`
	path := WriteLog(t, line)

	got, err := breakdownByModel(path)
	if err != nil {
		t.Fatalf("breakdownByModel: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1: %+v", len(got), got)
	}
	if got[0].Model != "unknown" {
		t.Errorf("got[0].Model = %q, want %q", got[0].Model, "unknown")
	}
	if got[0].UncachedInputTokens != 5 {
		t.Errorf("got[0].UncachedInputTokens = %d, want 5", got[0].UncachedInputTokens)
	}
}

// TestBreakdownByModel_EmptyMessageIDAlwaysCounted confirms that two
// step_finish lines carrying an empty messageID are both counted, not
// deduped against each other — there is nothing to dedup an empty id
// against, unlike the non-empty-id case in
// TestBreakdownByModel_DedupByMessageID.
func TestBreakdownByModel_EmptyMessageIDAlwaysCounted(t *testing.T) {
	lines := []string{
		`{"type":"step_finish","part":{"messageID":"","modelID":"gpt-5","tokens":{"input":10,"output":5}}}`,
		`{"type":"step_finish","part":{"messageID":"","modelID":"gpt-5","tokens":{"input":10,"output":5}}}`,
	}
	path := WriteLog(t, lines...)

	got, err := breakdownByModel(path)
	if err != nil {
		t.Fatalf("breakdownByModel: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1: %+v", len(got), got)
	}

	m := got[0]
	if m.Model != "gpt-5" {
		t.Fatalf("got[0].Model = %q, want %q", m.Model, "gpt-5")
	}
	if m.UncachedInputTokens != 20 {
		t.Errorf("UncachedInputTokens = %d, want 20", m.UncachedInputTokens)
	}
	if m.OutputTokens != 10 {
		t.Errorf("OutputTokens = %d, want 10", m.OutputTokens)
	}
}

// TestBreakdownByModel_CacheWriteTo5m confirms tokens.cache.write lands in
// CacheWrite5mTokens and CacheWrite1hTokens stays 0 — opencode reports no
// TTL split, unlike claude-code's ephemeral_5m/1h cache_creation.
func TestBreakdownByModel_CacheWriteTo5m(t *testing.T) {
	line := `{"type":"step_finish","part":{"messageID":"msg_1","modelID":"gpt-5","tokens":{"input":1,"output":1,"cache":{"write":77,"read":0}}}}`
	path := WriteLog(t, line)

	got, err := breakdownByModel(path)
	if err != nil {
		t.Fatalf("breakdownByModel: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1: %+v", len(got), got)
	}
	if got[0].CacheWrite5mTokens != 77 {
		t.Errorf("CacheWrite5mTokens = %d, want 77", got[0].CacheWrite5mTokens)
	}
	if got[0].CacheWrite1hTokens != 0 {
		t.Errorf("CacheWrite1hTokens = %d, want 0", got[0].CacheWrite1hTokens)
	}
}

// TestBreakdownByModel_TwoModels confirms two distinct modelIDs yield two
// rows, ordered by ascending raw model id — opencode models are not claude
// families, so there is no family-rank pass, unlike claude's breakdown.
func TestBreakdownByModel_TwoModels(t *testing.T) {
	lines := []string{
		`{"type":"step_finish","part":{"messageID":"msg_1","modelID":"gpt-5","tokens":{"input":100,"output":50}}}`,
		`{"type":"step_finish","part":{"messageID":"msg_2","modelID":"claude-sonnet-4","tokens":{"input":10,"output":5}}}`,
	}
	path := WriteLog(t, lines...)

	got, err := breakdownByModel(path)
	if err != nil {
		t.Fatalf("breakdownByModel: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2: %+v", len(got), got)
	}

	wantOrder := []string{"claude-sonnet-4", "gpt-5"}
	for i, m := range got {
		if m.Model != wantOrder[i] {
			t.Errorf("got[%d].Model = %q, want %q (order: %v)", i, m.Model, wantOrder[i], got)
		}
	}
}

// TestBreakdownByModel_FileNotFound confirms a missing log file degrades to
// (nil, nil), matching ExtractUsage's own missing-log contract.
func TestBreakdownByModel_FileNotFound(t *testing.T) {
	got, err := breakdownByModel("/nonexistent/x.log")
	if err != nil {
		t.Fatalf("breakdownByModel: %v", err)
	}
	if got != nil {
		t.Errorf("got = %+v, want nil", got)
	}
}

// TestExtractUsage_BreakdownByModelError confirms ExtractUsage still returns
// the aggregate totals it already summed from step_finish events when
// breakdownByModel fails with a real I/O error, rather than discarding them
// (issue #674, mirroring claude's ExtractUsage).
func TestExtractUsage_BreakdownByModelError(t *testing.T) {
	line := `{"type":"step_finish","part":{"messageID":"msg_a","modelID":"gpt-5","tokens":{"input":100,"output":50,"reasoning":0,"cache":{"write":0,"read":0}}}}`
	path := WriteLog(t, line)

	orig := breakdownByModel
	breakdownByModel = func(string) ([]usage.ModelUsage, error) {
		return nil, errors.New("simulated I/O error")
	}
	defer func() { breakdownByModel = orig }()

	var report usage.Report
	var err error
	stderr := testutil.CaptureStderr(t, func() {
		report, err = ExtractUsage(path)
	})
	if !strings.Contains(stderr, path) || !strings.Contains(stderr, "simulated I/O error") {
		t.Errorf("stderr = %q, want it to mention the log path and the error", stderr)
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !report.Found {
		t.Fatal("expected Found=true")
	}
	if report.FinalSnapshot.InputTokens != 100 || report.FinalSnapshot.OutputTokens != 50 {
		t.Errorf("Usage: got %+v, want InputTokens=100 OutputTokens=50", report.FinalSnapshot)
	}
	if report.SummedByModel != nil {
		t.Errorf("Models: got %+v, want nil", report.SummedByModel)
	}
}
