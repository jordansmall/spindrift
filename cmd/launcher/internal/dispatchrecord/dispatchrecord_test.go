package dispatchrecord

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/hostop"
	"spindrift.dev/launcher/internal/passmachine"
	"spindrift.dev/launcher/internal/seamtest"
)

func opLine(op hostop.SpindriftOp) string { return hostop.EncodeSpindriftOp(op) }

func assistant(id string) string {
	return seamtest.Line(driverkit.Event{Kind: driverkit.KindUsage, MessageID: id})
}

// result is a Pass's session summary at ts: one result event carrying the
// totals the CLI reports (cost, a fixed token tally, turns, durations) and the
// models it ran.
func result(ts string, cost float64, turns int, dur, apiDur int64, models ...string) string {
	return seamtest.Line(driverkit.Event{Kind: driverkit.KindResult, Timestamp: seamtest.Millis(ts), NumTurns: turns,
		Tokens:     driverkit.Tokens{Input: 10, Output: 20, CacheRead: 30, CacheWrite5m: 40, CostUSD: cost},
		DurationMs: dur, DurationAPIMs: apiDur, Models: models})
}

func writeLog(t *testing.T, name string, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(strings.Join(lines, "")), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseLog(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		file     string
		lines    []string
		wantID   string
		wantKind string
		wantKey  string
		passes   []Pass
	}{
		{
			name: "multi-pass work with review BLOCK then APPROVE",
			file: "issue-42.log",
			lines: []string{
				opLine(hostop.SpindriftOp{Op: "pass_start", Pass: 1, Role: "implement"}),
				seamtest.Line(driverkit.Event{Kind: driverkit.KindStart, Timestamp: seamtest.Millis("2026-10-07T12:34:56.789123Z")}),
				assistant("a"), assistant("a"), assistant("b"),
				result("2026-10-07T12:35:00Z", 1.5, 7, 4000, 3000, "claude-opus", "claude-haiku"),
				opLine(hostop.SpindriftOp{Op: "pass_start", Pass: 2, Role: "review"}),
				assistant("c"),
				result("2026-10-07T12:36:00Z", 0.5, 2, 1000, 900, "claude-sonnet"),
				opLine(hostop.SpindriftOp{Op: "verdict", Pass: 2, Verdict: "BLOCK"}),
				opLine(hostop.SpindriftOp{Op: "pass_start", Pass: 3, Role: "fix"}),
				result("2026-10-07T12:37:00Z", 1, 3, 2000, 1500, "claude-opus"),
				opLine(hostop.SpindriftOp{Op: "pass_start", Pass: 4, Role: "review"}),
				assistant("d"),
				opLine(hostop.SpindriftOp{Op: "pass_usage", Pass: 4, Usage: &hostop.PassUsage{APICalls: 9}}),
				result("2026-10-07T12:38:00Z", 0.25, 1, 500, 400, "claude-sonnet"),
				opLine(hostop.SpindriftOp{Op: "verdict", Pass: 4, Verdict: "APPROVE"}),
			},
			wantID:   "work:42@2026-10-07T12:34:56.789Z",
			wantKind: "work",
			wantKey:  "42",
			passes: []Pass{
				{Ordinal: 1, Role: "implement", Models: []string{"claude-haiku", "claude-opus"}, USD: 1.5,
					InputTokens: 10, OutputTokens: 20, CacheReadInputTokens: 30, CacheCreationInputTokens: 40,
					APICalls: 2, Turns: 7, DurationMs: 4000, APIDurationMs: 3000},
				{Ordinal: 2, Role: "review", Models: []string{"claude-sonnet"}, USD: 0.5,
					InputTokens: 10, OutputTokens: 20, CacheReadInputTokens: 30, CacheCreationInputTokens: 40,
					APICalls: 1, Turns: 2, DurationMs: 1000, APIDurationMs: 900, Verdict: "BLOCK"},
				{Ordinal: 3, Role: "fix", Models: []string{"claude-opus"}, USD: 1,
					InputTokens: 10, OutputTokens: 20, CacheReadInputTokens: 30, CacheCreationInputTokens: 40,
					Turns: 3, DurationMs: 2000, APIDurationMs: 1500},
				{Ordinal: 4, Role: "review", Models: []string{"claude-sonnet"}, USD: 0.25,
					InputTokens: 10, OutputTokens: 20, CacheReadInputTokens: 30, CacheCreationInputTokens: 40,
					APICalls: 9, Turns: 1, DurationMs: 500, APIDurationMs: 400, Verdict: "APPROVE"},
			},
		},
		{
			name: "butler key",
			file: "issue-butler-deps.log",
			lines: []string{
				result("2026-10-07T01:00:00.5Z", 2, 4, 100, 90, "claude-opus"),
			},
			wantID:   "butler:butler-deps@2026-10-07T01:00:00.500Z",
			wantKind: "butler",
			wantKey:  "butler-deps",
			passes: []Pass{
				{Ordinal: 1, Models: []string{"claude-opus"}, USD: 2,
					InputTokens: 10, OutputTokens: 20, CacheReadInputTokens: 30, CacheCreationInputTokens: 40,
					Turns: 4, DurationMs: 100, APIDurationMs: 90},
			},
		},
		{
			name: "unknown kind, legacy log sums several results, claim is the first timestamp not the earliest",
			file: "issue-7.log",
			lines: []string{
				assistant("x"),
				result("2026-10-07T02:00:00Z", 1, 1, 10, 5, "m1"),
				result("2026-10-07T01:59:00Z", 1, 2, 20, 6, "m2"),
			},
			wantID:   "unknown:7@2026-10-07T02:00:00.000Z",
			wantKind: "unknown",
			wantKey:  "7",
			passes: []Pass{
				{Ordinal: 1, Models: []string{"m1", "m2"}, USD: 2,
					InputTokens: 20, OutputTokens: 40, CacheReadInputTokens: 60, CacheCreationInputTokens: 80,
					APICalls: 1, Turns: 3, DurationMs: 30, APIDurationMs: 11},
			},
		},
		{
			name:     "prior-run file key",
			file:     "issue-9.log.prior-run.2",
			lines:    []string{opLine(hostop.SpindriftOp{Op: "pass_start", Pass: 1, Role: "implement"})},
			wantKind: "work",
			wantKey:  "9",
			passes:   []Pass{{Ordinal: 1, Role: "implement", Models: []string{}}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := writeLog(t, tc.file, tc.lines...)
			mtime := time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)
			if err := os.Chtimes(p, mtime, mtime); err != nil {
				t.Fatal(err)
			}
			rec, _, err := ParseLog(p)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantID != "" && rec.ID != tc.wantID {
				t.Errorf("ID = %q, want %q", rec.ID, tc.wantID)
			}
			if rec.Kind != tc.wantKind || rec.DispatchKey != tc.wantKey {
				t.Errorf("kind/key = %q/%q, want %q/%q", rec.Kind, rec.DispatchKey, tc.wantKind, tc.wantKey)
			}
			if rec.Attribution != "inferred" || rec.Outcome != "unknown" {
				t.Errorf("attribution/outcome = %q/%q", rec.Attribution, rec.Outcome)
			}
			// The per-agent breakdown is pinned in agents_test.go.
			for i := range rec.Passes {
				rec.Passes[i].Agents = nil
			}
			for i := range tc.passes {
				tc.passes[i].Log = tc.file
			}
			if !reflect.DeepEqual(rec.Passes, tc.passes) {
				t.Errorf("passes =\n%+v\nwant\n%+v", rec.Passes, tc.passes)
			}
		})
	}
}

