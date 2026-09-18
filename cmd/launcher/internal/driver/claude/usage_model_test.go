package claude

import (
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/usage"
)

// TestBreakdownByModel_Fixture pins the per-call SUM rule: each assistant event
// carries one call's usage, never a running total, so the breakdown sums over
// DISTINCT message.ids across roles and turns. On #2078 the result header
// snapshot came in ~9x below the per-role sum, which is what falsified the
// cumulative reading. #2080 confirmed the fixture's shape.
func TestBreakdownByModel_Fixture(t *testing.T) {
	path := filepath.Join("testdata", "run-usage-sample.jsonl")

	got, err := breakdownByModel(path)
	if err != nil {
		t.Fatalf("breakdownByModel: %v", err)
	}

	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3: %+v", len(got), got)
	}
	wantOrder := []string{"claude-opus-4-8", "claude-haiku-4-5-20251001", "claude-sonnet-5"}
	for i, m := range got {
		if m.Model != wantOrder[i] {
			t.Errorf("got[%d].Model = %q, want %q (order: %v)", i, m.Model, wantOrder[i], got)
		}
	}

	byModel := make(map[string]usage.ModelUsage)
	for _, m := range got {
		byModel[m.Model] = m
	}

	// Opus wants 140, not 180: the result event's own snapshot is excluded, and
	// fixture line 2 re-emits msg_opus_1 as a second content block, which dedup
	// collapses to one count.
	opus := byModel["claude-opus-4-8"]
	if opus.UncachedInputTokens != 140 {
		t.Errorf("opus.UncachedInputTokens = %d, want 140", opus.UncachedInputTokens)
	}
	if opus.OutputTokens != 70 {
		t.Errorf("opus.OutputTokens = %d, want 70", opus.OutputTokens)
	}
	if opus.CacheReadInputTokens != 3000 {
		t.Errorf("opus.CacheReadInputTokens = %d, want 3000", opus.CacheReadInputTokens)
	}
	if opus.CacheWrite5mTokens != 260 {
		t.Errorf("opus.CacheWrite5mTokens = %d, want 260", opus.CacheWrite5mTokens)
	}
	if opus.CacheWrite1hTokens != 140 {
		t.Errorf("opus.CacheWrite1hTokens = %d, want 140", opus.CacheWrite1hTokens)
	}

	haiku := byModel["claude-haiku-4-5-20251001"]
	if haiku.UncachedInputTokens != 18 {
		t.Errorf("haiku.UncachedInputTokens = %d, want 18", haiku.UncachedInputTokens)
	}
	if haiku.OutputTokens != 9 {
		t.Errorf("haiku.OutputTokens = %d, want 9", haiku.OutputTokens)
	}
	if haiku.CacheReadInputTokens != 800 {
		t.Errorf("haiku.CacheReadInputTokens = %d, want 800", haiku.CacheReadInputTokens)
	}
	if haiku.CacheWrite5mTokens != 70 {
		t.Errorf("haiku.CacheWrite5mTokens = %d, want 70", haiku.CacheWrite5mTokens)
	}
	if haiku.CacheWrite1hTokens != 0 {
		t.Errorf("haiku.CacheWrite1hTokens = %d, want 0", haiku.CacheWrite1hTokens)
	}

	sonnet := byModel["claude-sonnet-5"]
	if sonnet.UncachedInputTokens != 30 {
		t.Errorf("sonnet.UncachedInputTokens = %d, want 30", sonnet.UncachedInputTokens)
	}
	if sonnet.OutputTokens != 15 {
		t.Errorf("sonnet.OutputTokens = %d, want 15", sonnet.OutputTokens)
	}
	if sonnet.CacheReadInputTokens != 700 {
		t.Errorf("sonnet.CacheReadInputTokens = %d, want 700", sonnet.CacheReadInputTokens)
	}
	if sonnet.CacheWrite5mTokens != 0 {
		t.Errorf("sonnet.CacheWrite5mTokens = %d, want 0", sonnet.CacheWrite5mTokens)
	}
	if sonnet.CacheWrite1hTokens != 0 {
		t.Errorf("sonnet.CacheWrite1hTokens = %d, want 0", sonnet.CacheWrite1hTokens)
	}
}

