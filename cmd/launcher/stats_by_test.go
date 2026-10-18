package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/daemon"
	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/hostop"
	"spindrift.dev/launcher/internal/hostpaths"
	"spindrift.dev/launcher/internal/recordstats"
)

// statsStampedLog is a stamped work Dispatch log: dispatch_start, optional
// prompt_hashes, then one pass per (role, models) entry.
func statsStampedLog(key string, claim time.Time, start hostop.DispatchStart, hashes map[string]string, passes ...[2]string) string {
	start.RecordID = dispatchrecord.RecordID("work", key, claim)
	start.Kind, start.DispatchKey, start.ClaimTime, start.Started = "work", key, claim, claim
	out := statsOp(hostop.SpindriftOp{Op: "dispatch_start", Start: &start})
	if hashes != nil {
		out += statsOp(hostop.SpindriftOp{Op: "prompt_hashes", PromptHashes: &hostop.PromptHashes{RecordID: start.RecordID, Roles: hashes}})
	}
	for i, p := range passes {
		if start.Driver == "opencode" {
			out += statsOp(hostop.SpindriftOp{Op: "pass_start", Pass: i + 1, Role: p[0]}) +
				statsOpencodePass(i+1, claim.Add(time.Duration(i+1)*time.Minute), strings.Split(p[1], "+"))
			continue
		}
		ts := claim.Add(time.Duration(i+1) * time.Minute).Format(time.RFC3339)
		modelSpec, subagent, _ := strings.Cut(p[1], "@")
		models := make([]string, 0, 2)
		for _, m := range strings.Split(modelSpec, "+") {
			models = append(models, `"`+m+`":{}`)
		}
		out += statsOp(hostop.SpindriftOp{Op: "pass_start", Pass: i + 1, Role: p[0]})
		if subagent != "" {
			out += statsSubagentLines(i+1, subagent)
		}
		out += `{"type":"system","timestamp":"` + ts + `"}` + "\n" +
			`{"type":"result","result":"","timestamp":"` + ts + `","num_turns":2,"total_cost_usd":1.5,"duration_ms":60000,"duration_api_ms":900,` +
			`"usage":{"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":30,"cache_creation_input_tokens":40},` +
			`"modelUsage":{` + strings.Join(models, ",") + `}}` + "\n"
	}
	return out
}

// statsSubagentLines is a main-loop spawn of a subagent role and one subagent
// call, so the pass carries a main and a subagent usage row. As in a real
// claude log, the main row's input and cache tokens equal the result's and the
// subagent's come on top. The claude driver drops the subagent's
// `output_tokens: 99` (the goldens show worker output 0). A pass opts in by
// suffixing its models with "@<role>"; the models stay those of the result.
func statsSubagentLines(pass int, role string) string {
	id := fmt.Sprintf("toolu_%d", pass)
	return fmt.Sprintf(`{"type":"assistant","message":{"id":"msg_main_%d","content":[{"type":"tool_use","id":%q,"name":"Task","input":{"subagent_type":%q}}],"usage":{"input_tokens":10,"output_tokens":1,"cache_read_input_tokens":30,"cache_creation_input_tokens":40}}}`+"\n"+
		`{"type":"assistant","parent_tool_use_id":%q,"message":{"id":"msg_sub_%d","content":[],"usage":{"input_tokens":3,"output_tokens":99,"cache_read_input_tokens":5,"cache_creation_input_tokens":7}}}`+"\n",
		pass, id, role, id, pass)
}