func TestParseLogCrashFallsBackToMtime(t *testing.T) {
	t.Parallel()
	p := writeLog(t, "issue-5.log", opLine(hostop.SpindriftOp{Op: "pass_start", Pass: 1, Role: "implement"}))
	mtime := time.Date(2026, 10, 7, 9, 8, 7, 654_000_000, time.FixedZone("x", 3600))
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	rec, provisional, err := ParseLog(p)
	if err != nil {
		t.Fatal(err)
	}
	if want := "work:5@2026-10-07T08:08:07.654Z"; rec.ID != want {
		t.Errorf("ID = %q, want %q", rec.ID, want)
	}
	if !provisional || len(rec.Passes) != 1 {
		t.Errorf("provisional=%v passes=%+v, want provisional with the one pass", provisional, rec.Passes)
	}
	if !rec.ClaimTime.Equal(mtime) || rec.ClaimTime.Location() != time.UTC {
		t.Errorf("ClaimTime = %v", rec.ClaimTime)
	}
}

func TestParseLogDeterministicID(t *testing.T) {
	t.Parallel()
	p := writeLog(t, "issue-3.log", result("2026-10-07T12:00:00.123Z", 1, 1, 1, 1, "m"))
	a, _, err := ParseLog(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	b, _, err := ParseLog(p)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != b.ID || a.ID != "unknown:3@2026-10-07T12:00:00.123Z" {
		t.Errorf("IDs = %q, %q", a.ID, b.ID)
	}
}

func TestParseLogMissingFile(t *testing.T) {
	t.Parallel()
	if _, _, err := ParseLog(filepath.Join(t.TempDir(), "issue-1.log")); err == nil {
		t.Fatal("want error for missing file")
	}
}

func TestParseLogRejectsNonChainName(t *testing.T) {
	t.Parallel()
	if _, _, err := ParseLog(writeLog(t, "issue-1-fix-2.log")); err == nil {
		t.Fatal("want error for fix log name")
	}
}

func TestChainKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		key       string
		satellite bool
		ok        bool
	}{
		{"issue-42.log", "42", false, true},
		{"issue-42.log.prior-run.1", "42", false, true},
		{"issue-42.log.prior-run.12", "42", false, true},
		{"issue-butler-deps.log", "butler-deps", false, true},
		{"issue-42-fix-1.log", "42", true, true},
		{"issue-42-conflict-resolve.log", "42", true, true},
		{"issue-42-fix-1.log.prior-run.1", "42", true, true},
		{"issue-42-fix-2.log.1", "42", true, true},
		{"issue-42-conflict-resolve.log.2.prior-run.1", "42", true, true},
		{"issue-a-fix-1-fix-2.log", "a-fix-1", true, true},
		{"issue-42.log.1", "", false, false},
		{"issue-42.log.prior-run.x", "", false, false},
		{"issue-.log", "", false, false},
		{"other-42.log", "", false, false},
		{"issue-42.txt", "", false, false},
	}
	for _, tc := range tests {
		key, sat, ok := ChainKey(tc.name)
		if key != tc.key || sat != tc.satellite || ok != tc.ok {
			t.Errorf("ChainKey(%q) = %q,%v,%v want %q,%v,%v", tc.name, key, sat, ok, tc.key, tc.satellite, tc.ok)
		}
	}
}

