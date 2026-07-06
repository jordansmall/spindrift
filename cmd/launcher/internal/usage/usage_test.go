package usage_test

import (
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/usage"
)

func writeLog(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.log")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestLastInLog_FullResultEvent(t *testing.T) {
	line := `{"type":"result","num_turns":7,"total_cost_usd":0.1234,"duration_ms":5000,"duration_api_ms":3000,"usage":{"input_tokens":800,"output_tokens":200,"cache_read_input_tokens":150,"cache_creation_input_tokens":50}}`
	path := writeLog(t, "some output", line)

	u, found, err := usage.LastInLog(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if u.NumTurns != 7 {
		t.Errorf("NumTurns: got %d, want 7", u.NumTurns)
	}
	if u.TotalCostUSD != 0.1234 {
		t.Errorf("TotalCostUSD: got %f, want 0.1234", u.TotalCostUSD)
	}
	if u.DurationMs != 5000 {
		t.Errorf("DurationMs: got %d, want 5000", u.DurationMs)
	}
	if u.DurationApiMs != 3000 {
		t.Errorf("DurationApiMs: got %d, want 3000", u.DurationApiMs)
	}
	if u.InputTokens != 800 {
		t.Errorf("InputTokens: got %d, want 800", u.InputTokens)
	}
	if u.OutputTokens != 200 {
		t.Errorf("OutputTokens: got %d, want 200", u.OutputTokens)
	}
	if u.CacheReadInputTokens != 150 {
		t.Errorf("CacheReadInputTokens: got %d, want 150", u.CacheReadInputTokens)
	}
	if u.CacheCreationInputTokens != 50 {
		t.Errorf("CacheCreationInputTokens: got %d, want 50", u.CacheCreationInputTokens)
	}
}

func TestLastInLog_TakesLast(t *testing.T) {
	first := `{"type":"result","num_turns":1,"total_cost_usd":0.01,"duration_ms":100,"usage":{"input_tokens":10,"output_tokens":5}}`
	last := `{"type":"result","num_turns":9,"total_cost_usd":0.99,"duration_ms":9000,"usage":{"input_tokens":900,"output_tokens":90}}`
	path := writeLog(t, first, "some other output", last)

	u, found, err := usage.LastInLog(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if u.NumTurns != 9 {
		t.Errorf("NumTurns: got %d, want 9 (should take last)", u.NumTurns)
	}
}

func TestLastInLog_NoCacheFields(t *testing.T) {
	line := `{"type":"result","num_turns":3,"total_cost_usd":0.05,"duration_ms":2000,"usage":{"input_tokens":100,"output_tokens":40}}`
	path := writeLog(t, line)

	u, found, err := usage.LastInLog(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if u.CacheReadInputTokens != 0 {
		t.Errorf("CacheReadInputTokens: got %d, want 0 (absent field)", u.CacheReadInputTokens)
	}
	if u.CacheCreationInputTokens != 0 {
		t.Errorf("CacheCreationInputTokens: got %d, want 0 (absent field)", u.CacheCreationInputTokens)
	}
}

func TestLastInLog_NotFound(t *testing.T) {
	path := writeLog(t, "some output", "no result event here")
	_, found, err := usage.LastInLog(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Fatal("expected found=false")
	}
}

func TestLastInLog_FileNotFound(t *testing.T) {
	_, found, err := usage.LastInLog("/nonexistent/path/test.log")
	if err != nil {
		t.Fatalf("unexpected error for missing file: %v", err)
	}
	if found {
		t.Fatal("expected found=false for missing file")
	}
}

func TestLastInLog_MalformedJSON(t *testing.T) {
	path := writeLog(t, `{"type":"result","num_turns":INVALID}`)
	_, found, err := usage.LastInLog(path)
	if err != nil {
		t.Fatalf("unexpected error for malformed JSON: %v", err)
	}
	if found {
		t.Fatal("expected found=false for malformed JSON")
	}
}

func TestBreakdownByRole_ScoutAndReviewer(t *testing.T) {
	// Main agent: invokes scout Task (subagent_type in input)
	implMain1 := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_scout","name":"Task","input":{"subagent_type":"scout","prompt":"map files"}}],"usage":{"input_tokens":100,"output_tokens":50}}}`
	// Scout messages (grouped under toolu_scout)
	scoutMsg1 := `{"type":"assistant","message":{"content":[],"usage":{"input_tokens":200,"output_tokens":80}},"parent_tool_use_id":"toolu_scout"}`
	scoutMsg2 := `{"type":"assistant","message":{"content":[],"usage":{"input_tokens":100,"output_tokens":40}},"parent_tool_use_id":"toolu_scout"}`
	// Main agent: invokes reviewer Task
	implMain2 := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_reviewer","name":"Task","input":{"subagent_type":"reviewer","prompt":"review diff"}}],"usage":{"input_tokens":150,"output_tokens":60}}}`
	// Reviewer message
	reviewerMsg1 := `{"type":"assistant","message":{"content":[],"usage":{"input_tokens":300,"output_tokens":100}},"parent_tool_use_id":"toolu_reviewer"}`
	// Final main agent message (no parent)
	implMain3 := `{"type":"assistant","message":{"content":[],"usage":{"input_tokens":50,"output_tokens":20}}}`

	path := writeLog(t, implMain1, scoutMsg1, scoutMsg2, implMain2, reviewerMsg1, implMain3)

	breakdown, err := usage.BreakdownByRole(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	roles := map[string]usage.RoleUsage{}
	for _, r := range breakdown {
		roles[r.Role] = r
	}

	impl := roles["implementor"]
	if impl.InputTokens != 300 { // 100+150+50
		t.Errorf("implementor input tokens: got %d, want 300", impl.InputTokens)
	}
	if impl.OutputTokens != 130 { // 50+60+20
		t.Errorf("implementor output tokens: got %d, want 130", impl.OutputTokens)
	}

	scout := roles["scout"]
	if scout.InputTokens != 300 { // 200+100
		t.Errorf("scout input tokens: got %d, want 300", scout.InputTokens)
	}
	if scout.OutputTokens != 120 { // 80+40
		t.Errorf("scout output tokens: got %d, want 120", scout.OutputTokens)
	}

	reviewer := roles["reviewer"]
	if reviewer.InputTokens != 300 {
		t.Errorf("reviewer input tokens: got %d, want 300", reviewer.InputTokens)
	}
	if reviewer.OutputTokens != 100 {
		t.Errorf("reviewer output tokens: got %d, want 100", reviewer.OutputTokens)
	}
}

func TestBreakdownByRole_ReviewerReinvoked(t *testing.T) {
	// First reviewer invocation
	implMain1 := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_rev1","name":"Task","input":{"subagent_type":"reviewer","prompt":"review diff"}}],"usage":{"input_tokens":100,"output_tokens":30}}}`
	reviewerMsg1 := `{"type":"assistant","message":{"content":[],"usage":{"input_tokens":200,"output_tokens":80}},"parent_tool_use_id":"toolu_rev1"}`
	// Second reviewer invocation (after BLOCK)
	implMain2 := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_rev2","name":"Task","input":{"subagent_type":"reviewer","prompt":"re-review"}}],"usage":{"input_tokens":50,"output_tokens":20}}}`
	reviewerMsg2 := `{"type":"assistant","message":{"content":[],"usage":{"input_tokens":150,"output_tokens":60}},"parent_tool_use_id":"toolu_rev2"}`

	path := writeLog(t, implMain1, reviewerMsg1, implMain2, reviewerMsg2)

	breakdown, err := usage.BreakdownByRole(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	roles := map[string]usage.RoleUsage{}
	for _, r := range breakdown {
		roles[r.Role] = r
	}

	// Both reviewer invocations must be summed under "reviewer", not mislabeled.
	reviewer := roles["reviewer"]
	if reviewer.InputTokens != 350 { // 200+150
		t.Errorf("reviewer input tokens: got %d, want 350 (both invocations)", reviewer.InputTokens)
	}
	if _, ok := roles["subagent"]; ok {
		t.Error("want no subagent bucket; second reviewer invocation should sum into reviewer")
	}
}

func TestBreakdownByRole_NoSubagents(t *testing.T) {
	// Only main agent messages, no parent_tool_use_id
	implMsg1 := `{"type":"assistant","message":{"content":[],"usage":{"input_tokens":100,"output_tokens":50}}}`
	implMsg2 := `{"type":"assistant","message":{"content":[],"usage":{"input_tokens":200,"output_tokens":80}}}`

	path := writeLog(t, implMsg1, implMsg2)

	breakdown, err := usage.BreakdownByRole(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(breakdown) != 1 {
		t.Fatalf("want 1 role (implementor only), got %d", len(breakdown))
	}
	if breakdown[0].Role != "implementor" {
		t.Errorf("role: got %q, want %q", breakdown[0].Role, "implementor")
	}
	if breakdown[0].InputTokens != 300 {
		t.Errorf("implementor input tokens: got %d, want 300", breakdown[0].InputTokens)
	}
}

func TestBreakdownByRole_FileNotFound(t *testing.T) {
	breakdown, err := usage.BreakdownByRole("/nonexistent/path/test.log")
	if err != nil {
		t.Fatalf("unexpected error for missing file: %v", err)
	}
	if breakdown != nil {
		t.Fatal("expected nil breakdown for missing file")
	}
}

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		ms   int64
		want string
	}{
		{0, "0s"},
		{999, "0s"},
		{1000, "1s"},
		{5000, "5s"},
		{59000, "59s"},
		{60000, "1m 0s"},
		{65000, "1m 5s"},
		{3600000, "1h 0m 0s"},
		{3665000, "1h 1m 5s"},
		{7384000, "2h 3m 4s"},
	}
	for _, tc := range cases {
		got := usage.FormatDuration(tc.ms)
		if got != tc.want {
			t.Errorf("FormatDuration(%d): got %q, want %q", tc.ms, got, tc.want)
		}
	}
}

func TestLastInLog_OversizedLine(t *testing.T) {
	const fiveMiB = 5 * 1024 * 1024
	// Write an oversized line then a valid result event
	path := filepath.Join(t.TempDir(), "big.log")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	big := make([]byte, fiveMiB)
	for i := range big {
		big[i] = 'x'
	}
	f.Write(big)
	f.WriteString("\n")
	f.WriteString(`{"type":"result","num_turns":2,"total_cost_usd":0.02,"duration_ms":200,"usage":{"input_tokens":20,"output_tokens":10}}` + "\n")
	f.Close()

	u, found, err := usage.LastInLog(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true after oversized line")
	}
	if u.NumTurns != 2 {
		t.Errorf("NumTurns: got %d, want 2", u.NumTurns)
	}
}
