package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/golden"
	"spindrift.dev/launcher/internal/hostpaths"
	"spindrift.dev/launcher/internal/passmachine"
)

func statsOp(op claude.SpindriftOp) string { return claude.EncodeSpindriftOp(op) }

func statsAssistant(id string) string {
	return fmt.Sprintf(`{"type":"assistant","message":{"id":%q,"model":"m","content":[]}}`+"\n", id)
}

func statsResult(ts string, cost float64, turns int, dur, apiDur int64, model string) string {
	return statsResultText("", ts, cost, turns, dur, apiDur, model)
}

// statsResultText is statsResult with the review verdict prose in the result event.
func statsResultText(text, ts string, cost float64, turns int, dur, apiDur int64, model string) string {
	return fmt.Sprintf(`{"type":"result","result":%q,"timestamp":%q,"num_turns":%d,"total_cost_usd":%g,"duration_ms":%d,"duration_api_ms":%d,`+
		`"usage":{"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":30,"cache_creation_input_tokens":40},`+
		`"modelUsage":{%q:{}}}`+"\n", text, ts, turns, cost, dur, apiDur, model)
}

// statsWriteDispositions is an assistant event writing the fix pass's dispositions
// file, followed by the tool_result that shows the Write succeeded.
func statsWriteDispositions(id, content string) string {
	in, _ := json.Marshal(map[string]string{"file_path": passmachine.DispositionsPath, "content": content})
	return fmt.Sprintf(`{"type":"assistant","message":{"id":%q,"model":"m","content":[{"type":"tool_use","id":%q,"name":"Write","input":%s}]}}`+"\n"+
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":%q,"content":"ok"}]}}`+"\n", id, id, in, id)
}

// statsFixtureRoot writes a spread of chain logs under a fresh root's log
// directory: every kind, a multi-pass work Dispatch, a quarantined earlier
// run, and a zero-cost crash whose claim time falls back to a pinned mtime.
func statsFixtureRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := hostpaths.LogDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name string, lines ...string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(strings.Join(lines, "")), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	write("issue-42.log",
		statsOp(claude.SpindriftOp{Op: "pass_start", Pass: 1, Role: "implement"}),
		`{"type":"system","timestamp":"2026-10-07T12:34:56.789Z"}`+"\n",
		statsAssistant("a"), statsAssistant("b"),
		statsResult("2026-10-07T12:35:00Z", 1.5, 7, 240000, 3000, "claude-opus"),
		statsOp(claude.SpindriftOp{Op: "pass_start", Pass: 2, Role: "review"}),
		statsAssistant("c"),
		statsResultText("VERDICT: BLOCK\n- missing nil check in handler", "2026-10-07T12:36:00Z", 0.5, 2, 60000, 900, "claude-sonnet"),
		statsOp(claude.SpindriftOp{Op: "verdict", Pass: 2, Verdict: "BLOCK"}),
		statsOp(claude.SpindriftOp{Op: "pass_start", Pass: 3, Role: "fix"}),
		statsWriteDispositions("e", "1. nil check: fixed"),
		statsResult("2026-10-07T12:37:00Z", 1, 3, 120000, 1500, "claude-opus"),
		statsOp(claude.SpindriftOp{Op: "pass_start", Pass: 4, Role: "review"}),
		statsAssistant("d"),
		statsResultText("VERDICT: APPROVE", "2026-10-07T12:38:00Z", 0.25, 1, 30000, 400, "claude-sonnet"),
		statsOp(claude.SpindriftOp{Op: "verdict", Pass: 4, Verdict: "APPROVE"}),
	)
	write("issue-42.log.prior-run.1",
		statsOp(claude.SpindriftOp{Op: "pass_start", Pass: 1, Role: "implement"}),
		`{"type":"system","timestamp":"2026-10-06T09:00:00Z"}`+"\n",
		statsAssistant("p"),
		statsResult("2026-10-06T09:05:00Z", 0.75, 4, 300000, 2000, "claude-opus"),
	)
	write("issue-butler-deps.log",
		`{"type":"system","timestamp":"2026-10-07T01:00:00Z"}`+"\n",
		statsAssistant("x"),
		statsResult("2026-10-07T01:00:30Z", 2, 4, 90000, 80000, "claude-opus"),
	)
	write("issue-7.log",
		`{"type":"system","timestamp":"2026-10-05T02:00:00Z"}`+"\n",
		statsAssistant("y"),
		statsResult("2026-10-05T02:01:00Z", 0.1, 1, 6000, 5000, "claude-haiku"),
	)
	crash := write("issue-99.log", statsOp(claude.SpindriftOp{Op: "pass_start", Pass: 1, Role: "implement"}))
	mtime := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
	if err := os.Chtimes(crash, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	// Fix and conflict-resolve logs belong to another file of a chain and must
	// not become Records of their own.
	write("issue-42-fix-1.log", statsResult("2026-10-07T13:00:00Z", 9, 1, 1, 1, "claude-opus"))
	return root
}

func runStats(t *testing.T, root string, args ...string) (string, string) {
	t.Helper()
	t.Chdir(root)
	var stdout, stderr bytes.Buffer
	if code := mainRun(append([]string{"stats"}, args...), &stdout, &stderr); code != 0 {
		t.Fatalf("mainRun(stats %v) code = %d, stderr:\n%s", args, code, stderr.String())
	}
	return stdout.String(), stderr.String()
}

func TestStats_Golden(t *testing.T) {
	root := statsFixtureRoot(t)
	update := golden.Update()
	// runStats changes directory, so pin the golden directory first.
	goldenDir, err := filepath.Abs("testdata/golden")
	if err != nil {
		t.Fatal(err)
	}

	text, _ := runStats(t, root)
	if err := golden.CompareOrUpdateText(filepath.Join(goldenDir, "stats.txt"), []byte(text), update); err != nil {
		t.Error(err)
	}
	jsonl, _ := runStats(t, root, "--json")
	if err := golden.CompareOrUpdateText(filepath.Join(goldenDir, "stats.jsonl"), []byte(jsonl), update); err != nil {
		t.Error(err)
	}

	// A second run must neither duplicate nor re-parse: rewrite a log's
	// content with its size and mtime preserved, so only a re-parse could
	// change the output.
	statsTamperCost(t, root, "issue-7.log")
	if again, _ := runStats(t, root); again != text {
		t.Errorf("second stats run differs:\n%s\nwant\n%s", again, text)
	}
	if again, _ := runStats(t, root, "--json"); again != jsonl {
		t.Errorf("second stats --json run differs:\n%s\nwant\n%s", again, jsonl)
	}
}

// statsTamperCost rewrites a fixture log's 0.1 cost to 0.9 in place, keeping
// its size and mtime so only a forced re-parse can notice.
func statsTamperCost(t *testing.T, root, name string) {
	t.Helper()
	logPath := filepath.Join(hostpaths.LogDir(root), name)
	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatal(err)
	}
	orig, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	tampered := bytes.Replace(orig, []byte(`"total_cost_usd":0.1,`), []byte(`"total_cost_usd":0.9,`), 1)
	if bytes.Equal(tampered, orig) {
		t.Fatal("fixture rewrite did not change the log")
	}
	if err := os.WriteFile(logPath, tampered, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(logPath, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
}

func statsRemoveLogs(t *testing.T, root string, names ...string) {
	t.Helper()
	for _, n := range names {
		if err := os.Remove(filepath.Join(hostpaths.LogDir(root), n)); err != nil {
			t.Fatal(err)
		}
	}
}

// A Record is the history: it survives its logs being deleted, under plain
// stats and under --reingest alike.
func TestStats_RecordsOutliveLogs(t *testing.T) {
	for _, args := range [][]string{{"--json"}, {"--json", "--reingest"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			root := statsFixtureRoot(t)
			before, _ := runStats(t, root, "--json")
			statsRemoveLogs(t, root, "issue-42.log", "issue-42.log.prior-run.1")
			if after, _ := runStats(t, root, args...); after != before {
				t.Errorf("stats %v after deleting logs differs:\n%s\nwant\n%s", args, after, before)
			}
		})
	}
}

func TestStats_ReingestRepairsChangedLogKeepsDeletedRecord(t *testing.T) {
	root := statsFixtureRoot(t)
	before, _ := runStats(t, root, "--json")
	statsRemoveLogs(t, root, "issue-42.log", "issue-42.log.prior-run.1")
	statsTamperCost(t, root, "issue-7.log")

	if plain, _ := runStats(t, root, "--json"); plain != before {
		t.Errorf("plain stats re-parsed an unchanged-stat log:\n%s\nwant\n%s", plain, before)
	}
	after, _ := runStats(t, root, "--json", "--reingest")
	wantLines := strings.Split(before, "\n")
	gotLines := strings.Split(after, "\n")
	if len(gotLines) != len(wantLines) {
		t.Fatalf("--reingest changed the record count:\n%s\nwant\n%s", after, before)
	}
	changed := 0
	for i := range gotLines {
		if gotLines[i] == wantLines[i] {
			continue
		}
		changed++
		if !strings.Contains(gotLines[i], `"dispatch_key":"7"`) || !strings.Contains(gotLines[i], `"usd":0.9`) {
			t.Errorf("unexpected record change:\n%s\nwas\n%s", gotLines[i], wantLines[i])
		}
	}
	if changed != 1 {
		t.Errorf("--reingest changed %d records, want only issue 7's", changed)
	}
}

func TestStats_EmptyRoot(t *testing.T) {
	out, _ := runStats(t, t.TempDir())
	if want := "Records: 0  Passes: 0  Notional USD: $0.00 (API-equivalent)\n"; out != want {
		t.Errorf("stats on an empty root = %q, want %q", out, want)
	}
}

func TestStats_RejectsUnknownArgument(t *testing.T) {
	t.Chdir(t.TempDir())
	var stdout, stderr bytes.Buffer
	if code := mainRun([]string{"stats", "--bogus"}, &stdout, &stderr); code == 0 {
		t.Errorf("exit = 0, want non-zero; stderr=%q", stderr.String())
	}
}