func TestParseLogPassOrdinalsStrictlyIncrease(t *testing.T) {
	t.Parallel()
	p := writeLog(t, "issue-7.log",
		result("2026-10-07T12:00:00Z", 1, 1, 10, 5, "opus"),
		opLine(hostop.SpindriftOp{Op: "pass_start", Pass: 1, Role: "implement"}),
		result("2026-10-07T12:01:00Z", 1, 1, 10, 5, "opus"),
	)
	rec, _, err := ParseLog(p)
	if err != nil {
		t.Fatal(err)
	}
	var got []int
	for _, ps := range rec.Passes {
		got = append(got, ps.Ordinal)
	}
	if want := []int{1, 2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ordinals = %v, want %v", got, want)
	}
}

func TestParseLogTimestampedClaimIsNotProvisional(t *testing.T) {
	t.Parallel()
	p := writeLog(t, "issue-3.log", result("2026-10-07T12:00:00.123Z", 1, 1, 1, 1, "m"))
	_, provisional, err := ParseLog(p)
	if err != nil {
		t.Fatal(err)
	}
	if provisional {
		t.Error("timestamp-derived ID must not be provisional")
	}
}

func TestParseLogEventFreeLog(t *testing.T) {
	t.Parallel()
	for name, lines := range map[string][]string{
		"empty":      nil,
		"whitespace": {" \n", "\n", "\t\n"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := ParseLog(writeLog(t, "issue-5.log", lines...)); !errors.Is(err, ErrEmptyLog) {
				t.Fatalf("err = %v, want ErrEmptyLog", err)
			}
		})
	}
}

