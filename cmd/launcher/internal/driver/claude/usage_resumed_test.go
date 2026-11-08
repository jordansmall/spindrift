package claude_test

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/driver/driverkit"
)

// A fix dispatch's passes each --resume the implement session, and claude's
// result then carries the session's cumulative totals. Chaining the baselines
// log to log books each log's increment only.
func TestExtractUsageAfter_ResumedSessionAcrossFixLogsBooksEachIncrement(t *testing.T) {
	result := func(session string, cost float64, apiMs int, models string) string {
		return fmt.Sprintf(`{"type":"result","session_id":%q,"num_turns":5,"total_cost_usd":%v,"duration_ms":%d,"duration_api_ms":%d,"modelUsage":{%s}}`+"\n",
			session, cost, apiMs+1000, apiMs, models)
	}
	models := func(opus int) string {
		return fmt.Sprintf(`"claude-opus-4-1":{"outputTokens":%d},"claude-haiku-4-5":{"outputTokens":300},"claude-sonnet-4-5":{"outputTokens":200}`, opus)
	}
	dir := t.TempDir()
	logs := []struct {
		body    string
		cost    float64
		apiMs   int64
		outputs map[string]int
	}{
		{result("S", 2.00, 416000, models(95)) + result("R", 0.50, 60000, `"claude-opus-4-1":{"outputTokens":40}`),
			2.50, 476000, map[string]int{"claude-opus-4-1": 135, "claude-haiku-4-5": 300, "claude-sonnet-4-5": 200}},
		{result("S", 2.30, 466000, models(115)),
			0.30, 50000, map[string]int{"claude-opus-4-1": 20}},
		{result("S", 2.55, 506000, models(145)),
			0.25, 40000, map[string]int{"claude-opus-4-1": 30}},
	}

	d := newDriver()
	var base driverkit.SessionBaselines
	for i, l := range logs {
		path := filepath.Join(dir, fmt.Sprintf("log%d.jsonl", i))
		if err := os.WriteFile(path, []byte(l.body), 0o644); err != nil {
			t.Fatal(err)
		}
		r, after, err := d.ExtractUsageAfter(path, base)
		if err != nil {
			t.Fatal(err)
		}
		base = after
		if math.Abs(r.Totals.TotalCostUSD-l.cost) > 1e-9 || r.Totals.DurationApiMs != l.apiMs {
			t.Errorf("log %d = cost %v api %d, want cost %v api %d", i, r.Totals.TotalCostUSD, r.Totals.DurationApiMs, l.cost, l.apiMs)
		}
		got := map[string]int{}
		for _, m := range r.SummedByModel {
			if m.OutputTokens != 0 {
				got[m.Model] = m.OutputTokens
			}
		}
		if fmt.Sprint(got) != fmt.Sprint(l.outputs) {
			t.Errorf("log %d model output = %v, want %v", i, got, l.outputs)
		}
	}
}