// TestBreakdownByModel_UnknownModel confirms an assistant message with no
// model field buckets under "unknown" rather than being dropped or panicking.
func TestBreakdownByModel_UnknownModel(t *testing.T) {
	line := `{"type":"assistant","message":{"content":[],"usage":{"input_tokens":5,"output_tokens":2}}}`
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

// TestBreakdownByModel_CacheCreationCollapsed confirms a pre-TTL-split
// stream-json log, where the nested cache_creation object is absent but the
// flat cache_creation_input_tokens total is set, attributes that collapsed
// total to the 5-minute bucket rather than dropping it.
func TestBreakdownByModel_CacheCreationCollapsed(t *testing.T) {
	line := `{"type":"assistant","message":{"model":"claude-opus-4","content":[],"usage":{"input_tokens":5,"output_tokens":2,"cache_creation_input_tokens":123}}}`
	path := WriteLog(t, line)

	got, err := breakdownByModel(path)
	if err != nil {
		t.Fatalf("breakdownByModel: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1: %+v", len(got), got)
	}
	if got[0].CacheWrite5mTokens != 123 {
		t.Errorf("got[0].CacheWrite5mTokens = %d, want 123", got[0].CacheWrite5mTokens)
	}
	if got[0].CacheWrite1hTokens != 0 {
		t.Errorf("got[0].CacheWrite1hTokens = %d, want 0", got[0].CacheWrite1hTokens)
	}
}

// TestBreakdownByModel_DedupByMessageID pins the rule that claude-code
// re-emitting a multi-block assistant message once per content block, each line
// carrying the same message.id and identical usage, counts once. The fixture
// repeats "msg_a" and adds a distinct "msg_b", so the wanted totals are the
// deduped sum, not the inflated sum over all three lines.
func TestBreakdownByModel_DedupByMessageID(t *testing.T) {
	lines := []string{
		`{"type":"assistant","message":{"id":"msg_a","model":"claude-opus-4-8","content":[{"type":"text","text":"block 1"}],"usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":1000,"cache_creation_input_tokens":300,"cache_creation":{"ephemeral_5m_input_tokens":200,"ephemeral_1h_input_tokens":100}}}}`,
		`{"type":"assistant","message":{"id":"msg_a","model":"claude-opus-4-8","content":[{"type":"text","text":"block 2"}],"usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":1000,"cache_creation_input_tokens":300,"cache_creation":{"ephemeral_5m_input_tokens":200,"ephemeral_1h_input_tokens":100}}}}`,
		`{"type":"assistant","message":{"id":"msg_b","model":"claude-opus-4-8","content":[],"usage":{"input_tokens":40,"output_tokens":20,"cache_read_input_tokens":2000,"cache_creation_input_tokens":100,"cache_creation":{"ephemeral_5m_input_tokens":60,"ephemeral_1h_input_tokens":40}}}}`,
	}
	path := WriteLog(t, lines...)

	got, err := breakdownByModel(path)
	if err != nil {
		t.Fatalf("breakdownByModel: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1: %+v", len(got), got)
	}

	opus := got[0]
	if opus.Model != "claude-opus-4-8" {
		t.Fatalf("got[0].Model = %q, want %q", opus.Model, "claude-opus-4-8")
	}
	if opus.UncachedInputTokens != 140 {
		t.Errorf("opus.UncachedInputTokens = %d, want 140", opus.UncachedInputTokens)
	}
	if opus.OutputTokens != 70 {
		t.Errorf("opus.OutputTokens = %d, want 70", opus.OutputTokens)
	}
	if opus.CacheReadInputTokens != 3000 {
		t.Errorf("opus.CacheReadInputTokens = %d, want 3000", opus.CacheReadInputTokens)
	}
	if opus.CacheWrite5mTokens != 260 {
		t.Errorf("opus.CacheWrite5mTokens = %d, want 260", opus.CacheWrite5mTokens)
	}
	if opus.CacheWrite1hTokens != 140 {
		t.Errorf("opus.CacheWrite1hTokens = %d, want 140", opus.CacheWrite1hTokens)
	}
}

// TestBreakdownByModel_TwoIdsSameFamily confirms two distinct model ids in
// the same ModelFamily (opus) yield two distinct rows, each labeled with its
// exact id rather than being collapsed into one "opus" row. ModelFamily is
// used for ordering only, so within the opus family the two rows are
// ordered by raw id ("claude-opus-4-7" sorts before "claude-opus-4-8").
func TestBreakdownByModel_TwoIdsSameFamily(t *testing.T) {
	lines := []string{
		`{"type":"assistant","message":{"id":"msg_1","model":"claude-opus-4-8","content":[],"usage":{"input_tokens":100,"output_tokens":50}}}`,
		`{"type":"assistant","message":{"id":"msg_2","model":"claude-opus-4-7","content":[],"usage":{"input_tokens":10,"output_tokens":5}}}`,
		`{"type":"assistant","message":{"id":"msg_3","model":"claude-sonnet-5","content":[],"usage":{"input_tokens":1,"output_tokens":1}}}`,
	}
	path := WriteLog(t, lines...)

	got, err := breakdownByModel(path)
	if err != nil {
		t.Fatalf("breakdownByModel: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3: %+v", len(got), got)
	}

	wantOrder := []string{"claude-opus-4-7", "claude-opus-4-8", "claude-sonnet-5"}
	for i, m := range got {
		if m.Model != wantOrder[i] {
			t.Errorf("got[%d].Model = %q, want %q (order: %v)", i, m.Model, wantOrder[i], got)
		}
	}
}

// TestBreakdownByModel_FileNotFound confirms a missing log file degrades to
// (nil, nil), matching lastInLog's contract.
func TestBreakdownByModel_FileNotFound(t *testing.T) {
	got, err := breakdownByModel("/nonexistent/x.log")
	if err != nil {
		t.Fatalf("breakdownByModel: %v", err)
	}
	if got != nil {
		t.Errorf("got = %+v, want nil", got)
	}
}