func TestChoreKeyedKindFollowsDescriptor(t *testing.T) {
	t.Parallel()
	if got := choreKeyedKind(); got != dispatchkind.Butler.Name {
		t.Fatalf("choreKeyedKind = %q, want %q", got, dispatchkind.Butler.Name)
	}
}

func resultText(ts, text string) string {
	return seamtest.Line(driverkit.Event{Kind: driverkit.KindResult, Timestamp: seamtest.Millis(ts), NumTurns: 1, Text: text})
}

// assistantTool is one tool call event whose tool ID is the message ID.
func assistantTool(id, name string, input map[string]string) string {
	return assistantToolUse(id, id, name, input)
}

func assistantToolUse(msgID, toolID, name string, input map[string]string) string {
	in, _ := json.Marshal(input)
	return seamtest.Line(driverkit.Event{Kind: driverkit.KindToolCall, MessageID: msgID, ToolID: toolID,
		ToolName: name, ToolInput: in, ToolTarget: input["file_path"]})
}

// toolResult answers the tool call with the given ID.
func toolResult(toolID string, isError bool) string {
	return seamtest.Line(driverkit.Event{Kind: driverkit.KindToolResult, ToolID: toolID, IsError: isError})
}

func TestParseLogReviewEvidence(t *testing.T) {
	t.Parallel()
	const ts = "2026-10-07T12:00:00Z"
	write := func(id, path, content string) string {
		return assistantTool(id, "Write", map[string]string{"file_path": path, "content": content}) + toolResult(id, false)
	}
	p := writeLog(t, "issue-9.log",
		opLine(hostop.SpindriftOp{Op: "pass_start", Pass: 1, Role: "implement"}),
		write("i1", "/tmp/dispositions.md", "implement must not keep this"),
		resultText(ts, "implement result"),
		opLine(hostop.SpindriftOp{Op: "pass_start", Pass: 2, Role: "review"}),
		resultText(ts, "first draft"),
		resultText(ts, "VERDICT: BLOCK\nfix the thing"),
		opLine(hostop.SpindriftOp{Op: "pass_start", Pass: 3, Role: "fix"}),
		write("f1", "/tmp/dispositions.md", "early"),
		write("f2", "/tmp/other.md", "ignored"),
		assistantToolUse("f3", "f3a", "Write", map[string]string{"file_path": "/tmp/dispositions.md", "content": "first of shared id"})+toolResult("f3a", false),
		assistantToolUse("f3", "f3b", "Write", map[string]string{"file_path": "/tmp/dispositions.md", "content": "second of shared id"})+toolResult("f3b", false),
		assistantTool("f4", "Read", map[string]string{"file_path": "/tmp/dispositions.md", "content": "not a write"}),
		resultText(ts, "fix result"),
		opLine(hostop.SpindriftOp{Op: "pass_start", Pass: 4, Role: "delta-review"}),
		resultText(ts, "VERDICT: APPROVE"),
	)
	rec, _, err := ParseLog(p)
	if err != nil {
		t.Fatal(err)
	}
	type ev struct{ verdictText, dispositions string }
	var got []ev
	for _, ps := range rec.Passes {
		got = append(got, ev{ps.VerdictText, ps.Dispositions})
	}
	want := []ev{
		{"", ""},
		{"VERDICT: BLOCK\nfix the thing", ""},
		{"", "second of shared id"},
		{"VERDICT: APPROVE", ""},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("evidence = %q, want %q", got, want)
	}
}

