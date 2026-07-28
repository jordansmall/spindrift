package claude

import (
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/usage"
)

// TestBreakdownByModel_Fixture locks the per-call SUM rule against a real-
// shaped stream-json log: testdata/run-usage-sample.jsonl.
//
// The summation rule — per-call (sum across turns) vs cumulative (take the
// final snapshot) — is the crux the issue demanded be settled with evidence
// before implementing, precisely to rule out the ~9x inflation on #2078.
// Two independent lines of real evidence settle it as PER-CALL:
//
//  1. The Anthropic Messages API usage contract. Per-response `usage` is
//     reported per request: `input_tokens` is the uncached-input remainder
//     for that one call; `cache_read_input_tokens` and
//     `cache_creation_input_tokens` are that call's own cache tokens; and
//     `cache_creation` splits the creation total by TTL into
//     `ephemeral_5m_input_tokens` / `ephemeral_1h_input_tokens` (the
//     `cache_control` default TTL is 5m). Claude Code's
//     `--output-format stream-json` emits exactly one `assistant` event per
//     API response, so each event's `usage` is one call's per-request usage
//     — never a running total. Correct aggregation is therefore a SUM.
//
//  2. The real #2078 dispatch run-usage figures (produced by the launcher
//     parsing an actual dispatch's stream-json log — 15 turns). Its
//     result-event header `usage` snapshot reports input_tokens 3564 and
//     cache_read 502183; the per-role SUM over the same run's assistant
//     messages reports input_tokens 32165 and cache_read 4,615,090
//     (~9.2x the header). The header is the *final* call's snapshot. Were
//     per-message usage cumulative, that final message — and thus the
//     header — would already carry the whole-run running total (~32165
//     input), not 3564; the header being an order of magnitude *smaller*
//     than the 15-turn sum falsifies the cumulative hypothesis. The ~9x
//     cache_read gap is what per-call reads accumulated over ~15
//     growing-context turns look like, not double-summed running totals.
//
// The fixture is modeled on the confirmed real claude-code stream-json shape
// — #2080 confirmed the "Agent" spawn-block shape (rather than the legacy
// "Task" name) and the nested cache_creation TTL split. It is not a raw
// per-message capture of a live run: this box is read-only with no `claude`
// CLI and no network, so a live dispatch cannot be recorded in-box. The
// issue's optional out-of-band API-key reconciliation against the Usage &
// Cost Admin API remains the human confirmation step; the two evidence
// lines above are what settle the summation rule here.
//
// The assertions below lock that SUM rule: two models (opus, sonnet) appear
// as the implementor's own turns (no parent_tool_use_id) interleaved with a
// scout subagent's haiku turns and an unrelated worker's sonnet turn, and
// the totals must reflect the sum across all of them regardless of role or
// turn boundary. The result event's own "usage" is a non-cumulative
// snapshot of only its own call and must contribute nothing to the sums:
// opus.UncachedInputTokens is 140 (the two opus assistant lines, 100+40),
// not 180 — the result line's also-opus-shaped input (40) is excluded, so a
// regression that read or summed the header snapshot would fail this test.
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