// statsOpencodePass is the opencode counterpart of the claude result line: two
// model calls (the first model, then the last) summing to the same totals and
// spanning 60s from the first step_start to the last step_finish.
func statsOpencodePass(pass int, t0 time.Time, models []string) string {
	base := t0.UnixMilli()
	var out string
	for call, at := range [][2]int64{{0, 1000}, {2000, 60000}} {
		model := models[min(call, len(models)-1)]
		id := fmt.Sprintf("msg_%d_%d", pass, call)
		out += fmt.Sprintf(`{"type":"step_start","timestamp":%d,"part":{"messageID":%q}}`, base+at[0], id) + "\n" +
			fmt.Sprintf(`{"type":"step_finish","timestamp":%d,"part":{"messageID":%q,"modelID":%q,`+
				`"tokens":{"input":5,"output":10,"reasoning":0,"cache":{"write":20,"read":15}},"cost":0.75}}`, base+at[1], id, model) + "\n"
	}
	return out
}

func statsWriteLogs(t *testing.T, root string, logs map[string]string) {
	t.Helper()
	dir := hostpaths.LogDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range logs {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// statsStampedRoot holds three stamped Dispatches that differ in revision,
// knobs, prompt hashes and models, one stamped Dispatch recording none of
// them bar one empty-valued knob, and one inferred Record.
type statsGroupedLine struct {
	Group       string   `json:"group"`
	GroupKeys   []string `json:"group_keys"`
	DispatchKey string   `json:"dispatch_key"`
	ClaimTime   string   `json:"claim_time"`
	Root        string   `json:"root"`
}

func statsGroupedLines(t *testing.T, jsonl string) []statsGroupedLine {
	t.Helper()
	var recs []statsGroupedLine
	for _, line := range strings.Split(strings.TrimSuffix(jsonl, "\n"), "\n") {
		var rec statsGroupedLine
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatal(err)
		}
		recs = append(recs, rec)
	}
	return recs
}

func statsStampedRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	day := time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)
	statsWriteLogs(t, root, map[string]string{
		"issue-70.log": statsStampedLog("70", day,
			hostop.DispatchStart{Revision: "rev-a", Driver: "claude", Knobs: map[string]string{"MERGE_MODE": "auto"}},
			map[string]string{"implement": "hash-i1", "review": "hash-r1"},
			[2]string{"implement", "claude-opus@worker"}, [2]string{"review", "claude-sonnet"}),
		"issue-71.log": statsStampedLog("71", day.Add(time.Hour),
			hostop.DispatchStart{Revision: "rev-b", Driver: "opencode", Knobs: map[string]string{"MERGE_MODE": "manual"}},
			map[string]string{"implement": "hash-i2"},
			[2]string{"implement", "claude-opus+claude-haiku"}, [2]string{"review", "claude-sonnet"}),
		"issue-72.log": statsStampedLog("72", day.Add(2*time.Hour), hostop.DispatchStart{Driver: "claude", Knobs: map[string]string{"MERGE_MODE": ""}}, nil,
			[2]string{"implement", "claude-sonnet"}),
		"issue-7.log": `{"type":"system","timestamp":"2026-10-07T12:00:00Z"}` + "\n" +
			statsResult("2026-10-07T12:01:00Z", 0.1, 1, 6000, 5000, "claude-haiku"),
	})
	return root
}

// statsStampedSecondRoot adds a rev-a / auto Dispatch of another checkout, so
// the group spans roots.
func statsStampedSecondRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	statsWriteLogs(t, root, map[string]string{
		"issue-80.log": statsStampedLog("80", time.Date(2026, 10, 7, 14, 0, 0, 0, time.UTC),
			hostop.DispatchStart{Revision: "rev-a", Driver: "claude", Knobs: map[string]string{"MERGE_MODE": "auto"}},
			map[string]string{"implement": "hash-i1"},
			[2]string{"implement", "claude-opus"}),
	})
	return root
}