func TestParseLogDispositionsNeedASuccessfulWrite(t *testing.T) {
	t.Parallel()
	w := func(id, content string) string {
		return assistantTool(id, "Write", map[string]string{"file_path": passmachine.DispositionsPath, "content": content})
	}
	tests := []struct {
		name  string
		lines []string
		want  string
	}{
		{"rejected write then edit keeps the earlier write", []string{
			w("w1", "round one"), toolResult("w1", false),
			w("w2", "rejected"), toolResult("w2", true),
			assistantTool("e1", "Edit", map[string]string{"file_path": passmachine.DispositionsPath, "new_string": "edited"}), toolResult("e1", false),
		}, "round one"},
		{"rejected write alone records nothing", []string{w("w1", "rejected"), toolResult("w1", true)}, ""},
		{"write with no tool_result is not recorded", []string{w("w1", "unanswered")}, ""},
		{"successful write is recorded", []string{w("w1", "kept"), toolResult("w1", false)}, "kept"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines := append([]string{opLine(hostop.SpindriftOp{Op: "pass_start", Pass: 1, Role: "fix"})}, tt.lines...)
			rec, _, err := ParseLog(writeLog(t, "issue-9.log", append(lines, resultText("2026-10-07T12:00:00Z", "done"))...))
			if err != nil {
				t.Fatal(err)
			}
			if got := rec.Passes[0].Dispositions; got != tt.want {
				t.Fatalf("Dispositions = %q, want %q", got, tt.want)
			}
		})
	}
}

func outcomeLine(status string, extra ...string) string {
	return strings.Join(append([]string{"SPINDRIFT_OUTCOME issue=42 landing=x status=" + status}, extra...), " ") + "\n"
}

func TestParseLogKindFromOutcomeStatus(t *testing.T) {
	t.Parallel()
	start := opLine(hostop.SpindriftOp{Op: "pass_start", Pass: 1, Role: "implement"})
	res := result("2026-10-07T12:35:00Z", 1, 1, 1, 1, "m")
	roleless := opLine(hostop.SpindriftOp{Op: "pass_start", Pass: 1})
	researching := "==> claude researching issue #42\n"
	implementing := "==> claude implementing issue #42 on agent/issue-42\n"
	tests := []struct {
		name     string
		lines    []string
		wantKind string
	}{
		{"blocked research log is research by its announce", []string{researching, res, outcomeLine("blocked")}, "research"},
		{"blocked research log with role-less pass_start", []string{researching, roleless, res, outcomeLine("blocked")}, "research"},
		{"announce alone infers research (no outcome, no pass_start)", []string{researching, res}, "research"},
		{"announce alone infers work (no outcome, no pass_start)", []string{implementing, res}, "work"},
		{"blocked work log is work by its announce", []string{implementing, start, res, outcomeLine("blocked")}, "work"},
		{"pre-#734 research log announces implementing; its status still wins", []string{implementing, res, outcomeLine("recommend")}, "research"},
		{"first announce wins", []string{researching, implementing, res, outcomeLine("blocked")}, "research"},
		{"research status without pass_start", []string{res, outcomeLine("recommend")}, "research"},
		{"research status with pass_start", []string{start, res, outcomeLine("unclear")}, "research"},
		{"research status overrides pass_start role", []string{start, res, outcomeLine("reject")}, "research"},
		{"shared blocked status keeps pass_start work", []string{start, res, outcomeLine("blocked")}, "work"},
		{"pre-#734 fallback: blocked with no announce is work", []string{res, outcomeLine("blocked")}, "work"},
		{"work status without pass_start", []string{res, outcomeLine("ready")}, "work"},
		{"synthetic line ignored", []string{start, res, outcomeLine("recommend", "synthetic=true")}, "work"},
		{"synthetic line does not shadow the driver's", []string{res, outcomeLine("ready"), outcomeLine("reject", "synthetic=true")}, "work"},
		{"last self-report wins", []string{res, outcomeLine("recommend"), outcomeLine("ready")}, "work"},
		{"no pass_start and no status", []string{res}, KindUnknown},
		{"no pass_start and synthetic-only status", []string{res, outcomeLine("ready", "synthetic=true")}, KindUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec, _, err := ParseLog(writeLog(t, "issue-42.log", tc.lines...))
			if err != nil {
				t.Fatal(err)
			}
			if rec.Kind != tc.wantKind {
				t.Errorf("kind = %q, want %q", rec.Kind, tc.wantKind)
			}
		})
	}
}

