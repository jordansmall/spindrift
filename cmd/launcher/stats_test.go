package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/golden"
	"spindrift.dev/launcher/internal/hostpaths"
	"spindrift.dev/launcher/internal/passmachine"
	"spindrift.dev/launcher/internal/settle"
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
	// Fix and conflict-resolve logs are satellites of a Dispatch: this one starts
	// after work:42 and joins it rather than becoming a Record of its own.
	write("issue-42-fix-1.log", statsResult("2026-10-07T13:00:00Z", 9, 1, 1, 1, "claude-opus"))
	// The acceptance chain for one key: an earlier research run, quarantined,
	// then a work run with a fix pass and a conflict-resolve pass that began
	// after it. Two Records, the satellites joining the work one.
	write("issue-55.log.prior-run.1",
		`{"type":"system","timestamp":"2026-10-07T10:00:00Z"}`+"\n",
		statsAssistant("r"),
		statsResult("2026-10-07T10:03:00Z", 0.4, 2, 180000, 1200, "claude-sonnet"),
		"SPINDRIFT_OUTCOME issue=55 landing=none status=recommend note=x\n",
	)
	write("issue-55.log",
		statsOp(claude.SpindriftOp{Op: "pass_start", Pass: 1, Role: "implement"}),
		`{"type":"system","timestamp":"2026-10-07T14:00:00Z"}`+"\n",
		statsResult("2026-10-07T14:05:00Z", 2.25, 9, 300000, 4000, "claude-opus"),
	)
	write("issue-55-fix-1.log",
		`{"type":"system","timestamp":"2026-10-07T14:10:00Z"}`+"\n",
		statsResult("2026-10-07T14:12:00Z", 0.6, 3, 120000, 1500, "claude-opus"),
	)
	write("issue-55-conflict-resolve.log",
		`{"type":"system","timestamp":"2026-10-07T14:20:00Z"}`+"\n",
		statsResult("2026-10-07T14:21:00Z", 0.3, 2, 60000, 700, "claude-sonnet"),
	)
	// No result event: a Record that cost nothing.
	write("issue-60.log", `{"type":"system","timestamp":"2026-10-07T15:00:00Z"}`+"\n")
	return root
}