func TestStats_ByGolden(t *testing.T) {
	root := statsStampedRoot(t)
	roots := map[string]string{"$ROOT": root}
	for _, tc := range []struct{ name, by string }{
		{"revision", "revision"},
		{"model", "model"},
		{"prompt-implement", "prompt:implement"},
		{"knob-merge-mode", "knob:MERGE_MODE"},
		{"role", "role"},
		{"driver", "driver"},
		{"driver-role", "driver,role"},
		{"agent", "agent"},
		{"role-agent", "role,agent"},
		{"driver-agent", "driver,agent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text, _ := runStats(t, root, "--by", tc.by)
			statsGolden(t, "stats-by-"+tc.name+".txt", statsNormalize(text, roots))
			jsonl, _ := runStats(t, root, "--by="+tc.by, "--json")
			statsGolden(t, "stats-by-"+tc.name+".jsonl", statsNormalize(jsonl, roots))
		})
	}
}

func passUsage(p dispatchrecord.Pass) dispatchrecord.PassAgent {
	return dispatchrecord.PassAgent{APICalls: p.APICalls, InputTokens: p.InputTokens, OutputTokens: p.OutputTokens,
		CacheReadInputTokens: p.CacheReadInputTokens, CacheCreationInputTokens: p.CacheCreationInputTokens}
}

func addUsage(dst *dispatchrecord.PassAgent, src dispatchrecord.PassAgent) {
	dst.APICalls += src.APICalls
	dst.InputTokens += src.InputTokens
	dst.OutputTokens += src.OutputTokens
	dst.CacheReadInputTokens += src.CacheReadInputTokens
	dst.CacheCreationInputTokens += src.CacheCreationInputTokens
}

// Over a real claude log, grouping by agent adds the subagents' input and cache
// tokens on top of the ungrouped total (see recordstats.GroupAgents).
func TestGroupAgentsOverRealClaudeLogAddsSubagentTokens(t *testing.T) {
	rec := corpusRecord(t, "claude", "claude/testdata/corpus/real-fix-pass-subagents")
	var want, got, sub dispatchrecord.PassAgent
	var wantUSD, gotUSD float64
	var wantTurns, gotTurns int
	var wantMs, gotMs int64
	for _, p := range rec.Passes {
		addUsage(&want, passUsage(p))
		wantUSD += p.USD
		wantTurns += p.Turns
		wantMs += p.DurationMs
		for _, a := range p.Agents[1:] {
			addUsage(&sub, a)
		}
	}
	for _, g := range recordstats.GroupAgents([]dispatchrecord.Record{rec}) {
		for _, r := range g.Records {
			for _, p := range r.Passes {
				addUsage(&got, passUsage(p))
				gotUSD += p.USD
				gotTurns += p.Turns
				gotMs += p.DurationMs
			}
		}
	}
	if sub.InputTokens == 0 || sub.CacheReadInputTokens == 0 || sub.CacheCreationInputTokens == 0 {
		t.Fatalf("corpus has no subagent tokens: %+v", sub)
	}
	if got.OutputTokens != want.OutputTokens || got.APICalls != want.APICalls ||
		math.Abs(gotUSD-wantUSD) > 1e-9 || gotTurns != wantTurns || gotMs != wantMs {
		t.Errorf("across groups output %d calls %d usd %v turns %d ms %d; ungrouped %d %d %v %d %d",
			got.OutputTokens, got.APICalls, gotUSD, gotTurns, gotMs,
			want.OutputTokens, want.APICalls, wantUSD, wantTurns, wantMs)
	}
	if got.InputTokens != want.InputTokens+sub.InputTokens ||
		got.CacheReadInputTokens != want.CacheReadInputTokens+sub.CacheReadInputTokens ||
		got.CacheCreationInputTokens != want.CacheCreationInputTokens+sub.CacheCreationInputTokens {
		t.Errorf("input/cache across groups = %d/%d/%d, want ungrouped %d/%d/%d plus subagents %d/%d/%d",
			got.InputTokens, got.CacheReadInputTokens, got.CacheCreationInputTokens,
			want.InputTokens, want.CacheReadInputTokens, want.CacheCreationInputTokens,
			sub.InputTokens, sub.CacheReadInputTokens, sub.CacheCreationInputTokens)
	}
}

