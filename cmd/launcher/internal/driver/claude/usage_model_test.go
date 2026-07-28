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
// The assertions below lock that SUM rule as a sum over DISTINCT
// message.ids, not a naive sum over every assistant event: two models
// (opus, sonnet) appear as the implementor's own turns (no
// parent_tool_use_id) interleaved with a scout subagent's haiku turns and
// an unrelated worker's sonnet turn, and the totals must reflect the sum
// across all of them regardless of role or turn boundary. The result
// event's own "usage" is a non-cumulative snapshot of only its own call and
// must contribute nothing to the sums: opus.UncachedInputTokens is 140 (the
// two DISTINCT opus messages, msg_opus_1's 100 + msg_opus_2's 40), not 180
// — the result line's also-opus-shaped input (40) is excluded, so a
// regression that read or summed the header snapshot would fail this test.
//
// The fixture also carries a re-emit regression: line 2 is a second
// content-block re-emit of line 1's message — same message.id
// ("msg_opus_1") and byte-identical usage, but a different content block
// (a "text" block rather than line 1's tool_use "Agent" spawn block),
// modeling claude-code emitting one stream-json line per content block of a
// single multi-block assistant message. Line 2 must contribute nothing
// beyond line 1: opus stays at 140/70/3000/260/140 only because dedup
// collapses the repeated msg_opus_1 to a single count; a regression that
// summed every event rather than every distinct message.id would double
// line 1's contribution and read 240/120/4000/460/240 instead, failing this
// test. The re-emit's content block is deliberately a "text" block, not a
// Task/Agent spawn, so it does not also alter CollectTaskRoles-style role
// collection over this same fixture.
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

// TestBreakdownByModel_DedupByMessageID confirms that when claude-code
// re-emits a multi-content-block assistant message once per block — each
// line carrying the SAME message.id and byte-identical usage — the
// breakdown counts that message's usage once, not once per re-emitted line.
// Two opus lines share message.id "msg_a" with identical usage (simulating a
// 2-block re-emit); a third, distinct opus line carries a different id
// "msg_b". The expected totals are the deduped sum (msg_a once + msg_b
// once), not the naive 3x/2x-inflated sum over all three lines.
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