// statsSecondRoot is a small root of its own, as a Daemon checkout would be:
// one research Dispatch and one work Dispatch.
func statsSecondRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := hostpaths.LogDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	logs := map[string]string{
		"issue-8.log": `{"type":"system","timestamp":"2026-10-07T12:35:30Z"}` + "\n" +
			statsResult("2026-10-07T12:36:30Z", 0.2, 1, 60000, 800, "claude-sonnet") +
			"SPINDRIFT_OUTCOME issue=8 landing=none status=reject note=y\n",
		"issue-9.log": statsOp(claude.SpindriftOp{Op: "pass_start", Pass: 1, Role: "implement"}) +
			`{"type":"system","timestamp":"2026-10-08T08:00:00Z"}` + "\n" +
			statsResult("2026-10-08T08:10:00Z", 1.1, 5, 600000, 2500, "claude-opus"),
	}
	for name, content := range logs {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// statsNormalize swaps each temp root's absolute path for a stable
// placeholder so goldens do not embed a per-run directory.
func statsNormalize(out string, roots map[string]string) string {
	for placeholder, root := range roots {
		out = strings.ReplaceAll(out, root, placeholder)
	}
	return out
}

// statsGolden compares got with the named golden under testdata/golden.
func statsGolden(t *testing.T, name, got string) {
	t.Helper()
	// runStats changes directory, so resolve the golden directory from the
	// test's source location.
	_, file, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(file), "testdata", "golden", name)
	if err := golden.CompareOrUpdateText(path, []byte(got), golden.Update()); err != nil {
		t.Error(err)
	}
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
	roots := map[string]string{"$ROOT": root}

	text, _ := runStats(t, root)
	statsGolden(t, "stats.txt", statsNormalize(text, roots))
	jsonl, _ := runStats(t, root, "--json")
	statsGolden(t, "stats.jsonl", statsNormalize(jsonl, roots))

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
	if want := "Records: 0  Passes: 0  Notional USD: $0.00 (API-equivalent)  Landed keys: 0  USD per landed key: -  Outcome source: dispatch_settled 0, none 0\n"; out != want {
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

func TestRenderStats_LandedKeysAndSourceMix(t *testing.T) {
	const settled = dispatchrecord.OutcomeSourceSettled
	rec := func(kind, key, outcome, reason, source string, usd float64) dispatchrecord.Record {
		return dispatchrecord.Record{
			Kind: kind, DispatchKey: key, Outcome: outcome, Reason: reason, OutcomeSource: source,
			Passes: []dispatchrecord.Pass{{Role: "implement", USD: usd}},
		}
	}
	records := []dispatchrecord.Record{
		rec("work", "1", "complete", settle.ReasonMerged, settled, 1),
		rec("work", "1", "complete", settle.ReasonMerged, settled, 1), // a re-run of the same key
		rec("work", "2", "failed", settle.ReasonCIRed, settled, 2),
		rec("work", "3", "unknown", "", dispatchrecord.OutcomeSourceNone, 4),
		rec("research", "1", "complete", settle.ReasonVerdict, settled, 2), // a verdict, not a landing
		rec("work", "4", "complete", settle.ReasonMerged, settled, 2),
		rec("work", "5", "complete", settle.ReasonAlreadyResolved, settled, 0),
		rec("work", "6", "complete", settle.ReasonManual, settled, 0),
		rec("work", "7", "complete", settle.ReasonAutoMergeEnqueued, settled, 0),
		rec("work", "8", "complete", settle.ReasonMergeBlocked, settled, 0),
		rec("butler", "chore", "complete", settle.ReasonFindingsFiled, settled, 0),
	}
	summary := func(records []dispatchrecord.Record) string {
		var buf bytes.Buffer
		if err := renderStats(&buf, records); err != nil {
			t.Fatal(err)
		}
		first, _, _ := strings.Cut(buf.String(), "\n")
		_, tail, _ := strings.Cut(first, "(API-equivalent)  ")
		return tail
	}
	// Only a merged Record counts as landed; $12 over 2 merged keys.
	if got, want := summary(records), "Landed keys: 2  USD per landed key: $6.00  Outcome source: dispatch_settled 10, none 1"; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
	if got, want := summary(records[2:4]), "Landed keys: 0  USD per landed key: -  Outcome source: dispatch_settled 1, none 1"; got != want {
		t.Errorf("no-landing summary = %q, want %q", got, want)
	}
	var buf bytes.Buffer
	if err := renderStats(&buf, records[:1]); err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(strings.Split(buf.String(), "\n")[1], "Landed") {
		t.Errorf("landed keys must ride the summary line, not a second line:\n%s", buf.String())
	}
}

func TestStats_MultiRootGolden(t *testing.T) {
	a, b := statsFixtureRoot(t), statsSecondRoot(t)
	roots := map[string]string{"$ROOT_A": a, "$ROOT_B": b}

	text, _ := runStats(t, a, "--root", a, "--root", b)
	statsGolden(t, "stats-multiroot.txt", statsNormalize(text, roots))
	jsonl, _ := runStats(t, a, "--root", a, "--root="+b, "--json")
	statsGolden(t, "stats-multiroot.jsonl", statsNormalize(jsonl, roots))

	if !strings.Contains(jsonl, `"root":`+fmt.Sprintf("%q", a)) || !strings.Contains(jsonl, `"root":`+fmt.Sprintf("%q", b)) {
		t.Errorf("--json output does not tag Records with both roots:\n%s", jsonl)
	}
	// Naming a root twice must not double its Records.
	if again, _ := runStats(t, a, "--root", a, "--root", b, "--root", a, "--json"); again != jsonl {
		t.Errorf("duplicate --root changed the output:\n%s\nwant\n%s", again, jsonl)
	}
}

func TestStats_FilterGolden(t *testing.T) {
	a, b := statsFixtureRoot(t), statsSecondRoot(t)
	roots := map[string]string{"$ROOT_A": a, "$ROOT_B": b}

	since, _ := runStats(t, a, "--root", a, "--root", b, "--since", "2026-10-07T14:00:00Z", "--json")
	statsGolden(t, "stats-since.jsonl", statsNormalize(since, roots))
	if eq, _ := runStats(t, a, "--root", a, "--root", b, "--since=2026-10-07T14:00:00Z", "--json"); eq != since {
		t.Errorf("--since=value differs from --since value")
	}

	kind, _ := runStats(t, a, "--root", a, "--root", b, "--kind", "research")
	statsGolden(t, "stats-kind-research.txt", statsNormalize(kind, roots))
	kindJSON, _ := runStats(t, a, "--root", a, "--root", b, "--kind", "research", "--json")
	statsGolden(t, "stats-kind-research.jsonl", statsNormalize(kindJSON, roots))
}

func TestStats_SinceTakesADate(t *testing.T) {
	root := statsFixtureRoot(t)
	day, _ := runStats(t, root, "--since", "2026-10-07", "--json")
	exact, _ := runStats(t, root, "--since", "2026-10-07T00:00:00Z", "--json")
	if day != exact {
		t.Errorf("--since 2026-10-07 differs from midnight UTC:\n%s\nwant\n%s", day, exact)
	}
	if strings.Contains(day, "2026-10-06") || strings.Contains(day, "2026-10-05") {
		t.Errorf("--since 2026-10-07 kept an earlier Record:\n%s", day)
	}
	if !strings.Contains(day, "2026-10-07T12:34:56.789Z") {
		t.Errorf("--since 2026-10-07 dropped a Record on that day:\n%s", day)
	}
}

func TestStats_ExcludingInferredKeepsStamped(t *testing.T) {
	root := statsFixtureRoot(t)
	claim := time.Date(2026, 10, 7, 16, 0, 0, 0, time.UTC)
	id := dispatchrecord.RecordID("work", "70", claim)
	stamped := statsOp(claude.SpindriftOp{Op: "dispatch_start", Start: &claude.DispatchStart{
		RecordID: id, Kind: "work", DispatchKey: "70", ClaimTime: claim, Started: claim,
	}}) + `{"type":"system","timestamp":"2026-10-07T16:00:01Z"}` + "\n" +
		statsResult("2026-10-07T16:05:00Z", 0.5, 2, 60000, 900, "claude-opus")
	logPath := filepath.Join(hostpaths.LogDir(root), "issue-70.log")
	if err := os.WriteFile(logPath, []byte(stamped), 0o644); err != nil {
		t.Fatal(err)
	}

	out, _ := runStats(t, root, "--include-inferred=false", "--json")
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("--include-inferred=false --json emitted %d lines, want the one stamped Record:\n%s", len(lines), out)
	}
	var rec struct {
		ID          string `json:"record_id"`
		Attribution string `json:"attribution"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("decoding %q: %v", lines[0], err)
	}
	if rec.ID != id || rec.Attribution != dispatchrecord.AttributionStamped {
		t.Errorf("kept Record = %+v, want id %q attribution %q", rec, id, dispatchrecord.AttributionStamped)
	}
	text, _ := runStats(t, root, "--include-inferred=false")
	if !strings.Contains(text, "Records: 1  ") {
		t.Errorf("--include-inferred=false summary lacks Records: 1:\n%s", text)
	}

	def, _ := runStats(t, root, "--json")
	for _, want := range []string{id, `"attribution":"inferred"`, `"attribution":"stamped"`} {
		if !strings.Contains(def, want) {
			t.Errorf("default output lacks %s:\n%s", want, def)
		}
	}
	if withAll, _ := runStats(t, root, "--include-inferred=true", "--json"); withAll != def {
		t.Errorf("--include-inferred=true = %q, want the default output %q", withAll, def)
	}
}

func TestStats_RejectsBadArguments(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, args := range [][]string{
		{"--since", "yesterday"},
		{"--since"},
		{"--root"},
		{"--kind", ""},
		{"--kind", "wrok"},
		{"--include-inferred=maybe"},
		{"--json=1"},
	} {
		var stdout, stderr bytes.Buffer
		if code := mainRun(append([]string{"stats"}, args...), &stdout, &stderr); code != 1 {
			t.Errorf("stats %v exit = %d, want 1; stderr=%q", args, code, stderr.String())
		}
		if !strings.Contains(stderr.String(), "usage: spindrift stats") {
			t.Errorf("stats %v stderr lacks usage: %q", args, stderr.String())
		}
	}
}

func TestStats_RejectsMissingRoot(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-checkout")
	t.Chdir(t.TempDir())
	var stdout, stderr bytes.Buffer
	if code := mainRun([]string{"stats", "--root", missing}, &stdout, &stderr); code != 1 {
		t.Errorf("exit = %d, want 1; stderr=%q", code, stderr.String())
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Errorf("stats created the missing root %s (stat err %v)", missing, err)
	}
}

func TestStats_RejectsNonDirectoryRootWithRealError(t *testing.T) {
	file := filepath.Join(t.TempDir(), "plain-file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "no-such-checkout")
	t.Chdir(t.TempDir())
	for root, want := range map[string]string{file: "not a directory", missing: "no such file"} {
		var stdout, stderr bytes.Buffer
		if code := mainRun([]string{"stats", "--root", root}, &stdout, &stderr); code != 1 {
			t.Errorf("root %s exit = %d, want 1", root, code)
		}
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("root %s stderr = %q, want it to contain %q", root, stderr.String(), want)
		}
	}
}

func TestStats_SymlinkedRootCountsOnce(t *testing.T) {
	a := statsFixtureRoot(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(a, link); err != nil {
		t.Fatal(err)
	}
	want, _ := runStats(t, a, "--root", a, "--json")
	got, _ := runStats(t, a, "--root", a, "--root", link, "--json")
	if got != want {
		t.Errorf("a symlinked second root changed the output:\n%s\nwant\n%s", got, want)
	}
}