func TestStats_ByRoleTextIsUnchanged(t *testing.T) {
	root := statsFixtureRoot(t)
	plain, _ := runStats(t, root)
	if byRole, _ := runStats(t, root, "--by", "role"); byRole != plain {
		t.Errorf("--by role text differs from the default:\n%s\nwant\n%s", byRole, plain)
	}
}

func TestStats_ByMergesAcrossRoots(t *testing.T) {
	a, b := statsStampedRoot(t), statsStampedSecondRoot(t)
	roots := map[string]string{"$ROOT_A": a, "$ROOT_B": b}

	text, _ := runStats(t, a, "--root", a, "--root", b, "--by", "revision")
	statsGolden(t, "stats-by-multiroot.txt", statsNormalize(text, roots))
	jsonl, _ := runStats(t, a, "--root", a, "--root", b, "--by", "revision", "--json")
	statsGolden(t, "stats-by-multiroot.jsonl", statsNormalize(jsonl, roots))

	groupRoots := map[string]map[string]bool{}
	for _, rec := range statsGroupedLines(t, jsonl) {
		if groupRoots[rec.Group] == nil {
			groupRoots[rec.Group] = map[string]bool{}
		}
		groupRoots[rec.Group][rec.Root] = true
	}
	if len(groupRoots["rev-a"]) != 2 {
		t.Errorf("group rev-a spans roots %v, want both", groupRoots["rev-a"])
	}
}

func TestStats_ByRejectsBadDimension(t *testing.T) {
	root := statsStampedRoot(t)
	t.Chdir(root)
	for _, tc := range []struct{ by, want string }{
		{"flavour", `invalid --by "flavour": element "flavour": want role, agent, revision, model, driver, prompt:<role> or knob:<NAME>`},
		{"prompt:nonsense", `unknown prompt role "nonsense", want one of legacy, implement, fix, land, review, delta-review, research, butler`},
		{"prompt:", `unknown prompt role ""`},
		{"knob:NO_SUCH_KNOB", `unknown knob "NO_SUCH_KNOB"`},
		{"knob:GH_TOKEN", "knob GH_TOKEN is secret, never recorded"},
		{"knob:", "knob name is empty"},
		{"driver,", `invalid --by "driver,": empty dimension`},
		{",role", `invalid --by ",role": empty dimension`},
		{"role,role", `invalid --by "role,role": repeated dimension "role"`},
		{"driver,bogus", `invalid --by "driver,bogus": element "bogus": want role`},
		{"driver,knob:GH_TOKEN", `invalid --by "driver,knob:GH_TOKEN": element "knob:GH_TOKEN": knob GH_TOKEN is secret`},
		{"driver, ", `invalid --by "driver, ": empty dimension`},
		{"driver, role, driver", `invalid --by "driver, role, driver": repeated dimension "driver"`},
	} {
		t.Run(tc.by, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := mainRun([]string{"stats", "--by", tc.by}, &stdout, &stderr); code != 1 {
				t.Fatalf("code = %d, want 1", code)
			}
			if !strings.Contains(stderr.String(), tc.want) || !strings.Contains(stderr.String(), "usage: spindrift stats") {
				t.Errorf("stderr = %q, want %q and the usage line", stderr.String(), tc.want)
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout = %q, want empty", stdout.String())
			}
		})
	}
}

