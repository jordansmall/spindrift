package claude

import (
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/usage"
)

// TestBreakdownByModel_Fixture locks the per-call SUM rule against a real-
// shaped stream-json log: testdata/run-usage-sample.jsonl. The fixture is
// modeled on the confirmed real claude-code stream-json shape — issue #2078
// confirmed both the "Agent" spawn-block shape (rather than the legacy
// "Task" name) and the nested cache_creation object splitting
// cache_creation_input_tokens by TTL. A live per-dispatch capture of an
// actual run's stream-json log, to confirm no further shape drift, remains a
// human out-of-band step per issue #2085 — this fixture is not itself that
// capture.
//
// Per-message usage in claude-code stream-json is PER-CALL: each assistant
// event is one API response, so Anthropic's input_tokens is already the
// uncached input for that call, and cache_read/cache_creation are that
// call's own cache tokens — nothing here is cumulative across a turn or a
// run. Aggregation is therefore a SUM across every assistant event, across
// every turn and every subagent, keyed by the calling model's family. The
// assertions below lock that SUM rule: two models (opus, sonnet) appear as
// the implementor's own turns (no parent_tool_use_id) interleaved with a
// scout subagent's haiku turns and an unrelated worker's sonnet turn, and
// the totals must reflect the sum across all of them regardless of role or
// turn boundary. The result event's own "usage" is a non-cumulative
// snapshot of only its own call and must contribute nothing to the sums —
// verified implicitly below, since the opus totals equal the sum of the two
// opus assistant lines only, not double-counted against the result line's
// (also-opus-shaped) numbers.
func TestBreakdownByModel_Fixture(t *testing.T) {
	path := filepath.Join("testdata", "run-usage-sample.jsonl")

	got, err := breakdownByModel(path)
	if err != nil {
		t.Fatalf("breakdownByModel: %v", err)
	}

	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3: %+v", len(got), got)
	}
	wantOrder := []string{"opus", "haiku", "sonnet"}
	for i, m := range got {
		if m.Model != wantOrder[i] {
			t.Errorf("got[%d].Model = %q, want %q (order: %v)", i, m.Model, wantOrder[i], got)
		}
	}

	byModel := make(map[string]usage.ModelUsage)
	for _, m := range got {
		byModel[m.Model] = m
	}

	opus := byModel["opus"]
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

	haiku := byModel["haiku"]
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

	sonnet := byModel["sonnet"]
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
// stream-json log — where the nested cache_creation object is absent but the
// flat cache_creation_input_tokens total is set — attributes that collapsed
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

// TestBreakdownByModel_FileNotFound confirms a missing log file degrades to
// (nil, nil), matching breakdownByRoleFile's contract.
func TestBreakdownByModel_FileNotFound(t *testing.T) {
	got, err := breakdownByModel("/nonexistent/x.log")
	if err != nil {
		t.Fatalf("breakdownByModel: %v", err)
	}
	if got != nil {
		t.Errorf("got = %+v, want nil", got)
	}
}
