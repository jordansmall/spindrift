package main

import (
	"bytes"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/daemon"
	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/hostpaths"
)

// statsStampedLog is a stamped work Dispatch log: dispatch_start, optional
// prompt_hashes, then one pass per (role, models) entry.
func statsStampedLog(key string, claim time.Time, start claude.DispatchStart, hashes map[string]string, passes ...[2]string) string {
	start.RecordID = dispatchrecord.RecordID("work", key, claim)
	start.Kind, start.DispatchKey, start.ClaimTime, start.Started = "work", key, claim, claim
	out := statsOp(claude.SpindriftOp{Op: "dispatch_start", Start: &start})
	if hashes != nil {
		out += statsOp(claude.SpindriftOp{Op: "prompt_hashes", PromptHashes: &claude.PromptHashes{RecordID: start.RecordID, Roles: hashes}})
	}
	for i, p := range passes {
		ts := claim.Add(time.Duration(i+1) * time.Minute).Format(time.RFC3339)
		models := make([]string, 0, 2)
		for _, m := range strings.Split(p[1], "+") {
			models = append(models, `"`+m+`":{}`)
		}
		out += statsOp(claude.SpindriftOp{Op: "pass_start", Pass: i + 1, Role: p[0]}) +
			`{"type":"system","timestamp":"` + ts + `"}` + "\n" +
			`{"type":"result","result":"","timestamp":"` + ts + `","num_turns":2,"total_cost_usd":1.5,"duration_ms":60000,"duration_api_ms":900,` +
			`"usage":{"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":30,"cache_creation_input_tokens":40},` +
			`"modelUsage":{` + strings.Join(models, ",") + `}}` + "\n"
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
func statsStampedRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	day := time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)
	statsWriteLogs(t, root, map[string]string{
		"issue-70.log": statsStampedLog("70", day,
			claude.DispatchStart{Revision: "rev-a", Knobs: map[string]string{"MERGE_MODE": "auto"}},
			map[string]string{"implement": "hash-i1", "review": "hash-r1"},
			[2]string{"implement", "claude-opus"}, [2]string{"review", "claude-sonnet"}),
		"issue-71.log": statsStampedLog("71", day.Add(time.Hour),
			claude.DispatchStart{Revision: "rev-b", Knobs: map[string]string{"MERGE_MODE": "manual"}},
			map[string]string{"implement": "hash-i2"},
			[2]string{"implement", "claude-opus+claude-haiku"}, [2]string{"review", "claude-sonnet"}),
		"issue-72.log": statsStampedLog("72", day.Add(2*time.Hour), claude.DispatchStart{Knobs: map[string]string{"MERGE_MODE": ""}}, nil,
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
			claude.DispatchStart{Revision: "rev-a", Knobs: map[string]string{"MERGE_MODE": "auto"}},
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
	} {
		t.Run(tc.name, func(t *testing.T) {
			text, _ := runStats(t, root, "--by", tc.by)
			statsGolden(t, "stats-by-"+tc.name+".txt", statsNormalize(text, roots))
			jsonl, _ := runStats(t, root, "--by="+tc.by, "--json")
			statsGolden(t, "stats-by-"+tc.name+".jsonl", statsNormalize(jsonl, roots))
		})
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
	for _, line := range strings.Split(strings.TrimSuffix(jsonl, "\n"), "\n") {
		var rec struct{ Group, Root string }
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatal(err)
		}
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
		{"flavour", `invalid --by "flavour": want role, revision, model, prompt:<role> or knob:<NAME>`},
		{"prompt:nonsense", `unknown prompt role "nonsense", want one of legacy, implement, fix, land, review, delta-review, research, butler`},
		{"prompt:", `unknown prompt role ""`},
		{"knob:NO_SUCH_KNOB", `unknown knob "NO_SUCH_KNOB"`},
		{"knob:GH_TOKEN", "knob GH_TOKEN is secret, never recorded"},
		{"knob:", "knob name is empty"},
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
		return statsOp(claude.SpindriftOp{Op: "pass_start", Pass: 1, Role: "implement"}) +
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
	for _, line := range strings.Split(strings.TrimSuffix(jsonl, "\n"), "\n") {
		var rec struct {
			Group       string `json:"group"`
			DispatchKey string `json:"dispatch_key"`
			ClaimTime   string `json:"claim_time"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatal(err)
		}
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

func TestStats_ByRevisionIgnoresAnEnclosingRepoEvents(t *testing.T) {
	outer := statsEventsRoot(t)
	root := filepath.Join(outer, "nested")
	statsWriteLogs(t, root, map[string]string{
		"issue-10.log": statsOp(claude.SpindriftOp{Op: "pass_start", Pass: 1, Role: "implement"}) +
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
		"issue-butler-deps.log": statsOp(claude.SpindriftOp{Op: "pass_start", Pass: 1, Role: "butler"}) +
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