// statsEventsRoot is a git checkout holding inferred Dispatches and the
// Daemon's Events file: issue 10 has its own box event, issue 20 has two
// Dispatches of which only the first has one (the second was a manual
// dispatch), and issue 30 has none.
func statsEventsRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	gitRun(t, root, "init")
	inferred := func(ts string) string {
		return statsOp(hostop.SpindriftOp{Op: "pass_start", Pass: 1, Role: "implement"}) +
			`{"type":"system","timestamp":"` + ts + `"}` + "\n" +
			statsResult(ts, 1, 1, 1000, 900, "claude-opus")
	}
	statsWriteLogs(t, root, map[string]string{
		"issue-10.log":             inferred("2026-10-07T09:00:30Z"),
		"issue-20.log.prior-run.1": inferred("2026-10-07T10:00:30Z"),
		"issue-20.log":             inferred("2026-10-07T13:00:30Z"),
		"issue-30.log":             inferred("2026-10-07T14:00:30Z"),
	})
	box := func(at, key, phase, rev string) string {
		b, err := json.Marshal(daemon.Event{V: 1, Time: at, Event: "box", Kind: "dispatch", Key: dispatchkey.Issue(key), Phase: phase, Revision: rev})
		if err != nil {
			t.Fatal(err)
		}
		return string(b) + "\n"
	}
	events := box("2026-10-07T09:00:00Z", "10", "initial", "rev-ten") +
		box("2026-10-07T09:30:00Z", "10", "fix-pass-1", "rev-fix") +
		box("2026-10-07T10:00:00Z", "20", "initial", "rev-twenty")
	gitDir, err := gitOutput(root, "rev-parse", "--absolute-git-dir")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "spindrift-daemon.events"), []byte(events), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestStats_ByRevisionUsesEventsForInferredRecords(t *testing.T) {
	root := statsEventsRoot(t)
	roots := map[string]string{"$ROOT": root}

	text, _ := runStats(t, root, "--include-inferred", "--by", "revision")
	statsGolden(t, "stats-by-revision-events.txt", statsNormalize(text, roots))
	jsonl, _ := runStats(t, root, "--include-inferred", "--by", "revision", "--json")
	statsGolden(t, "stats-by-revision-events.jsonl", statsNormalize(jsonl, roots))

	got := map[string]string{}
	for _, rec := range statsGroupedLines(t, jsonl) {
		got[rec.DispatchKey+"@"+rec.ClaimTime] = rec.Group
	}
	want := map[string]string{
		"10@2026-10-07T09:00:30Z": "rev-ten",
		"20@2026-10-07T10:00:30Z": "rev-twenty",
		"20@2026-10-07T13:00:30Z": "(none)",
		"30@2026-10-07T14:00:30Z": "(none)",
	}
	if !maps.Equal(got, want) {
		t.Errorf("groups = %v, want %v", got, want)
	}
}

func TestStats_ByRevisionAnywhereInTheListUsesEvents(t *testing.T) {
	root := statsEventsRoot(t)
	jsonl, _ := runStats(t, root, "--include-inferred", "--by", "driver,revision", "--json")
	got := map[string]string{}
	gotKeys := map[string][]string{}
	for _, rec := range statsGroupedLines(t, jsonl) {
		at := rec.DispatchKey + "@" + rec.ClaimTime
		got[at], gotKeys[at] = rec.Group, rec.GroupKeys
	}
	wantKeys := map[string][]string{
		"10@2026-10-07T09:00:30Z": {"(none)", "rev-ten"},
		"20@2026-10-07T10:00:30Z": {"(none)", "rev-twenty"},
		"20@2026-10-07T13:00:30Z": {"(none)", "(none)"},
		"30@2026-10-07T14:00:30Z": {"(none)", "(none)"},
	}
	if !maps.EqualFunc(gotKeys, wantKeys, slices.Equal) {
		t.Errorf("group_keys = %q, want %q", gotKeys, wantKeys)
	}
	want := map[string]string{
		"10@2026-10-07T09:00:30Z": "(none), rev-ten",
		"20@2026-10-07T10:00:30Z": "(none), rev-twenty",
		"20@2026-10-07T13:00:30Z": "(none), (none)",
		"30@2026-10-07T14:00:30Z": "(none), (none)",
	}
	if !maps.Equal(got, want) {
		t.Errorf("groups = %v, want %v", got, want)
	}
}