// A custom RESEARCH_VERDICTS set can reuse a work-unique status; the research
// announce must still win.
func TestParseLogAnnounceBeatsWorkUniqueStatus(t *testing.T) {
	t.Parallel()
	res := result("2026-10-07T12:35:00Z", 1, 1, 1, 1, "m")
	researching := "==> claude researching issue #42\n"
	tests := []struct {
		name  string
		lines []string
	}{
		{"researching announce, ambiguous verdict", []string{researching, res, outcomeLine("ambiguous")}},
		{"researching announce, ready verdict", []string{researching, res, outcomeLine("ready")}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec, _, err := ParseLog(writeLog(t, "issue-42.log", tc.lines...))
			if err != nil {
				t.Fatal(err)
			}
			if rec.Kind != dispatchkind.Research.Name {
				t.Errorf("kind = %q, want %q", rec.Kind, dispatchkind.Research.Name)
			}
		})
	}
}

func TestParseLogChoreKeyBeatsOutcomeStatus(t *testing.T) {
	t.Parallel()
	p := writeLog(t, "issue-"+dispatchkey.ChorePrefix+"x.log", result("2026-10-07T12:35:00Z", 1, 1, 1, 1, "m"), outcomeLine("recommend"))
	rec, _, err := ParseLog(p)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Kind != choreKeyedKind() {
		t.Errorf("kind = %q, want %q", rec.Kind, choreKeyedKind())
	}
}

func TestParseLogPlainTextLogYieldsZeroCostRecord(t *testing.T) {
	t.Parallel()
	p := writeLog(t, "issue-9.log", "box: starting\n", "\n", "error: image pull failed\n")
	mtime := time.Date(2026, 10, 7, 9, 8, 7, 0, time.UTC)
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	rec, provisional, err := ParseLog(p)
	if err != nil {
		t.Fatal(err)
	}
	if !provisional || !rec.ClaimTime.Equal(mtime) || rec.Kind != KindUnknown || len(rec.Passes) != 0 {
		t.Errorf("rec=%+v provisional=%v, want provisional unknown-kind Record at mtime with no passes", rec, provisional)
	}
}

func TestStatusKind(t *testing.T) {
	t.Parallel()
	for status, want := range map[string]string{
		"ready":            dispatchkind.Work.Name,
		"already-resolved": dispatchkind.Work.Name,
		"recommend":        dispatchkind.Research.Name,
		"reject":           dispatchkind.Research.Name,
		"blocked":          KindUnknown, // on both the work and research rows
		"":                 KindUnknown,
		"bogus":            KindUnknown,
	} {
		if got := statusKind(status); got != want {
			t.Errorf("statusKind(%q) = %q, want %q", status, got, want)
		}
	}
}
