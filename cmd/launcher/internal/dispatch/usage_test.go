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

// TestCumulativeUsage_SumsMultipleSessionsWithinOnePassLog verifies that
// CumulativeUsage charges a run for the FULL summed spend of a single pass
// log that contains multiple driver sessions (multiple "result" events) —
// the actual #2574 scenario where the orchestrator invokes the driver
// repeatedly within one Box run. A last-wins bug would report only the
// second session's numbers (cost 5.00, input tokens 9000); the correct sum
// is 5.10 / 9100.
func TestCumulativeUsage_SumsMultipleSessionsWithinOnePassLog(t *testing.T) {
	dir := tempLogDir(t)
	f, err := NewFactory(Config{}, dir, runner.NewFake(), fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()
	d := f.New("11", "test issue")

	session1 := `{"type":"result","num_turns":1,"total_cost_usd":0.10,"usage":{"input_tokens":100,"output_tokens":50}}`
	session2 := `{"type":"result","num_turns":1,"total_cost_usd":5.00,"usage":{"input_tokens":9000,"output_tokens":500}}`
	writeRunLog(t, d, session1, session2)

	got := d.CumulativeUsage()
	if diff := got.TotalCostUSD - 5.10; diff > 0.0001 || diff < -0.0001 {
		t.Errorf("TotalCostUSD = %v, want ~5.10 (sum of both sessions, not just the last)", got.TotalCostUSD)
	}
	if got.InputTokens != 9100 {
		t.Errorf("InputTokens = %d, want 9100 (sum of both sessions, not just the last)", got.InputTokens)
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

// TestCumulativeUsage_ChargesRotatedAsideRetryAttempt verifies CumulativeUsage
// counts a hold/backoff-retried attempt that rotateStaleLog moved aside to
// logPath().1 (issue #561), not just the current attempt at the bare
// logPath() -- so the budget gate sees a run's full spend even when the
// overrun happened entirely inside an abandoned, retried attempt (issue
// #2575).
func TestCumulativeUsage_ChargesRotatedAsideRetryAttempt(t *testing.T) {
	dir := tempLogDir(t)
	f, err := NewFactory(Config{}, dir, runner.NewFake(), fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()
	d := f.New("12", "test issue")

	// Simulate what rotateStaleLog would have produced: the abandoned first
	// attempt rotated aside to logPath().1, and the current attempt left at
	// the bare logPath().
	if err := writeFile(d.logPath()+".1", `{"type":"result","num_turns":1,"total_cost_usd":3.00,"usage":{"input_tokens":5000,"output_tokens":250}}`+"\n"); err != nil {
		t.Fatal(err)
	}
	writeRunLog(t, d, `{"type":"result","num_turns":1,"total_cost_usd":0.10,"usage":{"input_tokens":100,"output_tokens":50}}`)

	got := d.CumulativeUsage()
	if diff := got.TotalCostUSD - 3.10; diff > 0.0001 || diff < -0.0001 {
		t.Errorf("TotalCostUSD = %v, want ~3.10 (rotated-aside retry attempt plus current attempt)", got.TotalCostUSD)
	}
	if got.InputTokens != 5100 {
		t.Errorf("InputTokens = %d, want 5100 (rotated-aside retry attempt plus current attempt)", got.InputTokens)
	}
	if got.OutputTokens != 300 {
		t.Errorf("OutputTokens = %d, want 300 (rotated-aside retry attempt plus current attempt)", got.OutputTokens)
	}
}

// TestUsageReport_SumsAcrossFixPasses verifies UsageReport's Turns/API
// time/token figures sum across the initial run's log AND every fix-pass
// log on disk (issue #2575), not just the initial pass's own result event —
// the report-comment counterpart of CumulativeUsage's existing multi-pass
// sum.
func TestUsageReport_SumsAcrossFixPasses(t *testing.T) {
	dir := tempLogDir(t)
	f, err := NewFactory(Config{}, dir, runner.NewFake(), fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()
	d := f.New("13", "test issue")

	writeRunLog(t, d, `{"type":"result","num_turns":2,"total_cost_usd":0.10,"duration_ms":1000,"duration_api_ms":500,"usage":{"input_tokens":100,"output_tokens":50}}`)
	if err := writeFile(d.fixLogPath(1), `{"type":"result","num_turns":3,"total_cost_usd":0.20,"duration_ms":2000,"duration_api_ms":700,"usage":{"input_tokens":200,"output_tokens":75}}`+"\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(d.fixLogPath(2), `{"type":"result","num_turns":4,"total_cost_usd":0.30,"duration_ms":3000,"duration_api_ms":900,"usage":{"input_tokens":300,"output_tokens":90}}`+"\n"); err != nil {
		t.Fatal(err)
	}

	body := d.UsageReport()
	if !strings.Contains(body, "| Turns | 9 |") {
		t.Errorf("report should sum Turns across all three passes to 9; got: %q", body)
	}
	// API time sums 500+700+900=2100ms -> "2s"
	if !strings.Contains(body, "| API time | 2s |") {
		t.Errorf("report should sum API time across all three passes to 2s; got: %q", body)
	}
}

// TestUsageReport_ChargesRotatedAsideRetryAttempt verifies UsageReport
// includes a hold/backoff-retried attempt's spend (rotated aside to
// logPath().1 per issue #561) alongside the current attempt at the bare
// logPath(), mirroring CumulativeUsage_ChargesRotatedAsideRetryAttempt but
// asserting on the rendered comment body (issue #2575).
func TestUsageReport_ChargesRotatedAsideRetryAttempt(t *testing.T) {
	dir := tempLogDir(t)
	f, err := NewFactory(Config{}, dir, runner.NewFake(), fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()
	d := f.New("14", "test issue")

	if err := writeFile(d.logPath()+".1", `{"type":"result","num_turns":1,"total_cost_usd":3.00,"duration_ms":5000,"duration_api_ms":1000,"usage":{"input_tokens":5000,"output_tokens":250}}`+"\n"); err != nil {
		t.Fatal(err)
	}
	writeRunLog(t, d, `{"type":"result","num_turns":2,"total_cost_usd":0.10,"duration_ms":1000,"duration_api_ms":500,"usage":{"input_tokens":100,"output_tokens":50}}`)

	body := d.UsageReport()
	if !strings.Contains(body, "| Turns | 3 |") {
		t.Errorf("report should sum Turns across the rotated attempt and current attempt to 3; got: %q", body)
	}
	if !strings.Contains(body, "| API time | 1s |") {
		t.Errorf("report should sum API time across the rotated attempt and current attempt to 1s; got: %q", body)
	}
}

// TestUsageReport_MissingFixPassDegrades verifies UsageReport still renders
// using only the found log(s) when a fix-pass log doesn't exist at all
// (never ran, or crashed before writing anything) — a missing pass never
// aborts the whole report back to "unavailable" as long as at least one log
// was found (issue #2575).
func TestUsageReport_MissingFixPassDegrades(t *testing.T) {
	dir := tempLogDir(t)
	f, err := NewFactory(Config{}, dir, runner.NewFake(), fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()
	d := f.New("15", "test issue")

	writeRunLog(t, d, `{"type":"result","num_turns":2,"total_cost_usd":0.10,"duration_ms":1000,"duration_api_ms":500,"usage":{"input_tokens":100,"output_tokens":50}}`)
	// fix-1 exists but has no result event (crashed before finishing).
	if err := writeFile(d.fixLogPath(1), `{"type":"assistant","message":{"content":[]}}`+"\n"); err != nil {
		t.Fatal(err)
	}
	// fix-2 never ran at all -- no file on disk.

	body := d.UsageReport()
	if strings.Contains(body, "unavailable") {
		t.Errorf("report should not say unavailable when at least one log was found; got: %q", body)
	}
	if !strings.Contains(body, "| Turns | 2 |") {
		t.Errorf("report should reflect only the found initial log's Turns (2); got: %q", body)
	}
}

// TestUsageReport_WallTimeSpansAcrossPasses verifies Wall time reflects the
// span-floor combination across two DIFFERENT passes' logs — the true
// combined span between the earliest timestamped event in pass 1 and the
// latest timestamped event in pass 2 — rather than a naive sum of the two
// passes' own duration_ms values, and rather than just one pass's own value
// (issue #2575).
func TestUsageReport_WallTimeSpansAcrossPasses(t *testing.T) {
	dir := tempLogDir(t)
	f, err := NewFactory(Config{}, dir, runner.NewFake(), fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()
	d := f.New("16", "test issue")

	// Pass 1: two timestamped assistant/user lines an hour apart, but its
	// own duration_ms only claims 10 minutes.
	pass1 := []string{
		`{"type":"assistant","timestamp":"2026-08-11T10:00:00.000Z","message":{"model":"claude-opus-4-8","content":[],"usage":{"input_tokens":10,"output_tokens":5}}}`,
		`{"type":"user","timestamp":"2026-08-11T11:00:00.000Z","message":{"content":[]}}`,
		`{"type":"result","num_turns":1,"total_cost_usd":0.10,"duration_ms":600000,"duration_api_ms":500,"usage":{"input_tokens":100,"output_tokens":50}}`,
	}
	if err := writeFile(d.logPath(), strings.Join(pass1, "\n")+"\n"); err != nil {
		t.Fatal(err)
	}

	// Pass 2 (fix-1): starts 3 hours after pass 1 began, runs another hour;
	// its own duration_ms also only claims 10 minutes. True combined span
	// (pass1 earliest to pass2 latest) is 4 hours -- far longer than either
	// pass's own duration_ms and far longer than a naive sum (1200000ms =
	// 20m).
	pass2 := []string{
		`{"type":"assistant","timestamp":"2026-08-11T13:00:00.000Z","message":{"model":"claude-opus-4-8","content":[],"usage":{"input_tokens":10,"output_tokens":5}}}`,
		`{"type":"user","timestamp":"2026-08-11T14:00:00.000Z","message":{"content":[]}}`,
		`{"type":"result","num_turns":1,"total_cost_usd":0.10,"duration_ms":600000,"duration_api_ms":500,"usage":{"input_tokens":100,"output_tokens":50}}`,
	}
	if err := writeFile(d.fixLogPath(1), strings.Join(pass2, "\n")+"\n"); err != nil {
		t.Fatal(err)
	}

	body := d.UsageReport()
	if !strings.Contains(body, "| Wall time | 4h 0m 0s |") {
		t.Errorf("report should reflect the 4h combined span across both passes; got: %q", body)
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

// TestUsageReport_MergesPerModelTokensAcrossPasses verifies that the SAME
// model appearing in two different passes' logs is merged into ONE row
// summing both passes' token figures, not rendered as two separate rows
// (issue #2575's per-model acceptance criterion).
func TestUsageReport_MergesPerModelTokensAcrossPasses(t *testing.T) {
	dir := tempLogDir(t)
	f, err := NewFactory(Config{}, dir, runner.NewFake(), fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()
	d := f.New("17", "test issue")

	pass1 := []string{
		`{"type":"assistant","message":{"model":"claude-opus-4-8","content":[],"usage":{"input_tokens":100,"output_tokens":50}}}`,
		`{"type":"result","num_turns":1,"total_cost_usd":0.10,"usage":{"input_tokens":100,"output_tokens":50}}`,
	}
	if err := writeFile(d.logPath(), strings.Join(pass1, "\n")+"\n"); err != nil {
		t.Fatal(err)
	}
	pass2 := []string{
		`{"type":"assistant","message":{"model":"claude-opus-4-8","content":[],"usage":{"input_tokens":40,"output_tokens":20}}}`,
		`{"type":"result","num_turns":1,"total_cost_usd":0.10,"usage":{"input_tokens":40,"output_tokens":20}}`,
	}
	if err := writeFile(d.fixLogPath(1), strings.Join(pass2, "\n")+"\n"); err != nil {
		t.Fatal(err)
	}

	body := d.UsageReport()
	if strings.Count(body, "claude-opus-4-8") != 1 {
		t.Fatalf("report should merge the shared model into one row; got: %q", body)
	}
	if !strings.Contains(body, "| claude-opus-4-8 | 140 | 70 | 0 | 0 | 0 |") {
		t.Errorf("report should sum the shared model's tokens across both passes (140/70); got: %q", body)
	}
}

// TestUsageReport_MergedModelOrderIsFirstAppearance verifies that when
// different passes contribute different models, the merged per-model table
// preserves first-appearance order across logs (not any family-rank
// reordering) -- a run whose initial pass used haiku and fix pass used opus
// must still render haiku-first, matching the order the underlying logs were
// produced in (issue #2575, reverted from a family-rank sort that
// mis-ordered opencode model ids -- see
// TestUsageReport_MergedModelOrderSurvivesFamilySubstringCollision).
func TestUsageReport_MergedModelOrderIsFirstAppearance(t *testing.T) {
	dir := tempLogDir(t)
	f, err := NewFactory(Config{}, dir, runner.NewFake(), fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()
	d := f.New("18", "test issue")

	// Initial pass uses haiku only; fix pass uses opus only -- haiku appears
	// first chronologically and should render first.
	pass1 := []string{
		`{"type":"assistant","message":{"model":"claude-haiku-4-5-20251001","content":[],"usage":{"input_tokens":10,"output_tokens":5}}}`,
		`{"type":"result","num_turns":1,"total_cost_usd":0.10,"usage":{"input_tokens":10,"output_tokens":5}}`,
	}
	if err := writeFile(d.logPath(), strings.Join(pass1, "\n")+"\n"); err != nil {
		t.Fatal(err)
	}
	pass2 := []string{
		`{"type":"assistant","message":{"model":"claude-opus-4-8","content":[],"usage":{"input_tokens":100,"output_tokens":50}}}`,
		`{"type":"result","num_turns":1,"total_cost_usd":0.10,"usage":{"input_tokens":100,"output_tokens":50}}`,
	}
	if err := writeFile(d.fixLogPath(1), strings.Join(pass2, "\n")+"\n"); err != nil {
		t.Fatal(err)
	}

	body := d.UsageReport()
	opusIdx := strings.Index(body, "claude-opus-4-8")
	haikuIdx := strings.Index(body, "claude-haiku-4-5-20251001")
	if opusIdx == -1 || haikuIdx == -1 {
		t.Fatalf("report should contain both models; got: %q", body)
	}
	if haikuIdx > opusIdx {
		t.Errorf("report should render haiku before opus (first appearance), got opus before haiku; body: %q", body)
	}
}

// TestUsageReport_MergedModelOrderSurvivesFamilySubstringCollision pins down
// the bug a prior family-rank sort introduced: an opencode-style model id
// such as "claude-sonnet-4" contains the substring "sonnet" just like a
// claude-driver id does, so a rank table keyed on opus/haiku/sonnet
// substrings silently reordered it. Here the id appearing FIRST (rank
// "sonnet", 2) and the id appearing SECOND (rank "haiku", 1) have ranks
// that disagree with appearance order -- exactly the case a family-rank
// sort gets wrong and first-appearance order gets right, regardless of
// which family substring either id happens to contain (issue #2575).
func TestUsageReport_MergedModelOrderSurvivesFamilySubstringCollision(t *testing.T) {
	found := []usage.Report{
		{
			Totals:        usage.Usage{InputTokens: 10, OutputTokens: 5},
			Found:         true,
			SummedByModel: []usage.ModelUsage{{Model: "claude-sonnet-4", UncachedInputTokens: 10, OutputTokens: 5}},
		},
		{
			Totals:        usage.Usage{InputTokens: 20, OutputTokens: 8},
			Found:         true,
			SummedByModel: []usage.ModelUsage{{Model: "claude-3-5-haiku-20241022", UncachedInputTokens: 20, OutputTokens: 8}},
		},
	}

	got := aggregatedReport(found)

	if len(got.SummedByModel) != 2 {
		t.Fatalf("SummedByModel = %v, want 2 rows", got.SummedByModel)
	}
	if got.SummedByModel[0].Model != "claude-sonnet-4" || got.SummedByModel[1].Model != "claude-3-5-haiku-20241022" {
		t.Errorf("SummedByModel order = %v, want [claude-sonnet-4, claude-3-5-haiku-20241022] (first appearance)",
			got.SummedByModel)
	}
}

// TestSpanDurationMs_NoUsableSpanFallsBackToLongestReportDuration verifies
// that when NO found report carries a usable event span (HasEventSpan is
// false on every one), spanDurationMs falls back to the largest single
// report's own Totals.DurationMs -- not zero, and not a naive sum of every
// report's duration (issue #2575).
func TestSpanDurationMs_NoUsableSpanFallsBackToLongestReportDuration(t *testing.T) {
	found := []usage.Report{
		{Totals: usage.Usage{DurationMs: 1000}, Found: true},
		{Totals: usage.Usage{DurationMs: 5000}, Found: true},
	}

	got := spanDurationMs(found)
	if got != 5000 {
		t.Errorf("spanDurationMs = %d, want 5000 (max own duration, not sum or zero)", got)
	}
}

// TestSpanDurationMs_LatestNotAfterEarliestFallsBackToLongestReportDuration
// verifies the latestMs > earliestMs guard: a report whose earliest and
// latest event timestamps land on the same instant (or are otherwise not
// strictly increasing) contributes a zero span, so spanDurationMs must still
// fall back to the largest single report's own Totals.DurationMs rather than
// reporting that degenerate zero span (issue #2575).
func TestSpanDurationMs_LatestNotAfterEarliestFallsBackToLongestReportDuration(t *testing.T) {
	found := []usage.Report{
		{
			Totals:          usage.Usage{DurationMs: 2000},
			Found:           true,
			HasEventSpan:    true,
			EarliestEventMs: 1000,
			LatestEventMs:   1000,
		},
		{Totals: usage.Usage{DurationMs: 7000}, Found: true},
	}

	got := spanDurationMs(found)
	if got != 7000 {
		t.Errorf("spanDurationMs = %d, want 7000 (max own duration, not the degenerate zero span)", got)
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