func TestStats_ByTrimsSpacesAroundDimensions(t *testing.T) {
	root := statsStampedRoot(t)
	spaced, _ := runStats(t, root, "--by", " driver , role ")
	tight, _ := runStats(t, root, "--by", "driver,role")
	if spaced != tight {
		t.Errorf("--by with spaces differs from the tight form:\n%s\nwant\n%s", spaced, tight)
	}
}

func TestStats_ByTwoKnobsAreDistinctDimensions(t *testing.T) {
	root := t.TempDir()
	day := time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)
	statsWriteLogs(t, root, map[string]string{
		"issue-1.log": statsStampedLog("1", day,
			hostop.DispatchStart{Knobs: map[string]string{"MERGE_MODE": "auto", "MERGE_METHOD": "squash"}}, nil,
			[2]string{"implement", "claude-opus"}),
		"issue-2.log": statsStampedLog("2", day.Add(time.Hour),
			hostop.DispatchStart{Knobs: map[string]string{"MERGE_MODE": "auto", "MERGE_METHOD": "rebase"}}, nil,
			[2]string{"implement", "claude-opus"}),
		"issue-3.log": statsStampedLog("3", day.Add(2*time.Hour),
			hostop.DispatchStart{Knobs: map[string]string{"MERGE_MODE": "manual", "MERGE_METHOD": "squash"}}, nil,
			[2]string{"implement", "claude-opus"}),
	})
	text, _ := runStats(t, root, "--by", "knob:MERGE_MODE,knob:MERGE_METHOD")
	for _, h := range []string{
		"knob:MERGE_MODE = auto, knob:MERGE_METHOD = squash\n",
		"knob:MERGE_MODE = auto, knob:MERGE_METHOD = rebase\n",
		"knob:MERGE_MODE = manual, knob:MERGE_METHOD = squash\n",
	} {
		if !strings.Contains(text, h) {
			t.Errorf("missing group header %q in:\n%s", h, text)
		}
	}
	if n := strings.Count(text, "Records: "); n != 3 {
		t.Errorf("%d groups, want 3:\n%s", n, text)
	}
}

// Keys containing ", " make the joined "group" label ambiguous; group_keys
// carries the keys unjoined so a consumer can tell the groups apart.
func TestStats_ByJSONGroupKeysDisambiguateCommaKeys(t *testing.T) {
	root := t.TempDir()
	day := time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)
	statsWriteLogs(t, root, map[string]string{
		"issue-1.log": statsStampedLog("1", day,
			hostop.DispatchStart{Knobs: map[string]string{"MERGE_GUARD_PATHS": "a, b", "MERGE_MODE": "auto"}}, nil,
			[2]string{"implement", "claude-opus"}),
		"issue-2.log": statsStampedLog("2", day.Add(time.Hour),
			hostop.DispatchStart{Knobs: map[string]string{"MERGE_GUARD_PATHS": "a", "MERGE_MODE": "b, auto"}}, nil,
			[2]string{"implement", "claude-opus"}),
	})
	jsonl, _ := runStats(t, root, "--by", "knob:MERGE_GUARD_PATHS,knob:MERGE_MODE", "--json")
	groups := map[string]string{}
	keys := map[string][]string{}
	for _, rec := range statsGroupedLines(t, jsonl) {
		groups[rec.DispatchKey], keys[rec.DispatchKey] = rec.Group, rec.GroupKeys
	}
	if groups["1"] != "a, b, auto" || groups["2"] != "a, b, auto" {
		t.Errorf("group labels = %v, want both %q", groups, "a, b, auto")
	}
	if want := []string{"a, b", "auto"}; !slices.Equal(keys["1"], want) {
		t.Errorf("issue 1 group_keys = %q, want %q", keys["1"], want)
	}
	if want := []string{"a", "b, auto"}; !slices.Equal(keys["2"], want) {
		t.Errorf("issue 2 group_keys = %q, want %q", keys["2"], want)
	}
}

