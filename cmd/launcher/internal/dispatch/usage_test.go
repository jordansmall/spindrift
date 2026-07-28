package dispatch

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/usage"
)

// writeRunLog writes lines directly to a Dispatch's run log path, simulating
// a box that already ran and reported them.
func writeRunLog(t *testing.T, d *Dispatch, lines ...string) {
	t.Helper()
	var parts []string
	for _, l := range lines {
		if l != "" {
			parts = append(parts, l)
		}
	}
	if err := writeFile(d.logPath(), strings.Join(parts, "\n")+"\n"); err != nil {
		t.Fatal(err)
	}
}

// TestUsageReport_HumanReadableDurations verifies that wall time and API
// time are formatted as h/m/s strings, not raw milliseconds.
func TestUsageReport_HumanReadableDurations(t *testing.T) {
	dir := tempLogDir(t)
	f, err := NewFactory(Config{}, dir, runner.NewFake(), fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()
	d := f.New("88", "test issue")

	// duration_ms=3665000 → "1h 1m 5s"; duration_api_ms=65000 → "1m 5s"
	resultEvent := `{"type":"result","num_turns":3,"total_cost_usd":0.10,"duration_ms":3665000,"duration_api_ms":65000,"usage":{"input_tokens":100,"output_tokens":50}}`
	writeRunLog(t, d, resultEvent)

	body := d.UsageReport()
	if !strings.Contains(body, "1h 1m 5s") {
		t.Errorf("report should contain wall time %q; got: %q", "1h 1m 5s", body)
	}
	if !strings.Contains(body, "1m 5s") {
		t.Errorf("report should contain API time %q; got: %q", "1m 5s", body)
	}
	if strings.Contains(body, "3665000ms") || strings.Contains(body, "65000ms") {
		t.Errorf("report should NOT contain raw ms values; got: %q", body)
	}
}

// TestUsageReport_OmitsCostAndHeaderTokens verifies the report surfaces
// Model/Wall time/API time/Turns, but no longer surfaces a cost figure or
// the aggregate header token rows (Input tokens / Output tokens / Cache
// read tokens / Cache creation tokens) — those are superseded by the
// per-model token table.
func TestUsageReport_OmitsCostAndHeaderTokens(t *testing.T) {
	dir := tempLogDir(t)
	f, err := NewFactory(Config{}, dir, runner.NewFake(), fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()
	d := f.New("42", "test issue")

	resultEvent := `{"type":"result","num_turns":5,"total_cost_usd":0.25,"duration_ms":3000,"duration_api_ms":2000,"usage":{"input_tokens":500,"output_tokens":100,"cache_read_input_tokens":50,"cache_creation_input_tokens":10}}`
	writeRunLog(t, d, resultEvent)

	body := d.UsageReport()
	if strings.Contains(body, "$") {
		t.Errorf("report should NOT contain a cost figure; got: %q", body)
	}
	if strings.Contains(body, "Cost") {
		t.Errorf("report should NOT contain a Cost row; got: %q", body)
	}
	for _, label := range []string{"Input tokens", "Output tokens", "Cache read tokens", "Cache creation tokens"} {
		if strings.Contains(body, label) {
			t.Errorf("report should NOT contain aggregate header row %q; got: %q", label, body)
		}
	}
	if !strings.Contains(body, "| Model |") {
		t.Errorf("report should contain Model row; got: %q", body)
	}
	if !strings.Contains(body, "| Wall time |") {
		t.Errorf("report should contain Wall time row; got: %q", body)
	}
	if !strings.Contains(body, "| API time |") {
		t.Errorf("report should contain API time row; got: %q", body)
	}
	if !strings.Contains(body, "| Turns | 5 |") {
		t.Errorf("report should contain Turns row with 5; got: %q", body)
	}
}

// TestUsageReport_MissingResultEvent_ReportsUnavailable verifies that when
// no result event is in the log, UsageReport degrades gracefully rather than
// erroring.
func TestUsageReport_MissingResultEvent_ReportsUnavailable(t *testing.T) {
	dir := tempLogDir(t)
	f, err := NewFactory(Config{}, dir, runner.NewFake(), fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()
	d := f.New("7", "test issue")

	// No result event written at all -- the log doesn't even exist yet.
	body := d.UsageReport()
	if !strings.Contains(body, "unavailable") {
		t.Errorf("report should say unavailable when usage missing; got: %q", body)
	}
}

// TestCumulativeUsage_SumsAcrossInitialAndFixPasses verifies CumulativeUsage
// adds token counts and cost across the initial run's log and every fix-pass
// log on disk, rather than reporting only the initial pass's own usage
// (issue #2001 — selfHealGate's budget gate needs the run's total spend, not
// just its first pass).
func TestCumulativeUsage_SumsAcrossInitialAndFixPasses(t *testing.T) {
	dir := tempLogDir(t)
	f, err := NewFactory(Config{}, dir, runner.NewFake(), fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()
	d := f.New("9", "test issue")

	writeRunLog(t, d, `{"type":"result","num_turns":1,"total_cost_usd":0.10,"usage":{"input_tokens":100,"output_tokens":50}}`)
	if err := writeFile(d.fixLogPath(1), `{"type":"result","num_turns":1,"total_cost_usd":0.20,"usage":{"input_tokens":200,"output_tokens":75}}`+"\n"); err != nil {
		t.Fatal(err)
	}

	got := d.CumulativeUsage()
	if diff := got.TotalCostUSD - 0.30; diff > 0.0001 || diff < -0.0001 {
		t.Errorf("TotalCostUSD = %v, want ~0.30", got.TotalCostUSD)
	}
	if got.InputTokens != 300 {
		t.Errorf("InputTokens = %d, want 300", got.InputTokens)
	}
	if got.OutputTokens != 125 {
		t.Errorf("OutputTokens = %d, want 125", got.OutputTokens)
	}
}

// TestCumulativeUsage_PassWithNoResultEventContributesNothing verifies that
// a fix-pass log with no result event (a crashed or still-running pass)
// degrades to contributing zero rather than aborting the whole sum — the
// initial run's own usage still comes through (issue #2001).
func TestCumulativeUsage_PassWithNoResultEventContributesNothing(t *testing.T) {
	dir := tempLogDir(t)
	f, err := NewFactory(Config{}, dir, runner.NewFake(), fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()
	d := f.New("10", "test issue")

	writeRunLog(t, d, `{"type":"result","num_turns":1,"total_cost_usd":0.10,"usage":{"input_tokens":100,"output_tokens":50}}`)
	if err := writeFile(d.fixLogPath(1), `{"type":"assistant","message":{"content":[]}}`+"\n"); err != nil {
		t.Fatal(err)
	}

	got := d.CumulativeUsage()
	if diff := got.TotalCostUSD - 0.10; diff > 0.0001 || diff < -0.0001 {
		t.Errorf("TotalCostUSD = %v, want ~0.10 (fix-pass with no result event contributes nothing)", got.TotalCostUSD)
	}
	if got.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100 (fix-pass with no result event contributes nothing)", got.InputTokens)
	}
}

// TestUsageReport_FullFormatLocksExactMarkdown locks the exact Markdown
// UsageReport renders for a log carrying two model families (opus and
// haiku) plus a result event: the metadata table (Model/Wall time/API
// time/Turns only — no Cost row, no aggregate header token rows) followed
// by the per-model token table, joined the same way modelBreakdownSection
// joins onto its caller (blank line, then the "### Per-model token usage"
// heading).
func TestUsageReport_FullFormatLocksExactMarkdown(t *testing.T) {
	t.Setenv("MODEL", "claude-opus-4-8")
	dir := tempLogDir(t)
	f, err := NewFactory(Config{}, dir, runner.NewFake(), fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()
	d := f.New("300", "test issue")

	opus1 := `{"type":"assistant","message":{"model":"claude-opus-4-8","content":[],"usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":1000,"cache_creation_input_tokens":300,"cache_creation":{"ephemeral_5m_input_tokens":200,"ephemeral_1h_input_tokens":100}}}}`
	haiku1 := `{"type":"assistant","message":{"model":"claude-haiku-4-5-20251001","content":[],"usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":500,"cache_creation_input_tokens":50,"cache_creation":{"ephemeral_5m_input_tokens":50,"ephemeral_1h_input_tokens":0}}}}`
	opus2 := `{"type":"assistant","message":{"model":"claude-opus-4-8","content":[],"usage":{"input_tokens":40,"output_tokens":20,"cache_read_input_tokens":2000,"cache_creation_input_tokens":100,"cache_creation":{"ephemeral_5m_input_tokens":60,"ephemeral_1h_input_tokens":40}}}}`
	resultEvent := `{"type":"result","num_turns":3,"total_cost_usd":0.30,"duration_ms":3665000,"duration_api_ms":65000,"usage":{"input_tokens":40,"output_tokens":20}}`
	writeRunLog(t, d, opus1, haiku1, opus2, resultEvent)

	body := d.UsageReport()

	want := "## Run usage\n\n" +
		"| Field | Value |\n" +
		"| --- | --- |\n" +
		"| Model | `claude-opus-4-8` |\n" +
		"| Wall time | 1h 1m 5s |\n" +
		"| API time | 1m 5s |\n" +
		"| Turns | 3 |\n\n" +
		"### Per-model token usage\n\n" +
		"| Model | Uncached input | Output | Cache read | Cache write (5m) | Cache write (1h) |\n" +
		"| --- | --- | --- | --- | --- | --- |\n" +
		"| claude-opus-4-8 | 140 | 70 | 3000 | 260 | 140 |\n" +
		"| claude-haiku-4-5-20251001 | 10 | 5 | 500 | 50 | 0 |\n"

	if body != want {
		t.Errorf("UsageReport() =\n%q\nwant:\n%q", body, want)
	}
	if strings.Contains(body, "$") {
		t.Errorf("report should NOT contain a cost figure; got: %q", body)
	}
	if strings.Contains(body, "Cost") {
		t.Errorf("report should NOT contain a Cost row; got: %q", body)
	}
	if strings.Contains(body, "Per-role") {
		t.Errorf("report should NOT contain a per-role breakdown section; got: %q", body)
	}
}

// TestModelBreakdownSection verifies the per-model token breakdown table
// renders one row per model with all six token categories, and that an
// empty models slice renders no section at all.
func TestModelBreakdownSection(t *testing.T) {
	models := []usage.ModelUsage{
		{
			Model:                "claude-opus-4-8",
			UncachedInputTokens:  140,
			OutputTokens:         70,
			CacheReadInputTokens: 3000,
			CacheWrite5mTokens:   260,
			CacheWrite1hTokens:   140,
		},
		{
			Model:                "claude-haiku-4-5-20251001",
			UncachedInputTokens:  18,
			OutputTokens:         9,
			CacheReadInputTokens: 800,
			CacheWrite5mTokens:   70,
			CacheWrite1hTokens:   0,
		},
	}

	body := modelBreakdownSection(models)

	if !strings.Contains(body, "### Per-model token usage") {
		t.Errorf("report should contain per-model header; got: %q", body)
	}
	if !strings.Contains(body, "| Model | Uncached input | Output | Cache read | Cache write (5m) | Cache write (1h) |") {
		t.Errorf("report should contain per-model table header row; got: %q", body)
	}
	if !strings.Contains(body, "| claude-opus-4-8 | 140 | 70 | 3000 | 260 | 140 |") {
		t.Errorf("report should contain opus row; got: %q", body)
	}
	if !strings.Contains(body, "| claude-haiku-4-5-20251001 | 18 | 9 | 800 | 70 | 0 |") {
		t.Errorf("report should contain haiku row; got: %q", body)
	}

	if got := modelBreakdownSection(nil); got != "" {
		t.Errorf("modelBreakdownSection(nil) = %q, want empty string", got)
	}
}
