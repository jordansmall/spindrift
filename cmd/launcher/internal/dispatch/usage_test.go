package dispatch

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/usage"
)

// writeRunLog simulates a box that already ran by writing the lines it would
// have reported straight to the Dispatch's run log path.
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

func TestUsageReport_HumanReadableDurations(t *testing.T) {
	dir := tempLogDir(t)
	f, err := NewFactory(Config{}, dir, runner.NewFake(), fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()
	d := f.New("88", "test issue")

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

// The per-model token table replaced the cost figure and the aggregate header
// token rows, so the report must still show Model, Wall time, API time and
// Turns while showing none of those.
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

func TestUsageReport_MissingResultEvent_ReportsUnavailable(t *testing.T) {
	dir := tempLogDir(t)
	f, err := NewFactory(Config{}, dir, runner.NewFake(), fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()
	d := f.New("7", "test issue")

	// Nothing is written at all, so the log file does not even exist yet.
	body := d.UsageReport()
	if !strings.Contains(body, "unavailable") {
		t.Errorf("report should say unavailable when usage missing; got: %q", body)
	}
}

// Issue #2001: selfHealGate's budget gate needs the run's total spend, so the
// sum covers every fix-pass log on disk, not just the initial pass.
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

// Issue #2574: the orchestrator invokes the driver repeatedly within one Box
// run, so a single pass log holds several result events. A last-wins bug
// reports only the second session (5.00 cost, 9000 input tokens); the correct
// sum is 5.10 and 9100.
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

// Issue #2001: a crashed or still-running fix pass has no result event, and it
// must contribute zero rather than abort the whole sum.
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

// Issues #561 and #2575: rotateStaleLog moves a hold/backoff-retried attempt
// aside to logPath().1, and the budget gate must still see that spend when the
// overrun happened entirely inside the abandoned attempt.
func TestCumulativeUsage_ChargesRotatedAsideRetryAttempt(t *testing.T) {
	dir := tempLogDir(t)
	f, err := NewFactory(Config{}, dir, runner.NewFake(), fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()
	d := f.New("12", "test issue")

	// This is what rotateStaleLog leaves behind: the abandoned first attempt at
	// logPath().1 and the current attempt at the bare logPath().
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

// Issue #2575: the rendered comment is the counterpart of CumulativeUsage's
// multi-pass sum, so its Turns, API time and token figures must cover every
// fix-pass log too.
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
	// API time sums 500, 700 and 900 into 2100ms, which renders as "2s".
	if !strings.Contains(body, "| API time | 2s |") {
		t.Errorf("report should sum API time across all three passes to 2s; got: %q", body)
	}
}

// Issues #561 and #2575: the rendered comment body must charge the attempt
// rotated aside to logPath().1 as well, mirroring
// TestCumulativeUsage_ChargesRotatedAsideRetryAttempt.
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

// Issue #2575: a fix-pass log that never ran must not drag the whole report
// back to "unavailable" as long as one log was found.
func TestUsageReport_MissingFixPassDegrades(t *testing.T) {
	dir := tempLogDir(t)
	f, err := NewFactory(Config{}, dir, runner.NewFake(), fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()
	d := f.New("15", "test issue")

	writeRunLog(t, d, `{"type":"result","num_turns":2,"total_cost_usd":0.10,"duration_ms":1000,"duration_api_ms":500,"usage":{"input_tokens":100,"output_tokens":50}}`)
	// fix-1 exists but crashed before writing a result event.
	if err := writeFile(d.fixLogPath(1), `{"type":"assistant","message":{"content":[]}}`+"\n"); err != nil {
		t.Fatal(err)
	}
	// fix-2 never ran, so it leaves no file on disk.

	body := d.UsageReport()
	if strings.Contains(body, "unavailable") {
		t.Errorf("report should not say unavailable when at least one log was found; got: %q", body)
	}
	if !strings.Contains(body, "| Turns | 2 |") {
		t.Errorf("report should reflect only the found initial log's Turns (2); got: %q", body)
	}
}

// Issue #2575: Wall time is the span from the earliest timestamped event in
// pass 1 to the latest in pass 2, not a sum of the two passes' own duration_ms
// values and not one pass's value alone.
func TestUsageReport_WallTimeSpansAcrossPasses(t *testing.T) {
	dir := tempLogDir(t)
	f, err := NewFactory(Config{}, dir, runner.NewFake(), fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()
	d := f.New("16", "test issue")

	// Pass 1 spans an hour of timestamps while its own duration_ms claims only
	// 10 minutes.
	pass1 := []string{
		`{"type":"assistant","timestamp":"2026-08-11T10:00:00.000Z","message":{"model":"claude-opus-4-8","content":[],"usage":{"input_tokens":10,"output_tokens":5}}}`,
		`{"type":"user","timestamp":"2026-08-11T11:00:00.000Z","message":{"content":[]}}`,
		`{"type":"result","num_turns":1,"total_cost_usd":0.10,"duration_ms":600000,"duration_api_ms":500,"usage":{"input_tokens":100,"output_tokens":50}}`,
	}
	if err := writeFile(d.logPath(), strings.Join(pass1, "\n")+"\n"); err != nil {
		t.Fatal(err)
	}

	// Pass 2 starts 3 hours after pass 1 began and runs another hour, so the
	// combined span is 4 hours: far longer than either pass's own duration_ms
	// and than their sum of 20 minutes.
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

// This golden locks the exact Markdown for a two-model log, including how
// modelBreakdownSection joins onto the metadata table: a blank line, then the
// per-model heading.
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

// Issue #2575: one model appearing in two passes' logs merges into a single
// row summing both, never two rows.
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

// Issue #2575: the merged table keeps first-appearance order across logs. A
// family-rank sort once replaced it and mis-ordered opencode model ids, so see
// TestUsageReport_MergedModelOrderSurvivesFamilySubstringCollision.
func TestUsageReport_MergedModelOrderIsFirstAppearance(t *testing.T) {
	dir := tempLogDir(t)
	f, err := NewFactory(Config{}, dir, runner.NewFake(), fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()
	d := f.New("18", "test issue")

	// The initial pass uses haiku only and the fix pass opus only, so haiku
	// comes first chronologically and must render first.
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

// Issue #2575 pins the bug a prior family-rank sort introduced: an
// opencode-style id such as "claude-sonnet-4" carries the same family
// substrings a claude-driver id does, so a rank table keyed on them reordered
// it silently. The two fixture ids below have ranks that disagree with
// appearance order, which is the case that sort got wrong.
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

// Issue #2575: with HasEventSpan false everywhere, the fallback is the largest
// single report's own Totals.DurationMs, not zero and not a sum.
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

// Issue #2575 pins the latestMs > earliestMs guard: timestamps landing on the
// same instant give a zero span, and the fallback to the largest report's own
// Totals.DurationMs must win over that degenerate zero.
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