func TestStats_ByPassLevelDimensionBeforeRecordLevel(t *testing.T) {
	root := statsStampedRoot(t)
	text, _ := runStats(t, root, "--by", "role,driver")
	for _, h := range []string{"role = implement, driver = claude\n", "role = implement, driver = opencode\n", "role = review, driver = claude\n"} {
		if !strings.Contains(text, h) {
			t.Errorf("missing group header %q in:\n%s", h, text)
		}
	}
}

func TestStats_ByRevisionIgnoresAnEnclosingRepoEvents(t *testing.T) {
	outer := statsEventsRoot(t)
	root := filepath.Join(outer, "nested")
	statsWriteLogs(t, root, map[string]string{
		"issue-10.log": statsOp(hostop.SpindriftOp{Op: "pass_start", Pass: 1, Role: "implement"}) +
			`{"type":"system","timestamp":"2026-10-07T09:00:30Z"}` + "\n" +
			statsResult("2026-10-07T09:00:30Z", 1, 1, 1000, 900, "claude-opus"),
	})
	text, _ := runStats(t, root, "--include-inferred", "--by", "revision")
	if strings.Contains(text, "rev-ten") {
		t.Errorf("a root inside another checkout borrowed its events:\n%s", text)
	}
}

func statsWriteEvents(t *testing.T, root, content string) {
	t.Helper()
	gitDir, err := gitOutput(root, "rev-parse", "--absolute-git-dir")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "spindrift-daemon.events"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStats_EventsRevisionAppearsOnlyUnderByRevision(t *testing.T) {
	root := statsEventsRoot(t)
	for _, args := range [][]string{
		{"--include-inferred", "--json"},
		{"--include-inferred", "--by", "role", "--json"},
		{"--include-inferred", "--by", "model", "--json"},
	} {
		out, _ := runStats(t, root, args...)
		if strings.Contains(out, `"revision"`) || strings.Contains(out, "rev-ten") {
			t.Errorf("stats %v leaked an Events revision:\n%s", args, out)
		}
	}
}

func TestStats_ByRevisionUsesEventsForButlerChoreKey(t *testing.T) {
	root := t.TempDir()
	gitRun(t, root, "init")
	statsWriteLogs(t, root, map[string]string{
		"issue-butler-deps.log": statsOp(hostop.SpindriftOp{Op: "pass_start", Pass: 1, Role: "butler"}) +
			`{"type":"system","timestamp":"2026-10-07T09:00:30Z"}` + "\n" +
			statsResult("2026-10-07T09:00:30Z", 1, 1, 1000, 900, "claude-opus"),
	})
	b, err := json.Marshal(daemon.Event{V: 1, Time: "2026-10-07T09:00:00Z", Event: "box", Kind: "butler", Key: dispatchkey.Chore("deps"), Phase: "initial", Revision: "rev-butler"})
	if err != nil {
		t.Fatal(err)
	}
	statsWriteEvents(t, root, string(b)+"\n")

	out, _ := runStats(t, root, "--include-inferred", "--by", "revision", "--json")
	var rec struct {
		Kind, Group string
		DispatchKey string `json:"dispatch_key"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &rec); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if rec.DispatchKey != "butler-deps" || rec.Group != "rev-butler" {
		t.Errorf("record = %+v, want key butler-deps in group rev-butler", rec)
	}
}

func TestStats_UnreadableEventsWarnsAndContinues(t *testing.T) {
	root := statsEventsRoot(t)
	gitDir, err := gitOutput(root, "rev-parse", "--absolute-git-dir")
	if err != nil {
		t.Fatal(err)
	}
	events := filepath.Join(gitDir, "spindrift-daemon.events")
	if err := os.Remove(events); err != nil {
		t.Fatal(err)
	}
	// A directory where the file belongs makes ReadFile fail with something
	// other than not-exist, whoever runs the test.
	if err := os.Mkdir(events, 0o755); err != nil {
		t.Fatal(err)
	}
	out, stderr := runStats(t, root, "--include-inferred", "--by", "revision")
	if !strings.Contains(stderr, "warning: ") || !strings.Contains(stderr, "spindrift-daemon.events") {
		t.Errorf("stderr = %q, want a warning naming the events file", stderr)
	}
	if !strings.Contains(out, "(none)") || strings.Contains(out, "rev-ten") {
		t.Errorf("stdout = %q, want every inferred Record in (none)", out)
	}
	_, quiet := runStats(t, root, "--include-inferred")
	if strings.Contains(quiet, "spindrift-daemon.events") {
		t.Errorf("stderr without --by revision = %q, want no events warning", quiet)
	}
}

func TestStats_MissingGitWarnsButNonCheckoutStaysQuiet(t *testing.T) {
	root := statsEventsRoot(t)
	plain := t.TempDir()
	statsWriteLogs(t, plain, map[string]string{
		"issue-7.log": `{"type":"system","timestamp":"2026-10-07T12:00:00Z"}` + "\n" +
			statsResult("2026-10-07T12:01:00Z", 0.1, 1, 6000, 5000, "claude-haiku"),
	})
	if _, stderr := runStats(t, plain, "--include-inferred", "--by", "revision"); strings.Contains(stderr, "daemon events") {
		t.Errorf("stderr for a non-checkout root = %q, want no events warning", stderr)
	}

	t.Setenv("PATH", t.TempDir())
	out, stderr := runStats(t, root, "--include-inferred", "--by", "revision")
	if !strings.Contains(stderr, "warning: not reading daemon events") {
		t.Errorf("stderr = %q, want a warning that git could not run", stderr)
	}
	if !strings.Contains(out, "(none)") {
		t.Errorf("stdout = %q, want every inferred Record in (none)", out)
	}
}

// Keeps lib/cli-flags.nix and docs/reference.md hand-written while making a
// dimension added to statsDims without documenting it a test failure.
func TestStats_ByDimensionsDocumented(t *testing.T) {
	var flagDoc string
	for _, f := range cliFlags {
		if f.verb == "stats" && f.flag == "by" {
			flagDoc = f.doc
		}
	}
	if flagDoc == "" {
		t.Fatal("cliFlags has no stats --by row")
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "reference.md"))
	if err != nil {
		t.Fatalf("read docs/reference.md: %v", err)
	}
	_, rest, ok := strings.Cut(string(raw), "**Grouping.**")
	if !ok {
		t.Fatal("docs/reference.md has no **Grouping.** paragraph")
	}
	grouping, _, ok := strings.Cut(rest, "**Re-ingest.**")
	if !ok {
		t.Fatal("docs/reference.md: **Re-ingest.** does not follow **Grouping.**")
	}
	_, afterRoot, ok := strings.Cut(flagDoc, "--root: ")
	if !ok {
		t.Fatal(`by flag doc has no "--root: " anchor before its dimension list`)
	}
	list, _, ok := strings.Cut(afterRoot, ";")
	if !ok {
		t.Fatal(`by flag doc's dimension list has no closing ";"`)
	}
	documented := map[string]bool{}
	for _, item := range strings.Split(list, ", ") {
		documented[strings.TrimPrefix(item, "or ")] = true
	}
	for _, s := range statsDims {
		sp := s.spelling()
		if !documented[sp] {
			t.Errorf("--by dimension %q is missing from the by flag doc: edit lib/cli-flags.nix, then `nix run .#regen`", sp)
		}
		// Backticked so a stray English word ("model") cannot satisfy it.
		if !strings.Contains(grouping, "`"+sp+"`") {
			t.Errorf("--by dimension %q is missing from the Grouping section of docs/reference.md: add a backticked `%s`", sp, sp)
		}
	}
}
