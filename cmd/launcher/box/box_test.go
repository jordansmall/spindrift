package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/bundleout"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/outcomebackstop"
	"spindrift.dev/launcher/internal/promptassembly"
	"spindrift.dev/launcher/internal/retry"
	"spindrift.dev/launcher/internal/signalwire"
	"spindrift.dev/launcher/internal/testutil/repopath"
)

const (
	readyLine     = "SPINDRIFT_OUTCOME issue=42 landing=agent/issue-42 status=ready note=done"
	blockedLine   = "SPINDRIFT_OUTCOME issue=42 landing=agent/issue-42 status=blocked note=stuck"
	resolvedLine  = "SPINDRIFT_OUTCOME issue=42 landing=agent/issue-42 status=already-resolved note=dup"
	syntheticLine = "SPINDRIFT_OUTCOME issue=42 landing=agent/issue-42 status=ready note=synthetic"
	nearMissLine  = "SPINDRIFT_OUTCOME I finished the work"
	prIntentLine  = "SPINDRIFT_PR_INTENT n0nce dGl0bGUKCmJvZHk="
	completeLine  = "==> entrypoint complete for 42"
	announceLine  = "==> claude implementing issue #42 on agent/issue-42"
)

type resumeScript struct {
	result   string
	rawLines []string
	rc       int
}

type orchCall struct {
	argv    map[string]string
	prompt  string
	session string
	handoff map[string]json.RawMessage
}

const firstPrompt = "the assembled prompt"

type fixture struct {
	t   *testing.T
	dir string
	in  inputs
	// handoffFile is what the fake Assemble returns: the file the test wrote.
	handoffFile string
	env         promptassembly.Env
	d           deps
	out         bytes.Buffer
	errb        bytes.Buffer

	// first scripts the first Driver run, the orchestrator's first call; the
	// resumes script the calls after it, and calls records only those.
	first     resumeScript
	firstCall orchCall
	resumes   []resumeScript
	calls     []orchCall
	ranFirst  bool

	assembled   int
	assembleErr error

	backstopOut  string
	backstopErr  error
	backstopCfgs []outcomebackstop.Config
	demoteOut    string
	demoteErr    error
	demoteCfgs   []outcomebackstop.Config
	demotePriors []string
	bundleCfgs   []bundleout.Config
	bundleErr    error
	scanDirs     []string
	scanOut      string
	nixCalls     [][]string
	nixErr       error
	nixAtCall    func()
	prefetched   []*exec.Cmd
	prefetchErr  error
	assembledIn  []assemblyInputs
	knobs        map[string]string
}

func resultEvent(text string) string {
	b, _ := json.Marshal(struct {
		Type   string `json:"type"`
		Result string `json:"result"`
	}{"result", text})
	return string(b) + "\n"
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	f := &fixture{t: t, dir: dir, backstopOut: syntheticLine + "\n"}
	f.knobs = map[string]string{
		"MAX_REBASE_ATTEMPTS": "3", "TRANSIENT_BACKOFF_SECS": "2", "HOLD_JITTER_SECS": "1",
		"REPO_SLUG": "owner/repo", "HOME": filepath.Join(dir, "home"),
	}
	f.handoffFile = filepath.Join(dir, "handoff.json")
	f.in = inputs{
		WorkDir:   filepath.Join(dir, "work"),
		OutboxDir: filepath.Join(dir, "outbox"),
	}
	f.in.Assembly.SkillsDir = filepath.Join(dir, "driver-skills")
	if err := os.MkdirAll(f.knobs["HOME"], 0o755); err != nil {
		t.Fatal(err)
	}
	promptFile := filepath.Join(dir, "prompt.md")
	// The shell's assemble-prompt leaves a trailing newline bash's `$(cat)` trimmed.
	f.write(promptFile, firstPrompt+"\n\n")
	f.write(f.handoffFile, `{"Driver":"claude","SessionMode":"initial","PromptFile":`+strconv.Quote(promptFile)+`,"ReviewPromptFile":"/tmp/review.md","Model":"opus","Caps":{"MaxReviewRounds":3}}`)
	f.env = promptassembly.Env{
		DispatchKey:          "42",
		DispatchKeying:       "issue",
		DispatchAnnounceVerb: "implementing",
		IssueNumber:          "42",
		Branch:               "agent/issue-42",
		BaseBranch:           "main",
		RunNonce:             "n0nce",
		BoxWriteEnabled:      true,
	}
	f.firstRun(readyLine+"\n", 0)
	f.d = deps{
		Assemble: func(in assemblyInputs, _ promptassembly.Env, _ io.Writer) (string, error) {
			f.assembled++
			f.assembledIn = append(f.assembledIn, in)
			return f.handoffFile, f.assembleErr
		},
		Nix: func(_ context.Context, _ string, args ...string) error {
			f.nixCalls = append(f.nixCalls, args)
			if f.nixAtCall != nil {
				f.nixAtCall()
			}
			return f.nixErr
		},
		RunCmd: func(cmd *exec.Cmd) error {
			f.prefetched = append(f.prefetched, cmd)
			return f.prefetchErr
		},
		Orchestrate: f.orchestrate,
		Backstop: func(cfg outcomebackstop.Config, w io.Writer) error {
			f.backstopCfgs = append(f.backstopCfgs, cfg)
			_, _ = io.WriteString(w, f.backstopOut)
			return f.backstopErr
		},
		Demote: func(cfg outcomebackstop.Config, prior string, w io.Writer) error {
			f.demoteCfgs = append(f.demoteCfgs, cfg)
			f.demotePriors = append(f.demotePriors, prior)
			_, _ = io.WriteString(w, f.demoteOut)
			return f.demoteErr
		},
		BundleOut: func(cfg bundleout.Config, _ io.Writer) error {
			f.bundleCfgs = append(f.bundleCfgs, cfg)
			return f.bundleErr
		},
		WarnLockfiles: func(w io.Writer, dir string) {
			f.scanDirs = append(f.scanDirs, dir)
			_, _ = io.WriteString(w, f.scanOut)
		},
		Getenv: func(k string) string { return f.knobs[k] },
		Stdout: &f.out,
		Stderr: &f.errb,
	}
	return f
}

func (f *fixture) write(path, content string) {
	f.t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

// firstRun scripts the Driver run box now performs itself: what the first
// orchestrator call writes to its stream log and exits with.
func (f *fixture) firstRun(text string, rc int, rawLines ...string) {
	f.first = resumeScript{result: text, rawLines: rawLines, rc: rc}
}

// transcript makes the pinned session's transcript exist under HOME, so a
// resume's session flags resume it.
func (f *fixture) transcript() {
	f.t.Helper()
	dir := filepath.Join(f.knobs["HOME"], ".claude", "projects", "x")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	f.write(filepath.Join(dir, f.sessionID()+".jsonl"), "")
}

func (f *fixture) sessionID() string {
	d, err := driver.New("claude")
	if err != nil {
		f.t.Fatal(err)
	}
	return strings.TrimPrefix(d.SessionFlags("initial", f.knobs["REPO_SLUG"], f.env.IssueNumber, ""), "--session-id ")
}

func (f *fixture) readOnlyRelay() {
	f.env.BoxWriteEnabled = false
	f.env.OutboxRelayCapable = true
}

func (f *fixture) orchestrate(argv []string) int {
	f.t.Helper()
	call := orchCall{argv: map[string]string{}}
	for i := 0; i+1 < len(argv); i += 2 {
		call.argv[argv[i]] = argv[i+1]
	}
	prompt, err := os.ReadFile(call.argv["--prompt-file"])
	if err != nil {
		f.t.Fatalf("prompt file unreadable during the orchestrator call: %v", err)
	}
	call.prompt = string(prompt)
	session, err := os.ReadFile(call.argv["--session-file"])
	if err != nil {
		f.t.Fatalf("session file unreadable during the orchestrator call: %v", err)
	}
	call.session = string(session)
	raw, err := os.ReadFile(call.argv["--handoff-file"])
	if err != nil {
		f.t.Fatalf("handoff file unreadable during the orchestrator call: %v", err)
	}
	if err := json.Unmarshal(raw, &call.handoff); err != nil {
		f.t.Fatalf("handoff not JSON: %v", err)
	}
	var s resumeScript
	if !f.ranFirst {
		f.ranFirst = true
		f.firstCall = call
		s = f.first
	} else {
		f.calls = append(f.calls, call)
		if len(f.resumes) == 0 {
			f.t.Errorf("unscripted orchestrator call #%d", len(f.calls)+1)
			return 99
		}
		s = f.resumes[0]
		f.resumes = f.resumes[1:]
	}
	f.write(call.argv["--log-path"], resultEvent(s.result)+strings.Join(s.rawLines, "\n")+"\n")
	return s.rc
}

func (f *fixture) run() int {
	f.t.Helper()
	rc, err := run(f.in, f.env, f.d)
	if err != nil {
		f.t.Fatalf("run() error = %v", err)
	}
	return rc
}

func (f *fixture) stdout() string { return f.out.String() }

func (f *fixture) lines() []string {
	return strings.Split(strings.TrimSuffix(f.out.String(), "\n"), "\n")
}

func countLine(lines []string, want string) int {
	n := 0
	for _, l := range lines {
		if l == want {
			n++
		}
	}
	return n
}

func prIntentGateMissing(f *fixture) {
	f.readOnlyRelay()
	f.firstRun(readyLine+"\n", 0)
}

// --- outcome nudge gate (ported from the deleted entrypoint-outcome-recovery.bats, #4292) ---

func TestOutcomeNudge_ResumeSuppliesOutcome_NoBackstop(t *testing.T) {
	f := newFixture(t)
	f.firstRun("all done, no marker\n", 0)
	f.resumes = []resumeScript{{result: readyLine + "\n"}}

	if rc := f.run(); rc != 0 {
		t.Fatalf("rc = %d, want 0", rc)
	}
	if len(f.calls) != 1 {
		t.Fatalf("orchestrator calls = %d, want 1", len(f.calls))
	}
	if len(f.backstopCfgs) != 0 {
		t.Fatalf("backstop ran %d times, want 0", len(f.backstopCfgs))
	}
	want := []string{
		announceLine,
		"==> required marker missing — resuming the session once with a nudge",
		readyLine,
		completeLine,
	}
	if got := f.lines(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestOutcomeNudge_ResumeAlsoMissing_BackstopNotesRecovery(t *testing.T) {
	f := newFixture(t)
	f.firstRun("all done, no marker\n", 0)
	f.resumes = []resumeScript{{result: "still nothing\n"}}

	f.run()

	if len(f.calls) != 1 {
		t.Fatalf("orchestrator calls = %d, want 1 (the nudge is capped)", len(f.calls))
	}
	if len(f.backstopCfgs) != 1 {
		t.Fatalf("backstop ran %d times, want exactly 1", len(f.backstopCfgs))
	}
	if !f.backstopCfgs[0].RecoveryAttempted {
		t.Error("backstop RecoveryAttempted = false, want true after a resume")
	}
	if !strings.Contains(f.stdout(), "==> driver produced no SPINDRIFT_OUTCOME line — emitting synthetic backstop\n"+syntheticLine+"\n") {
		t.Errorf("stdout lacks the backstop banner + line:\n%s", f.stdout())
	}
}

func TestOutcomeNudge_ResumeTargetsPinnedSessionWithStrippedHandoff(t *testing.T) {
	f := newFixture(t)
	f.transcript()
	f.firstRun("no marker\n", 0)
	f.resumes = []resumeScript{{result: readyLine + "\n"}}

	f.run()

	call := f.calls[0]
	if want := "--resume " + f.sessionID(); call.session != want {
		t.Errorf("session file = %q, want %q", call.session, want)
	}
	if got := call.argv["--handoff-file"]; got == f.handoffFile {
		t.Error("resume reused the shared handoff file, want its own stripped copy")
	}
	if got := string(call.handoff["ReviewPromptFile"]); got != `""` {
		t.Errorf("ReviewPromptFile = %s, want cleared", got)
	}
	for _, k := range []string{"Driver", "SessionMode", "Model", "Caps"} {
		if _, ok := call.handoff[k]; !ok {
			t.Errorf("stripped handoff dropped field %s", k)
		}
	}
	shared, _ := os.ReadFile(f.handoffFile)
	if !strings.Contains(string(shared), "/tmp/review.md") {
		t.Error("the shared handoff file was modified")
	}
	if !strings.Contains(call.prompt, "SPINDRIFT_OUTCOME") {
		t.Errorf("nudge prompt does not restate the marker:\n%s", call.prompt)
	}
	if strings.HasSuffix(call.prompt, "\n") {
		t.Error("nudge prompt kept a trailing newline bash's $(...) would have trimmed")
	}
}

func TestOutcomeNudge_NonZeroExitNeverResumesNorBackstops(t *testing.T) {
	f := newFixture(t)
	f.firstRun("crashed\n", 3)

	if rc := f.run(); rc != 3 {
		t.Fatalf("rc = %d, want the driver's 3", rc)
	}
	if len(f.calls) != 0 || len(f.backstopCfgs) != 0 {
		t.Fatalf("calls=%d backstops=%d, want neither on a non-zero exit", len(f.calls), len(f.backstopCfgs))
	}
	if strings.Contains(f.stdout(), "SPINDRIFT_OUTCOME") {
		t.Errorf("synthetic line emitted on a crash:\n%s", f.stdout())
	}
}

func TestOutcomeNudge_NearMissPromptQuotesOffendingLine(t *testing.T) {
	f := newFixture(t)
	f.firstRun(nearMissLine+"\n", 0)
	f.resumes = []resumeScript{{result: readyLine + "\n"}}

	f.run()

	if len(f.calls) != 1 || !strings.Contains(f.calls[0].prompt, nearMissLine) {
		t.Fatalf("nudge prompt does not quote the near-miss line: %+v", f.calls)
	}
}

func TestOutcomeNudge_NearMissOnEveryCall_OneResumeThenBackstop(t *testing.T) {
	f := newFixture(t)
	f.firstRun(nearMissLine+"\n", 0)
	f.resumes = []resumeScript{{result: nearMissLine + "\n"}}

	f.run()

	if len(f.calls) != 1 {
		t.Fatalf("orchestrator calls = %d, want 1", len(f.calls))
	}
	if len(f.backstopCfgs) != 1 || !f.backstopCfgs[0].RecoveryAttempted {
		t.Fatalf("backstop cfgs = %+v, want one with RecoveryAttempted", f.backstopCfgs)
	}
}

func TestOutcomeNudge_ResumeExitCodeReplacesOnlyWhenNonZero(t *testing.T) {
	f := newFixture(t)
	f.firstRun("no marker\n", 0)
	f.resumes = []resumeScript{{result: readyLine + "\n", rc: 7}}
	if rc := f.run(); rc != 7 {
		t.Fatalf("rc = %d, want the resume's 7", rc)
	}
	if len(f.backstopCfgs) != 0 {
		t.Error("backstop ran for a resume that produced an outcome")
	}
}

func TestOutcomeNudge_SkippedForAdviseOnlyKind(t *testing.T) {
	f := newFixture(t)
	f.env.DispatchKind = "research"
	f.firstRun("verdict, no marker\n", 0)

	f.run()

	if len(f.calls) != 0 {
		t.Fatalf("advise-only kind resumed %d times, want 0", len(f.calls))
	}
}

// --- outcome backstop (ported from the deleted entrypoint-outcome-backstop.bats, #4292) ---

func TestBackstop_NoOutcomeLine_EmitsSyntheticLineOnce(t *testing.T) {
	f := newFixture(t)
	f.firstRun("done\n", 0)
	f.resumes = []resumeScript{{result: "done\n"}}

	f.run()

	if got := countLine(f.lines(), syntheticLine); got != 1 {
		t.Fatalf("synthetic outcome line printed %d times, want 1:\n%s", got, f.stdout())
	}
}

func TestBackstop_TrimsTrailingNewlinesAndReprintsWithOne(t *testing.T) {
	f := newFixture(t)
	f.firstRun("done\n", 0)
	f.resumes = []resumeScript{{result: "done\n"}}
	f.backstopOut = syntheticLine + "\n\n\n"

	f.run()

	if !strings.Contains(f.stdout(), syntheticLine+"\n"+"==> entrypoint complete") {
		t.Errorf("backstop output not trimmed to one newline:\n%q", f.stdout())
	}
}

func TestBackstop_OwnOutcomePassesThroughWithoutSyntheticLine(t *testing.T) {
	f := newFixture(t)
	f.firstRun(blockedLine+"\n", 0)

	f.run()

	if len(f.backstopCfgs) != 0 {
		t.Fatalf("backstop ran for a driver that reported its own outcome")
	}
}

func TestBackstop_ConfigMapping(t *testing.T) {
	f := newFixture(t)
	f.env.DispatchKind = ""
	f.env.HostMediatedRemote = true
	f.env.OutboxRelayCapable = true
	f.env.BoxWriteEnabled = true
	f.firstRun("done\n", 0)
	f.resumes = []resumeScript{{result: "done\n"}}

	f.run()

	got := f.backstopCfgs[0]
	if got.Clock.Sleep == nil {
		t.Error("backstop Clock unset, want retry.RealClock()")
	}
	got.Clock = retry.Clock{}
	want := outcomebackstop.Config{
		Repo:               f.in.WorkDir,
		Issue:              "42",
		Branch:             "agent/issue-42",
		Base:               "origin/main",
		Kind:               "work",
		HostMediatedRemote: true,
		OutboxRelayCapable: true,
		WriteEnabled:       true,
		RecoveryAttempted:  true,
		MaxAttempts:        3,
		Backoff:            2e9,
		Jitter:             1e9,
		RunStateFilePath:   "/tmp/run-state.json",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("backstop config = %+v, want %+v", got, want)
	}
}

func TestBackstop_NegativeBackoffAndJitterPassThrough(t *testing.T) {
	f := newFixture(t)
	f.knobs["TRANSIENT_BACKOFF_SECS"] = "-5"
	f.knobs["HOLD_JITTER_SECS"] = "-1"
	f.firstRun("done\n", 0)
	f.resumes = []resumeScript{{result: "done\n"}}

	f.run()

	if len(f.backstopCfgs) != 1 || f.backstopCfgs[0].Backoff >= 0 || f.backstopCfgs[0].Jitter >= 0 {
		t.Fatalf("cfgs = %+v, want negatives handed to the backstop, which clamps them (see outcomebackstop tests)", f.backstopCfgs)
	}
}

func TestBackstop_MalformedKnobIsFatalOnlyWhenTheVerbRuns(t *testing.T) {
	for _, knob := range []string{"MAX_REBASE_ATTEMPTS", "TRANSIENT_BACKOFF_SECS", "HOLD_JITTER_SECS"} {
		for _, bad := range []string{"", "abc"} {
			f := newFixture(t)
			f.knobs[knob] = bad
			f.firstRun("done\n", 0)
			f.resumes = []resumeScript{{result: "done\n"}}
			_, err := run(f.in, f.env, f.d)
			if err == nil || !strings.Contains(err.Error(), knob) {
				t.Errorf("%s=%q: error = %v, want one naming the knob", knob, bad, err)
			}
		}
	}

	// Advise-only: neither backstop nor demotion reads the knobs.
	f := newFixture(t)
	f.knobs = map[string]string{"HOME": f.knobs["HOME"]}
	f.env.DispatchKind = "research"
	f.firstRun(blockedLine+"\n", 0)
	if _, err := run(f.in, f.env, f.d); err != nil {
		t.Fatalf("advise-only run read an operator knob: %v", err)
	}
}

func TestBackstop_ErrorIsFatalAndNamesPhase(t *testing.T) {
	f := newFixture(t)
	f.firstRun("done\n", 0)
	f.resumes = []resumeScript{{result: "done\n"}}
	f.backstopErr = errors.New("push exploded")

	_, err := run(f.in, f.env, f.d)

	if err == nil || err.Error() != "outcome-backstop: push exploded" {
		t.Fatalf("error = %v, want %q", err, "outcome-backstop: push exploded")
	}
	if strings.Contains(f.stdout(), completeLine) {
		t.Error("run continued past a fatal backstop error")
	}
}

func TestBackstop_AdviseOnlyKindReachesTheBackstopWithItsKind(t *testing.T) {
	f := newFixture(t)
	f.env.DispatchKind = "research"
	f.firstRun("verdict\n", 0)

	f.run()

	if len(f.backstopCfgs) != 1 || f.backstopCfgs[0].Kind != "research" {
		t.Fatalf("backstop cfgs = %+v, want one for kind research", f.backstopCfgs)
	}
	if len(f.calls) != 0 {
		t.Error("an advise-only kind was resumed")
	}
}

// --- already-resolved demotion: bats outcome-backstop #4016 cases ---

func TestDemotion_ReplacesOutcomeLineWithVerbOutput(t *testing.T) {
	f := newFixture(t)
	f.firstRun(resolvedLine+"\n", 0)
	f.demoteOut = blockedLine + "\n"

	f.run()

	if len(f.demotePriors) != 1 || f.demotePriors[0] != resolvedLine {
		t.Fatalf("demote priors = %q, want [%q]", f.demotePriors, resolvedLine)
	}
	if f.demoteCfgs[0].RecoveryAttempted {
		t.Error("demotion config carried RecoveryAttempted")
	}
	if !strings.Contains(f.stdout(), blockedLine+"\n"+completeLine) {
		t.Errorf("demoted line not printed before completion:\n%s", f.stdout())
	}
}

func TestDemotion_EmptyOutputLeavesClaimUntouched(t *testing.T) {
	f := newFixture(t)
	f.firstRun(resolvedLine+"\n", 0)

	f.run()

	if strings.Contains(f.stdout(), "status=blocked") {
		t.Errorf("claim was altered:\n%s", f.stdout())
	}
	if got, want := f.stdout(), announceLine+"\n"+resolvedLine+"\n"+completeLine+"\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

func TestDemotion_OutputFeedsBundleOutPriorLine(t *testing.T) {
	f := newFixture(t)
	f.readOnlyRelay()
	f.firstRun(resolvedLine+"\n", 0)
	f.demoteOut = blockedLine + "\n"

	f.run()

	if len(f.bundleCfgs) != 1 || f.bundleCfgs[0].PriorOutcomeLine != blockedLine {
		t.Fatalf("bundle cfgs = %+v, want PriorOutcomeLine %q", f.bundleCfgs, blockedLine)
	}
}

func TestDemotion_SkippedForAdviseOnlyAndForNoOutcome(t *testing.T) {
	f := newFixture(t)
	f.env.DispatchKind = "research"
	f.firstRun(resolvedLine+"\n", 0)
	f.run()
	if len(f.demoteCfgs) != 0 {
		t.Error("advise-only kind ran the demotion")
	}
}

func TestDemotion_RunsEvenAfterANonZeroDriverExit(t *testing.T) {
	f := newFixture(t)
	f.firstRun(resolvedLine+"\n", 5)
	if rc := f.run(); rc != 5 {
		t.Fatalf("rc = %d, want 5", rc)
	}
	if len(f.demoteCfgs) != 1 {
		t.Fatalf("demotion ran %d times, want 1 (unguarded by the exit code)", len(f.demoteCfgs))
	}
}

func TestDemotion_ErrorIsFatal(t *testing.T) {
	f := newFixture(t)
	f.firstRun(resolvedLine+"\n", 0)
	f.demoteErr = errors.New("boom")
	_, err := run(f.in, f.env, f.d)
	if err == nil || err.Error() != "outcome-demotion: boom" {
		t.Fatalf("error = %v", err)
	}
}

// --- PR-intent nudge gate (ported from the deleted entrypoint-pr-intent-nudge.bats, #4292) ---

func TestPRIntent_GenuineMarkerPresent_NoResume(t *testing.T) {
	f := newFixture(t)
	prIntentGateMissing(f)
	f.firstRun(readyLine+"\n", 0, prIntentLine)

	f.run()

	if len(f.calls) != 0 {
		t.Fatalf("resumed %d times with the PR-intent marker present", len(f.calls))
	}
}

func TestPRIntent_Missing_ResumeSuppliesIt(t *testing.T) {
	f := newFixture(t)
	f.transcript()
	prIntentGateMissing(f)
	f.resumes = []resumeScript{{result: readyLine + "\n", rawLines: []string{prIntentLine}}}

	f.run()

	if len(f.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(f.calls))
	}
	out := f.stdout()
	if !strings.Contains(out, "==> PR-intent marker missing — resuming the session once with a nudge\n") {
		t.Errorf("banner missing:\n%s", out)
	}
	if strings.Contains(out, "spindrift_op") {
		t.Errorf("give-up op emitted although the resume supplied the marker:\n%s", out)
	}
	if call := f.calls[0]; call.session != "--resume "+f.sessionID() || string(call.handoff["ReviewPromptFile"]) != `""` {
		t.Errorf("resume not narrowed: %+v", call)
	}
	if !strings.Contains(f.calls[0].prompt, readyLine) {
		t.Errorf("nudge prompt does not carry the original ready line:\n%s", f.calls[0].prompt)
	}
}

func TestPRIntent_SecondMiss_FallsThroughNoLoop(t *testing.T) {
	f := newFixture(t)
	prIntentGateMissing(f)
	f.resumes = []resumeScript{{result: readyLine + "\n"}, {result: readyLine + "\n"}}

	if rc := f.run(); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if len(f.calls) != 1 {
		t.Fatalf("calls = %d, want the nudge capped at %d", len(f.calls), prIntentNudgeCap)
	}
	if !strings.HasSuffix(f.stdout(), completeLine+"\n") {
		t.Errorf("run did not complete:\n%s", f.stdout())
	}
}

func TestPRIntent_ExhaustedNudgeEmitsGiveUpOp(t *testing.T) {
	f := newFixture(t)
	prIntentGateMissing(f)
	f.resumes = []resumeScript{{result: readyLine + "\n"}}

	f.run()

	want := `"reason":"read-only PR-intent nudge exhausted after 1 attempt; no marker line, handing off blocked"`
	if !strings.Contains(f.stdout(), want) {
		t.Errorf("give-up op missing:\n%s", f.stdout())
	}
	if strings.Contains(f.stdout(), "\n\n") {
		t.Errorf("op line printed with a doubled newline:\n%q", f.stdout())
	}
}

func TestPRIntent_FiresOnReadyReachedOnlyViaBackstop(t *testing.T) {
	f := newFixture(t)
	f.readOnlyRelay()
	f.firstRun("done\n", 0)
	f.resumes = []resumeScript{{result: "done\n"}, {result: syntheticLine + "\n", rawLines: []string{prIntentLine}}}

	f.run()

	if len(f.calls) != 2 {
		t.Fatalf("calls = %d, want the outcome nudge then the PR-intent nudge", len(f.calls))
	}
	if !strings.Contains(f.calls[1].prompt, syntheticLine) {
		t.Errorf("PR-intent nudge did not carry the backstop line:\n%s", f.calls[1].prompt)
	}
	if strings.Contains(f.stdout(), "spindrift_op") {
		t.Errorf("give-up op emitted although the resumed pass supplied PR-intent:\n%s", f.stdout())
	}
}

func TestPRIntent_CrashedResumeAfterBackstopReadyStaysTerminal(t *testing.T) {
	f := newFixture(t)
	f.readOnlyRelay()
	f.firstRun("done\n", 0)
	f.resumes = []resumeScript{{result: "done\n"}, {result: "", rc: 9}}

	if rc := f.run(); rc != 0 {
		t.Fatalf("rc = %d, want 0 (backstop-declared ready stays terminal)", rc)
	}
	want := "==> PR-intent nudge resume failed (rc=9) after a backstop-declared ready outcome — staying terminal (issue #593, #2448)\n"
	if !strings.Contains(f.stdout(), want) {
		t.Errorf("force-exit-zero banner missing:\n%s", f.stdout())
	}
}

func TestPRIntent_CrashedResumeOnGenuineReadyKeepsItsExitCode(t *testing.T) {
	f := newFixture(t)
	prIntentGateMissing(f)
	f.resumes = []resumeScript{{result: "", rc: 9}}

	if rc := f.run(); rc != 9 {
		t.Fatalf("rc = %d, want the resume's 9", rc)
	}
}

func TestPRIntent_ResumedBlockedVerdictNeverClobberedToReady(t *testing.T) {
	f := newFixture(t)
	prIntentGateMissing(f)
	f.readOnlyRelay()
	f.resumes = []resumeScript{{result: blockedLine + "\n"}}

	f.run()

	if strings.Contains(f.stdout(), "restoring it") {
		t.Errorf("restore fired over a genuine resumed verdict:\n%s", f.stdout())
	}
	if countLine(f.lines(), readyLine) != 1 {
		t.Errorf("ready line not printed exactly once (the first run's):\n%s", f.stdout())
	}
}

func TestPRIntent_ShadowedByNearMiss_RestoresOriginalLine(t *testing.T) {
	f := newFixture(t)
	prIntentGateMissing(f)
	f.resumes = []resumeScript{{result: nearMissLine + "\n"}}

	f.run()

	out := f.stdout()
	want := "==> resumed pass did not repeat the original SPINDRIFT_OUTCOME line — restoring it\n" + readyLine + "\n"
	if !strings.Contains(out, want) {
		t.Errorf("restore missing:\n%s", out)
	}
	if got := countLine(f.lines(), readyLine); got != 2 {
		t.Errorf("ready line printed %d times, want the first run's print plus the restore", got)
	}
}

func TestPRIntent_NeverFires(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *fixture)
	}{
		{"read-write run", func(f *fixture) { f.env.BoxWriteEnabled = true; f.env.OutboxRelayCapable = true }},
		{"CODE_FORGE=git (not relay capable)", func(f *fixture) { f.env.BoxWriteEnabled = false; f.env.OutboxRelayCapable = false }},
		{"CODE_FORGE=local (host mediated, not relay capable)", func(f *fixture) {
			f.env.BoxWriteEnabled = false
			f.env.OutboxRelayCapable = false
			f.env.HostMediatedRemote = true
		}},
		{"advise-only research", func(f *fixture) { f.readOnlyRelay(); f.env.DispatchKind = "research" }},
	}
	for _, c := range cases {
		for _, viaBackstop := range []bool{false, true} {
			name := c.name
			if viaBackstop {
				name += "/via-backstop"
			}
			t.Run(name, func(t *testing.T) {
				f := newFixture(t)
				c.setup(f)
				if viaBackstop {
					f.firstRun("done\n", 0)
					if f.env.DispatchKind != "research" {
						f.resumes = []resumeScript{{result: "done\n"}}
					}
				} else {
					f.firstRun(readyLine+"\n", 0)
				}
				f.run()
				for _, call := range f.calls {
					if strings.Contains(call.prompt, "SPINDRIFT_PR_INTENT") {
						t.Fatalf("PR-intent nudge fired:\n%s", call.prompt)
					}
				}
				if strings.Contains(f.stdout(), "PR-intent marker missing") {
					t.Fatalf("PR-intent banner printed:\n%s", f.stdout())
				}
			})
		}
	}
}

func TestPRIntent_NeverFiresOnBlockedRun(t *testing.T) {
	for _, viaBackstop := range []bool{false, true} {
		f := newFixture(t)
		f.readOnlyRelay()
		if viaBackstop {
			f.backstopOut = blockedLine + "\n"
			f.firstRun("done\n", 0)
			f.resumes = []resumeScript{{result: "done\n"}}
		} else {
			f.firstRun(blockedLine+"\n", 0)
		}
		f.run()
		if strings.Contains(f.stdout(), "PR-intent marker missing") {
			t.Fatalf("viaBackstop=%v: PR-intent banner on a blocked run:\n%s", viaBackstop, f.stdout())
		}
	}
}

func TestPRIntent_NonZeroExitSkipsTheGate(t *testing.T) {
	f := newFixture(t)
	prIntentGateMissing(f)
	f.firstRun(readyLine+"\n", 4)
	if rc := f.run(); rc != 4 || len(f.calls) != 0 {
		t.Fatalf("rc=%d calls=%d, want 4 and no resume", rc, len(f.calls))
	}
}

func TestPRIntent_SocketCarrierQueriesSignalStatus(t *testing.T) {
	f := newFixture(t)
	prIntentGateMissing(f)
	f.env.SignalCarrier = "socket"
	queried := 0
	f.d.SignalStatus = func() (signalwire.Status, error) {
		queried++
		return signalwire.Status{PRIntent: &signalwire.Receipt{}}, nil
	}
	// The log carries no marker, yet the socket does: no nudge.
	f.run()
	if queried == 0 {
		t.Fatal("signal status never queried under the socket carrier")
	}
	if len(f.calls) != 0 {
		t.Fatalf("resumed %d times although the socket reported a PR intent", len(f.calls))
	}
}

func TestPRIntent_SocketGiveUpNamesTheCarrier(t *testing.T) {
	f := newFixture(t)
	prIntentGateMissing(f)
	f.env.SignalCarrier = "socket"
	f.d.SignalStatus = func() (signalwire.Status, error) { return signalwire.Status{}, nil }
	f.resumes = []resumeScript{{result: readyLine + "\n"}}

	f.run()

	if !strings.Contains(f.stdout(), "no PR intent on the signal socket status route") {
		t.Errorf("socket give-up reason missing:\n%s", f.stdout())
	}
}

func TestPRIntent_SignalStatusErrorWarnsAndStillNudges(t *testing.T) {
	f := newFixture(t)
	prIntentGateMissing(f)
	f.env.SignalCarrier = "socket"
	f.d.SignalStatus = func() (signalwire.Status, error) { return signalwire.Status{}, errors.New("socket down") }
	f.resumes = []resumeScript{{result: readyLine + "\n"}}

	f.run()

	if len(f.calls) != 1 {
		t.Fatalf("calls = %d, want the fail-safe nudge", len(f.calls))
	}
	if !strings.Contains(f.errb.String(), "socket down") {
		t.Errorf("scan error not surfaced on stderr: %q", f.errb.String())
	}
}

// --- settle phases and sequencing ---

func TestSettle_LockfileScanRunsUnlessSelfContained(t *testing.T) {
	f := newFixture(t)
	f.firstRun(blockedLine+"\n", 6)
	f.run()
	if len(f.scanDirs) != 1 || f.scanDirs[0] != f.in.WorkDir {
		t.Fatalf("scan dirs = %v, want one scan of the work dir even after a crash", f.scanDirs)
	}

	g := newFixture(t)
	g.env.SelfContained = true
	g.run()
	if len(g.scanDirs) != 0 {
		t.Fatalf("self-contained dispatch scanned %v", g.scanDirs)
	}
}

func TestSettle_BundleOutPredicate(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *fixture)
		want  int
	}{
		{"read-write github", func(f *fixture) {}, 0},
		{"host mediated (local)", func(f *fixture) { f.env.HostMediatedRemote = true }, 1},
		{"read-only relay-capable", func(f *fixture) { f.readOnlyRelay() }, 1},
		{"read-only not relay-capable", func(f *fixture) { f.env.BoxWriteEnabled = false }, 0},
		{"advise-only skips even when needed", func(f *fixture) { f.readOnlyRelay(); f.env.DispatchKind = "research" }, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			c.setup(f)
			f.firstRun(blockedLine+"\n", 0)
			f.run()
			if len(f.bundleCfgs) != c.want {
				t.Fatalf("bundle-out ran %d times, want %d", len(f.bundleCfgs), c.want)
			}
		})
	}
}

func TestSettle_BundleOutConfigMapping(t *testing.T) {
	f := newFixture(t)
	f.readOnlyRelay()
	f.firstRun(blockedLine+"\n", 0)

	f.run()

	want := bundleout.Config{
		Repo:             f.in.WorkDir,
		Base:             "origin/main",
		Branch:           "agent/issue-42",
		OutboxDir:        f.in.OutboxDir,
		Issue:            "42",
		PriorOutcomeLine: blockedLine,
	}
	if len(f.bundleCfgs) != 1 || f.bundleCfgs[0] != want {
		t.Fatalf("bundle cfgs = %+v, want [%+v]", f.bundleCfgs, want)
	}
}

func TestSettle_BundleOutRelaysTheBackstopLine(t *testing.T) {
	f := newFixture(t)
	f.env.BoxWriteEnabled = false
	f.env.OutboxRelayCapable = true
	f.firstRun("done\n", 0)
	f.backstopOut = blockedLine + "\n"
	f.resumes = []resumeScript{{result: "done\n"}}

	f.run()

	if len(f.bundleCfgs) != 1 || f.bundleCfgs[0].PriorOutcomeLine != blockedLine {
		t.Fatalf("bundle cfgs = %+v, want the backstop line as the prior outcome", f.bundleCfgs)
	}
}

func TestSettle_BundleOutFailureIsFatal(t *testing.T) {
	f := newFixture(t)
	f.env.HostMediatedRemote = true
	f.bundleErr = errors.New("no bundle")

	_, err := run(f.in, f.env, f.d)

	if err == nil || err.Error() != "bundle-out: no bundle" {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(f.stdout(), completeLine) {
		t.Error("completion line printed after a fatal bundle-out")
	}
}

func TestSettle_WarnLockfilesOutputReachesStdoutBeforeCompletion(t *testing.T) {
	f := newFixture(t)
	f.scanOut = "==> WARNING: lockfile\n"
	f.run()
	got := f.lines()
	if got[len(got)-2] != "==> WARNING: lockfile" || got[len(got)-1] != completeLine {
		t.Fatalf("stdout tail = %q", got)
	}
}

func TestOrchestratorArgv_ManifestPathOnlyWhenOutboxMounted(t *testing.T) {
	for _, needs := range []bool{false, true} {
		f := newFixture(t)
		f.env.HostMediatedRemote = needs
		f.firstRun("no marker\n", 0)
		f.resumes = []resumeScript{{result: readyLine + "\n"}}
		f.run()
		got, ok := f.calls[0].argv["--manifest-path"]
		if ok != needs {
			t.Fatalf("needsOutbox=%v: --manifest-path present = %v", needs, ok)
		}
		if needs && got != f.in.OutboxDir+"/manifest.json" {
			t.Errorf("--manifest-path = %q", got)
		}
	}
}

func TestResume_RefreshesOutcomeLineAndLogsEvenToEmpty(t *testing.T) {
	f := newFixture(t)
	f.firstRun(nearMissLine+"\n", 0)
	f.resumes = []resumeScript{{result: "nothing useful\n"}}
	f.backstopOut = ""

	f.run()

	// The backstop ran (resume's outcome line was empty, not the stale first
	// one), and the PR-intent/demotion logic saw the refreshed state.
	if len(f.backstopCfgs) != 1 {
		t.Fatalf("backstop ran %d times, want 1", len(f.backstopCfgs))
	}
}

func TestResume_PrintsResumedOutcomeLineWhenNonEmpty(t *testing.T) {
	f := newFixture(t)
	f.firstRun("no marker\n", 0)
	f.resumes = []resumeScript{{result: "**" + readyLine + "**\n"}}

	f.run()

	if countLine(f.lines(), readyLine) != 1 {
		t.Errorf("resumed line (markdown-unwrapped) not printed once:\n%s", f.stdout())
	}
}

func TestResume_TempFilesAreCleanedExceptLogsAndHandoffCopy(t *testing.T) {
	f := newFixture(t)
	f.firstRun("no marker\n", 0)
	f.resumes = []resumeScript{{result: readyLine + "\n"}}
	f.run()
	if _, err := os.Stat(f.calls[0].argv["--prompt-file"]); !os.IsNotExist(err) {
		t.Errorf("prompt temp file survived the resume: %v", err)
	}
	if _, err := os.Stat(f.calls[0].argv["--log-path"]); err != nil {
		t.Errorf("stream log must survive for later scans: %v", err)
	}
	if _, err := os.Stat(f.calls[0].argv["--handoff-file"]); err != nil {
		t.Errorf("stripped handoff must be left on disk: %v", err)
	}
}

func TestResume_UnreadableHandoffIsFatal(t *testing.T) {
	f := newFixture(t)
	f.firstRun("no marker\n", 0)
	// The first run reads the handoff in place; losing it afterwards fails the
	// resume's own copy.
	f.d.Orchestrate = func(argv []string) int {
		rc := f.orchestrate(argv)
		os.Remove(f.handoffFile)
		return rc
	}
	_, err := run(f.in, f.env, f.d)
	if err == nil || !strings.HasPrefix(err.Error(), "resume handoff: ") {
		t.Fatalf("error = %v", err)
	}
}

func TestFirstRun_UnknownDriverIsFatal(t *testing.T) {
	f := newFixture(t)
	f.write(f.handoffFile, `{"Driver":"nope","PromptFile":"`+filepath.Join(f.dir, "prompt.md")+`"}`)
	_, err := run(f.in, f.env, f.d)
	if err == nil || !strings.HasPrefix(err.Error(), "driver: ") {
		t.Fatalf("error = %v", err)
	}
}

func TestRun_UnrecognizedDispatchKindIsFatal(t *testing.T) {
	f := newFixture(t)
	f.env.DispatchKind = "bogus"
	_, err := run(f.in, f.env, f.d)
	if err == nil || !strings.Contains(err.Error(), `unrecognized DISPATCH_KIND=bogus`) {
		t.Fatalf("error = %v", err)
	}
}

func TestRun_StdoutOrderForACleanRun(t *testing.T) {
	f := newFixture(t)
	if rc := f.run(); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	want := []string{announceLine, readyLine, completeLine}
	if got := f.lines(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

// --- the first Driver run ---

func TestFirstRun_PassesTheSharedHandoffAndPromptFileContent(t *testing.T) {
	f := newFixture(t)
	f.run()

	call := f.firstCall
	if got := call.argv["--handoff-file"]; got != f.handoffFile {
		t.Errorf("--handoff-file = %q, want the shared handoff %q as-is", got, f.handoffFile)
	}
	if got := string(call.handoff["ReviewPromptFile"]); got != `"/tmp/review.md"` {
		t.Errorf("ReviewPromptFile = %s, want the first run to keep the review loop", got)
	}
	if call.prompt != firstPrompt {
		t.Errorf("prompt = %q, want the PromptFile content without its trailing newlines", call.prompt)
	}
	if len(f.calls) != 0 {
		t.Errorf("clean first run made %d further orchestrator calls", len(f.calls))
	}
}

func TestFirstRun_SessionFlagsFollowTheHandoffSessionMode(t *testing.T) {
	setMode := func(f *fixture, mode string) {
		f.write(f.handoffFile, `{"Driver":"claude","SessionMode":"`+mode+`","PromptFile":"`+filepath.Join(f.dir, "prompt.md")+`"}`)
	}
	cases := []struct {
		name       string
		mode       string
		transcript bool
		want       func(id string) string
	}{
		{"initial pins the session", "initial", false, func(id string) string { return "--session-id " + id }},
		{"resume with a transcript", "resume", true, func(id string) string { return "--resume " + id }},
		{"resume without a transcript", "resume", false, func(string) string { return "" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			setMode(f, c.mode)
			if c.transcript {
				f.transcript()
			}
			f.run()
			if want := c.want(f.sessionID()); f.firstCall.session != want {
				t.Errorf("session file = %q, want %q", f.firstCall.session, want)
			}
		})
	}
}

func TestFirstRun_ManifestPathOnlyWhenOutboxMounted(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(*fixture)
		want  bool
	}{
		{"read-write box with no host-mediated remote", func(*fixture) {}, false},
		{"host-mediated remote", func(f *fixture) { f.env.HostMediatedRemote = true }, true},
		{"read-only outbox-relay-capable box", func(f *fixture) { f.readOnlyRelay() }, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			c.setup(f)
			// A read-only box also runs the PR-intent gate, which wants its marker.
			f.firstRun(readyLine+"\n", 0, prIntentLine)
			f.run()
			got, ok := f.firstCall.argv["--manifest-path"]
			if ok != c.want {
				t.Fatalf("--manifest-path present = %v, want %v", ok, c.want)
			}
			if c.want && got != f.in.OutboxDir+"/manifest.json" {
				t.Errorf("--manifest-path = %q", got)
			}
		})
	}
}

func TestFirstRun_PrintsItsOutcomeLineExactlyOnce(t *testing.T) {
	f := newFixture(t)
	f.run()
	if n := countLine(f.lines(), readyLine); n != 1 {
		t.Errorf("outcome line printed %d times:\n%s", n, f.stdout())
	}
}

// Regression (#1611): claude sometimes wraps the outcome line in markdown, which
// the extractor's leading-token anchor would otherwise miss.
func TestFirstRun_ReemitsAMarkdownWrappedOutcomeLineBare(t *testing.T) {
	for name, wrapped := range map[string]string{
		"backticks":  "`" + readyLine + "`",
		"bold":       "**" + readyLine + "**",
		"whitespace": "  \t" + readyLine + " \t",
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.firstRun(wrapped+"\n", 0)
			f.run()
			if n := countLine(f.lines(), readyLine); n != 1 {
				t.Errorf("bare outcome line printed %d times:\n%s", n, f.stdout())
			}
			if len(f.calls) != 0 || len(f.backstopCfgs) != 0 {
				t.Errorf("calls=%d backstops=%d, a wrapped line must count as present", len(f.calls), len(f.backstopCfgs))
			}
		})
	}
}

func TestFirstRun_KeepsTheLastOutcomeLineAcrossResultEvents(t *testing.T) {
	f := newFixture(t)
	f.firstRun(blockedLine+"\n", 0, strings.TrimSuffix(resultEvent(readyLine+"\n"), "\n"))
	f.run()
	if n := countLine(f.lines(), readyLine); n != 1 {
		t.Errorf("last outcome line printed %d times:\n%s", n, f.stdout())
	}
	if n := countLine(f.lines(), blockedLine); n != 0 {
		t.Errorf("stale outcome line printed %d times:\n%s", n, f.stdout())
	}
}

func TestFirstRun_NonZeroExitIsTheRunsAndSkipsTheNudges(t *testing.T) {
	f := newFixture(t)
	f.firstRun("crashed\n", 3)
	if rc := f.run(); rc != 3 {
		t.Fatalf("rc = %d, want the orchestrator's 3", rc)
	}
	if len(f.calls) != 0 || len(f.backstopCfgs) != 0 {
		t.Fatalf("calls=%d backstops=%d, want neither on a non-zero exit", len(f.calls), len(f.backstopCfgs))
	}
}

func TestFirstRun_TempFilesAreCleanedExceptLogs(t *testing.T) {
	f := newFixture(t)
	f.run()
	for _, name := range []string{"--prompt-file", "--session-file"} {
		if _, err := os.Stat(f.firstCall.argv[name]); !os.IsNotExist(err) {
			t.Errorf("%s temp file survived the first run: %v", name, err)
		}
	}
	if _, err := os.Stat(f.firstCall.argv["--log-path"]); err != nil {
		t.Errorf("stream log must survive for later scans: %v", err)
	}
	if _, err := os.Stat(f.handoffFile); err != nil {
		t.Errorf("the shared handoff must stay: %v", err)
	}
}

func TestFirstRun_UnreadablePromptFileIsFatal(t *testing.T) {
	f := newFixture(t)
	f.write(f.handoffFile, `{"Driver":"claude","PromptFile":"`+filepath.Join(f.dir, "missing.md")+`"}`)
	_, err := run(f.in, f.env, f.d)
	if err == nil || !strings.HasPrefix(err.Error(), "first-run: ") {
		t.Fatalf("error = %v", err)
	}
	if f.ranFirst {
		t.Error("the orchestrator ran without a prompt")
	}
}

func TestFirstRun_UnreadableHandoffIsFatal(t *testing.T) {
	f := newFixture(t)
	f.handoffFile = filepath.Join(f.dir, "missing.json")
	_, err := run(f.in, f.env, f.d)
	if err == nil || !strings.HasPrefix(err.Error(), "first-run handoff: ") {
		t.Fatalf("error = %v", err)
	}
}

func TestFirstRun_AnnounceLine(t *testing.T) {
	for _, c := range []struct {
		name string
		env  func(*promptassembly.Env)
		want string
	}{
		{"work names the issue and the branch", func(*promptassembly.Env) {},
			"==> claude implementing issue #42 on agent/issue-42"},
		{"research is advise-only and names no branch", func(e *promptassembly.Env) {
			e.DispatchKind, e.DispatchAnnounceVerb = "research", "researching"
		}, "==> claude researching issue #42"},
		{"butler names its chore", func(e *promptassembly.Env) {
			e.DispatchKind, e.DispatchKeying, e.DispatchAnnounceVerb, e.ChoreName = "butler", "chore", "sweeping", "bugs"
		}, "==> claude sweeping chore bugs"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			c.env(&f.env)
			f.run()
			if got := f.lines()[0]; got != c.want {
				t.Errorf("first stdout line = %q, want %q", got, c.want)
			}
		})
	}
}

func TestPhaseError_Format(t *testing.T) {
	err := &phaseError{phase: "bundle-out", err: errors.New("x")}
	if err.Error() != "bundle-out: x" || !errors.Is(err, err.err) {
		t.Fatalf("phaseError = %q", err.Error())
	}
}

// --- real orchestrator runner ---

func TestExecOrchestrator_NotOnPathIsRC127(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var stderr bytes.Buffer
	if rc := execOrchestrator(nil, io.Discard, &stderr, nil); rc != 127 {
		t.Fatalf("rc = %d, want 127", rc)
	}
	if !strings.Contains(stderr.String(), "orchestrator") {
		t.Errorf("stderr = %q, want a diagnosis", stderr.String())
	}
}

func TestExecOrchestrator_PropagatesExitCodeAndOutput(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\necho \"args:$*\"\nexit 5\n"
	if err := os.WriteFile(filepath.Join(bin, "orchestrator"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	var stdout bytes.Buffer
	rc := execOrchestrator([]string{"--a", "b"}, &stdout, io.Discard, nil)
	if rc != 5 || stdout.String() != "args:--a b\n" {
		t.Fatalf("rc=%d stdout=%q", rc, stdout.String())
	}
}

// --- flags ---

// allFlags is one `--name=value` token per flag, so a bool needs no separate
// value argument.
func allFlags() []string {
	return []string{
		"--work-dir=/w", "--outbox-dir=/o",
		"--registry=/reg.json", "--validate-markers-registry=/markers.json", "--driver-skills-dir=/skills",
		"--prompts-dir=/prompts", "--agents-prompt-files={}", "--driver-agent-files-dir=",
		"--comms-contract-file=/comms", "--check-contract-file=/check",
		"--outcome-contract-file=/outcome", "--research-outcome-contract-file=/research",
		"--argv-prompt-style=flag", "--argv-prompt-flag=-p", "--argv-model-flag=--model",
		"--argv-model-omit-empty=0", "--argv-agents-flag=--agents", "--argv-effort-flag=--effort",
		"--argv-order=prompt model agents", "--model=opus", "--effort=high",
		"--driver=claude", "--driver-bin=claude", "--driver-flags=--verbose", "--heartbeat-log=/hb",
		"--max-budget-tokens=1000", "--max-budget-usd=2.5",
		"--prework-rebase-conflict=1", "--publish-rebase=0",
		"--harness-skills-dir=/harness-skills", "--operator-skills-dir=/operator-skills",
		"--harness-home-agent-dir=/home-agent", "--driver-session-cache-dir=/session-cache",
	}
}

func TestParseFlags_AllSupplied(t *testing.T) {
	in, err := parseFlags(allFlags(), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	want := inputs{
		WorkDir:               "/w",
		PreworkRebaseConflict: true,
		OutboxDir:             "/o",
		HarnessSkillsDir:      "/harness-skills",
		OperatorSkillsDir:     "/operator-skills",
		HarnessHomeAgentDir:   "/home-agent",
		DriverSessionCacheDir: "/session-cache",
		Assembly: assemblyInputs{
			RegistryFile:                "/reg.json",
			ValidateMarkersFile:         "/markers.json",
			SkillsDir:                   "/skills",
			PromptsDir:                  "/prompts",
			AgentsPromptFiles:           "{}",
			CommsContractFile:           "/comms",
			CheckContractFile:           "/check",
			OutcomeContractFile:         "/outcome",
			ResearchOutcomeContractFile: "/research",
			Passthrough: promptassembly.Passthrough{
				Model: "opus", Effort: "high", Driver: "claude", DriverBin: "claude",
				DriverFlags: "--verbose", HeartbeatLog: "/hb",
				ArgvShape: promptassembly.ArgvShape{
					PromptStyle: "flag", PromptFlag: "-p", ModelFlag: "--model", AgentsFlag: "--agents",
					EffortFlag: "--effort", Order: []string{"prompt", "model", "agents"},
				},
				Caps: promptassembly.Caps{
					MaxSlices: promptassembly.DefaultMaxSlices, MaxReviewRounds: promptassembly.DefaultMaxReviewRounds,
					MaxBudgetTokens: 1000, MaxBudgetUSD: 2.5,
				},
			},
		},
	}
	if !reflect.DeepEqual(in, want) {
		t.Fatalf("inputs = %+v, want %+v", in, want)
	}
}

func TestHomeLayout_PopulatesBeforeAssembly(t *testing.T) {
	f := newFixture(t)
	harness, operator, staged := filepath.Join(f.dir, "harness"), filepath.Join(f.dir, "operator"), filepath.Join(f.dir, "staged")
	for path, content := range map[string]string{
		filepath.Join(harness, "a", "SKILL.md"):    "harness a",
		filepath.Join(harness, "b", "SKILL.md"):    "harness b",
		filepath.Join(operator, "b", "SKILL.md"):   "operator b",
		filepath.Join(staged, ".claude", "x.md"):   "staged x",
		filepath.Join(staged, ".config", "y.json"): "staged y",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		f.write(path, content)
	}
	f.in.HarnessSkillsDir, f.in.OperatorSkillsDir, f.in.HarnessHomeAgentDir = harness, operator, staged
	checked := 0
	f.d.Assemble = func(in assemblyInputs, _ promptassembly.Env, _ io.Writer) (string, error) {
		home := f.knobs["HOME"]
		for path, want := range map[string]string{
			filepath.Join(in.SkillsDir, "a", "SKILL.md"): "harness a",
			filepath.Join(in.SkillsDir, "b", "SKILL.md"): "operator b",
			filepath.Join(home, ".claude", "x.md"):       "staged x",
			filepath.Join(home, ".config", "y.json"):     "staged y",
		} {
			got, err := os.ReadFile(path)
			if err != nil || string(got) != want {
				t.Errorf("at Assemble, %s = %q, %v; want %q", path, got, err, want)
			}
			checked++
		}
		f.assembled++
		return f.handoffFile, nil
	}
	f.run()
	if checked == 0 || f.assembled != 1 {
		t.Fatalf("Assemble ran %d times, checked %d files", f.assembled, checked)
	}
}

func TestHomeLayout_FailureIsPhaseErrorAndStopsTheRun(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		f := newFixture(t)
		f.in.PreworkRebaseConflict = conflict
		blocker := filepath.Join(f.dir, "blocker")
		f.write(blocker, "a file, not a directory")
		f.in.Assembly.SkillsDir = filepath.Join(blocker, "skills")
		// AbortRebase and Git stay nil: the layout must fail before the
		// conflict pass could reach either.
		rc, err := run(f.in, f.env, f.d)
		var pe *phaseError
		if !errors.As(err, &pe) || pe.phase != "home-layout" {
			t.Fatalf("conflict=%v: run() = %d, %v; want a home-layout phaseError", conflict, rc, err)
		}
		if f.assembled != 0 || f.ranFirst {
			t.Errorf("conflict=%v: Assemble ran %d times, orchestrator ran=%v; want neither after a layout failure", conflict, f.assembled, f.ranFirst)
		}
	}
}

func TestHomeLayout_EmptyHomeIsPhaseErrorAndCopiesNothing(t *testing.T) {
	f := newFixture(t)
	staged := filepath.Join(f.dir, "staged")
	if err := os.MkdirAll(filepath.Join(staged, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.write(filepath.Join(staged, ".claude", "settings.json"), "{}")
	f.in.HarnessHomeAgentDir = staged
	f.knobs["HOME"] = ""
	// Chdir into the work dir: an empty HOME makes the copy land relative to
	// the working directory, the cloned repo.
	if err := os.MkdirAll(f.in.WorkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(f.in.WorkDir)
	rc, err := run(f.in, f.env, f.d)
	var pe *phaseError
	if !errors.As(err, &pe) || pe.phase != "home-layout" {
		t.Fatalf("run() = %d, %v; want a home-layout phaseError", rc, err)
	}
	if f.assembled != 0 || f.ranFirst {
		t.Errorf("Assemble ran %d times, orchestrator ran=%v; want neither", f.assembled, f.ranFirst)
	}
	entries, err := os.ReadDir(f.in.WorkDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("work dir holds %d entries after the failed layout; want nothing copied", len(entries))
	}
}

func TestParseFlags_MalformedBudgetDegradesToZero(t *testing.T) {
	args := append(allFlags(), "--max-budget-tokens=lots", "--max-budget-usd=-3")
	in, err := parseFlags(args, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if caps := in.Assembly.Passthrough.Caps; caps.MaxBudgetTokens != 0 || caps.MaxBudgetUSD != 0 {
		t.Fatalf("caps = %+v, want a malformed budget to degrade to 0", caps)
	}
}

func TestParseFlags_EveryFlagRequired(t *testing.T) {
	all := allFlags()
	for i, tok := range all {
		missing := append(append([]string{}, all[:i]...), all[i+1:]...)
		name, _, _ := strings.Cut(tok, "=")
		if _, err := parseFlags(missing, io.Discard); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("without %s: error = %v, want it named", name, err)
		}
	}
}

func TestParseFlags_RetiredFlagsAreGone(t *testing.T) {
	for _, old := range []string{"--handoff-file", "--driver-exit-code", "--outcome-line", "--stream-log", "--driver-text-log", "--resume-session-file", "--devshell", "--devshell-name"} {
		if _, err := parseFlags(append(allFlags(), old+"=x"), io.Discard); err == nil {
			t.Errorf("%s accepted; box now assembles and runs the first Driver run itself", old)
		}
	}
}

func TestParseFlags_UnknownFlagAndStrayArgRejected(t *testing.T) {
	if _, err := parseFlags(append(allFlags(), "--bogus"), io.Discard); err == nil {
		t.Error("unknown flag accepted")
	}
	if _, err := parseFlags(append(allFlags(), "stray"), io.Discard); err == nil {
		t.Error("stray positional accepted")
	}
}

func TestMainRun_ErrorPrintsBoxPhaseAndExitsOne(t *testing.T) {
	var stdout, stderr bytes.Buffer
	d := deps{Stdout: &stdout, Stderr: &stderr}
	rc := mainRun(allFlags(), promptassembly.Env{DispatchKind: "bogus"}, d)
	if rc != 1 || !strings.HasPrefix(stderr.String(), "box: ") {
		t.Fatalf("rc=%d stderr=%q", rc, stderr.String())
	}
}

// --- prompt assembly ---

func TestAssemble_FailureIsAPhaseErrorAndNoOrchestratorRuns(t *testing.T) {
	f := newFixture(t)
	f.assembleErr = errors.New("registry unreadable")
	_, err := run(f.in, f.env, f.d)
	var pe *phaseError
	if !errors.As(err, &pe) || pe.phase != "prompt-assembly" || !strings.Contains(err.Error(), "registry unreadable") {
		t.Fatalf("err = %v, want a prompt-assembly phase error carrying the cause", err)
	}
	if f.ranFirst || len(f.calls) != 0 {
		t.Error("the orchestrator ran after assembly failed")
	}
}

func TestAssemble_RunsOnceBeforeTheFirstDriverRun(t *testing.T) {
	f := newFixture(t)
	f.run()
	if f.assembled != 1 {
		t.Fatalf("assemble ran %d times, want once", f.assembled)
	}
}

// assemblyFixture points assemblePrompt at the repo's real templates and the
// promptassembly test registries, with tiny contract files and a skills dir
// holding only the named skills.
func assemblyFixture(t *testing.T, skills ...string) assemblyInputs {
	t.Helper()
	dir := t.TempDir()
	contract := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("# "+name+"\n\nbody\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	skillsDir := filepath.Join(dir, "skills")
	for _, s := range skills {
		if err := os.MkdirAll(filepath.Join(skillsDir, s), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(skillsDir, s, "SKILL.md"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return assemblyInputs{
		RegistryFile:                repopath.RegistryJSON(),
		ValidateMarkersFile:         repopath.ValidateMarkersJSON(),
		SkillsDir:                   skillsDir,
		PromptsDir:                  repopath.PromptsDir(),
		CommsContractFile:           contract("comms-contract.md"),
		CheckContractFile:           contract("check-contract.md"),
		OutcomeContractFile:         contract("outcome-contract.md"),
		ResearchOutcomeContractFile: contract("research-outcome-contract.md"),
		Passthrough: promptassembly.Passthrough{
			Driver: "claude", Model: "opus",
			ArgvShape: promptassembly.ArgvShape{PromptStyle: "flag", ModelFlag: "--model", Order: []string{"prompt", "model"}},
			Caps:      promptassembly.Caps{MaxSlices: 9, MaxReviewRounds: 3},
		},
	}
}

// coveredWorkEnv is a work cell Assemble accepts, as the real Box env carries it.
func coveredWorkEnv() promptassembly.Env {
	return promptassembly.Env{
		IssueTracker: "github", TrackerAxisRead: "GITHUB", TrackerAxisWrite: "GITHUB", TrackerAxisFiler: "GH",
		CodeForge: "github", ForgeBackend: "GH", BoxWriteEnabled: true, DispatchKind: "work",
		IssueNumber: "42", IssueTitle: "Do it", Branch: "agent/issue-42", BaseBranch: "main",
		InProgressLabel: "agent-in-progress", CompleteLabel: "agent-complete", RunNonce: "n0nce",
	}
}

const cavemanSentinel = "Default to the `/caveman` skill for all narration and prose output this run."

func assembledPrompt(t *testing.T, in assemblyInputs) (handoffPath string, h promptassembly.Handoff, prompt string) {
	t.Helper()
	handoffPath, err := assemblePrompt(in, coveredWorkEnv(), io.Discard)
	if err != nil {
		t.Fatalf("assemblePrompt: %v", err)
	}
	h, err = promptassembly.LoadHandoffFile(handoffPath)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(h.PromptFile)
	if err != nil {
		t.Fatalf("handoff names an unreadable prompt: %v", err)
	}
	return handoffPath, h, string(b)
}

func TestAssemblePrompt_WritesTheFilesTheHandoffNames(t *testing.T) {
	in := assemblyFixture(t, "caveman")
	in.Passthrough.Devshell = true
	_, h, prompt := assembledPrompt(t, in)
	if h.Driver != "claude" || h.Model != "opus" || !h.Devshell || h.Issue != "42" || h.SessionMode != "initial" {
		t.Errorf("handoff = %+v; want the passthrough, issue and session mode layered in", h)
	}
	if h.Caps.MaxSlices != 9 || h.Caps.MaxReviewRounds != 3 {
		t.Errorf("caps = %+v", h.Caps)
	}
	if !strings.Contains(prompt, "42") {
		t.Errorf("prompt does not mention the issue:\n%s", prompt)
	}
	// A work cell renders a review prompt, and the handoff must name a file
	// that outlives the call.
	if h.ReviewPromptFile == "" {
		t.Fatal("handoff names no review prompt for a work cell")
	}
	if _, err := os.Stat(h.ReviewPromptFile); err != nil {
		t.Errorf("review prompt file: %v", err)
	}
}

func TestAssemblePrompt_NoReviewPromptFileWhenNoneRendered(t *testing.T) {
	in := assemblyFixture(t)
	env := coveredWorkEnv()
	env.DispatchKind = "research"
	handoff, err := assemblePrompt(in, env, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	h, err := promptassembly.LoadHandoffFile(handoff)
	if err != nil {
		t.Fatal(err)
	}
	if h.ReviewPromptFile != "" {
		t.Errorf("ReviewPromptFile = %q; want none", h.ReviewPromptFile)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(handoff), "review-prompt.txt")); err == nil {
		t.Error("a review prompt file was left behind")
	}
}

// A fix pass renders no review prompt, so assembly must leave no file behind
// for the life of the Box and the handoff must name none (issue #2975).
func TestAssemblePrompt_FixPassLeavesNoReviewPromptFile(t *testing.T) {
	env := coveredWorkEnv()
	env.FixPass = 1
	handoff, err := assemblePrompt(assemblyFixture(t), env, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	h, err := promptassembly.LoadHandoffFile(handoff)
	if err != nil {
		t.Fatal(err)
	}
	if h.ReviewPromptFile != "" {
		t.Errorf("ReviewPromptFile = %q; want none on a fix pass", h.ReviewPromptFile)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(handoff), "review-prompt.txt")); err == nil {
		t.Error("a review prompt file was left behind")
	}
}

// The flag-supplied Driver settings ride the Handoff to the orchestrator and
// driver-exec, which no longer read them from the environment (issue #2975).
func TestAssemblePrompt_HandoffCarriesTheDriverSettings(t *testing.T) {
	in := assemblyFixture(t)
	in.Passthrough = promptassembly.Passthrough{
		Driver: "claude", DriverBin: "claude", DriverFlags: "--verbose", Model: "claude-test-model", Effort: "high",
		ArgvShape: promptassembly.ArgvShape{
			PromptStyle: "flag", PromptFlag: "-p", ModelFlag: "--model", AgentsFlag: "--agents", EffortFlag: "--effort",
			Order: []string{"prompt", "model", "agents", "session", "driverFlags", "effort"},
		},
		Caps: promptassembly.Caps{MaxSlices: 9, MaxReviewRounds: 3, MaxBudgetTokens: 500000, MaxBudgetUSD: 4.44},
	}
	_, h, _ := assembledPrompt(t, in)
	if h.DriverBin != "claude" || h.DriverFlags != "--verbose" || h.Model != "claude-test-model" || h.Effort != "high" {
		t.Errorf("handoff driver settings = %+v", h)
	}
	if h.Caps.MaxBudgetTokens != 500000 || h.Caps.MaxBudgetUSD != 4.44 {
		t.Errorf("caps = %+v; want the budgets", h.Caps)
	}
	if want := in.Passthrough.ArgvShape; !reflect.DeepEqual(h.ArgvShape, want) {
		t.Errorf("ArgvShape = %+v; want %+v (ModelOmitEmpty false)", h.ArgvShape, want)
	}
}

// An unset budget knob is the schema default (0), which reads back as zero
// Caps, not an absent field.
func TestAssemblePrompt_UnsetBudgetsAreZeroCaps(t *testing.T) {
	_, h, _ := assembledPrompt(t, assemblyFixture(t))
	if h.Caps.MaxBudgetTokens != 0 || h.Caps.MaxBudgetUSD != 0 {
		t.Errorf("caps = %+v; want zero budgets", h.Caps)
	}
}

func TestAssemblePrompt_SkillProbesFollowTheSkillsDir(t *testing.T) {
	_, _, baked := assembledPrompt(t, assemblyFixture(t, "caveman"))
	_, _, bare := assembledPrompt(t, assemblyFixture(t))
	if !strings.Contains(baked, cavemanSentinel) {
		t.Error("a baked caveman skill did not reach the prompt")
	}
	if strings.Contains(bare, cavemanSentinel) {
		t.Error("the caveman fragment rendered with no baked skill")
	}
}

func TestAssemblePrompt_UnreadableRegistryFailsBeforeWritingAnything(t *testing.T) {
	in := assemblyFixture(t)
	in.RegistryFile = filepath.Join(t.TempDir(), "absent.json")
	if _, err := assemblePrompt(in, coveredWorkEnv(), io.Discard); err == nil {
		t.Fatal("assemblePrompt succeeded with an unreadable registry")
	}
}

func TestAssemblePrompt_UnsupportedCellFails(t *testing.T) {
	env := coveredWorkEnv()
	env.DispatchKind = "bogus"
	if _, err := assemblePrompt(assemblyFixture(t), env, io.Discard); err == nil {
		t.Fatal("assemblePrompt accepted an unsupported dispatch kind")
	}
}

func TestRun_AssemblesThenTheFirstPassGetsTheHandoffBoxWrote(t *testing.T) {
	f := newFixture(t)
	in := assemblyFixture(t, "caveman")
	f.in.Assembly = in
	f.env = coveredWorkEnv()
	f.d.Assemble = assemblePrompt
	f.run()

	handoff := f.firstCall.argv["--handoff-file"]
	h, err := promptassembly.LoadHandoffFile(handoff)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(h.PromptFile)
	if err != nil {
		t.Fatal(err)
	}
	if f.firstCall.prompt != strings.TrimRight(string(want), "\n") {
		t.Errorf("first pass prompt differs from the assembled prompt file %s", h.PromptFile)
	}
	if !strings.Contains(f.firstCall.prompt, cavemanSentinel) {
		t.Error("the first pass prompt lacks the baked caveman fragment")
	}
}

// --- the toolchain decision ---

const (
	probingLine = "==> flake.nix found in cloned repo; probing for devShell"
	foundLine   = "==> devShell found — lifecycle will run inside nix develop"
	absentLine  = "==> no devShell in flake (or nix develop failed) — using baked toolchain"
	goHintLine  = "==> hint: go mod project detected; set 'prefetch' to warm dependency caches per run, or 'packages' to bake a toolchain into the image"
)

// target gives the clone the named files.
func (f *fixture) target(names ...string) {
	f.t.Helper()
	if err := os.MkdirAll(f.in.WorkDir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	for _, n := range names {
		f.write(filepath.Join(f.in.WorkDir, n), "")
	}
}

func (f *fixture) passthrough() promptassembly.Passthrough {
	f.t.Helper()
	if len(f.assembledIn) != 1 {
		f.t.Fatalf("Assemble ran %d times, want 1", len(f.assembledIn))
	}
	return f.assembledIn[0].Passthrough
}

func TestToolchain_NoFlakeMeansNoDevshellAndNoNix(t *testing.T) {
	f := newFixture(t)
	f.target("README.md")
	f.run()
	if p := f.passthrough(); p.Devshell || p.DevshellName != "default" {
		t.Errorf("Devshell, DevshellName = %v, %q; want false, default", p.Devshell, p.DevshellName)
	}
	if len(f.nixCalls) != 0 || len(f.prefetched) != 0 {
		t.Errorf("nix calls %v, prefetches %d; want none", f.nixCalls, len(f.prefetched))
	}
	if got := f.lines(); got[0] != announceLine {
		t.Errorf("stdout = %q, want no toolchain lines", got)
	}
}

func TestToolchain_DevshellFoundCarriesTheNameIntoAssembly(t *testing.T) {
	f := newFixture(t)
	f.target("flake.nix")
	f.knobs["DEV_SHELL_NAME"], f.knobs["DEV_SHELL_PROBE_TIMEOUT"] = "ci", "300"
	f.run()
	if p := f.passthrough(); !p.Devshell || p.DevshellName != "ci" {
		t.Errorf("Devshell, DevshellName = %v, %q; want true, ci", p.Devshell, p.DevshellName)
	}
	if want := [][]string{{"develop", ".#ci", "--command", "true"}}; !reflect.DeepEqual(f.nixCalls, want) {
		t.Errorf("nix calls = %q, want %q", f.nixCalls, want)
	}
	if got := f.lines(); got[0] != probingLine || got[1] != foundLine {
		t.Errorf("stdout = %q, want the probing then the found line first", got)
	}
}

func TestToolchain_DevshellAbsentFallsBackToDefaultName(t *testing.T) {
	f := newFixture(t)
	f.target("flake.nix")
	f.knobs["DEV_SHELL_NAME"], f.knobs["DEV_SHELL_PROBE_TIMEOUT"] = "ci", "300"
	f.nixErr = errors.New("exit status 1")
	f.run()
	if p := f.passthrough(); p.Devshell || p.DevshellName != "default" {
		t.Errorf("Devshell, DevshellName = %v, %q; want false, default", p.Devshell, p.DevshellName)
	}
	if got := f.lines(); got[0] != probingLine || got[1] != absentLine {
		t.Errorf("stdout = %q, want the probing then the no-devShell line first", got)
	}
}

func TestToolchain_SelfContainedSkipsEveryPhase(t *testing.T) {
	f := newFixture(t)
	f.env.SelfContained = true
	f.target("flake.nix", "go.sum")
	f.knobs["PREFETCH"] = "go mod download"
	f.run()
	if p := f.passthrough(); p.Devshell || p.DevshellName != "default" {
		t.Errorf("Devshell, DevshellName = %v, %q; want false, default", p.Devshell, p.DevshellName)
	}
	if len(f.nixCalls) != 0 || len(f.prefetched) != 0 {
		t.Errorf("nix calls %v, prefetches %d; want none", f.nixCalls, len(f.prefetched))
	}
	if got := f.lines(); got[0] != announceLine {
		t.Errorf("stdout = %q, want no toolchain lines", got)
	}
}

func TestToolchain_HintPrintsWithoutPrefetchAndIsSuppressedByIt(t *testing.T) {
	f := newFixture(t)
	f.target("go.sum")
	f.run()
	if got := f.lines(); got[0] != goHintLine {
		t.Errorf("stdout = %q, want the hint first", got)
	}
	if len(f.prefetched) != 0 {
		t.Errorf("prefetched %d times with no hook", len(f.prefetched))
	}

	f = newFixture(t)
	f.target("go.sum")
	f.knobs["PREFETCH"] = "go mod download"
	f.run()
	if strings.Contains(f.out.String(), "hint:") {
		t.Errorf("stdout = %q, want no hint with prefetch set", f.out.String())
	}
}

func TestToolchain_PrefetchRunsTheHookInTheCloneWithBoxStdio(t *testing.T) {
	f := newFixture(t)
	f.target("go.sum")
	f.knobs["PREFETCH"] = "go mod download"
	f.knobs["PATH"] = "/harness/bin"
	f.run()
	if len(f.prefetched) != 1 {
		t.Fatalf("prefetched %d times, want 1", len(f.prefetched))
	}
	cmd := f.prefetched[0]
	if want := []string{"bash", "-c", "go mod download"}; !reflect.DeepEqual(cmd.Args, want) {
		t.Errorf("args = %q, want %q", cmd.Args, want)
	}
	if cmd.Dir != f.in.WorkDir || cmd.Stdout != &f.out || cmd.Stderr != &f.errb {
		t.Errorf("Dir, Stdout, Stderr = %q, %v, %v; want the clone and box's stdio", cmd.Dir, cmd.Stdout, cmd.Stderr)
	}
}

func TestToolchain_PrefetchInsideADevshellKeepsTheHarnessPath(t *testing.T) {
	f := newFixture(t)
	f.target("flake.nix")
	f.knobs["PREFETCH"], f.knobs["PATH"], f.knobs["DEV_SHELL_PROBE_TIMEOUT"] = "make deps", "/harness/bin", "300"
	f.run()
	if len(f.prefetched) != 1 {
		t.Fatalf("prefetched %d times, want 1", len(f.prefetched))
	}
	if args := f.prefetched[0].Args; args[0] != "nix" || args[len(args)-1] != "/harness/bin" {
		t.Errorf("args = %q, want nix develop with the harness PATH", args)
	}
}

func TestToolchain_PrefetchFailureWarnsAndTheRunContinues(t *testing.T) {
	f := newFixture(t)
	f.target("go.sum")
	f.knobs["PREFETCH"] = "false"
	f.prefetchErr = errors.New("exit status 1")
	if rc := f.run(); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if want := "==> WARNING: prefetch hook failed (exit status 1) — continuing"; countLine(f.lines(), want) != 1 {
		t.Errorf("stdout = %q, want %q", f.lines(), want)
	}
	if f.assembled != 1 || !f.ranFirst {
		t.Errorf("assembled %d, ranFirst %v; want the run to carry on", f.assembled, f.ranFirst)
	}
}

func TestToolchain_RunsBeforeTheHomeLayout(t *testing.T) {
	f := newFixture(t)
	f.target("flake.nix")
	f.knobs["DEV_SHELL_PROBE_TIMEOUT"] = "300"
	f.nixAtCall = func() {
		if _, err := os.Stat(f.in.Assembly.SkillsDir); err == nil {
			t.Error("the home layout ran before the toolchain probe")
		}
	}
	harness := filepath.Join(f.dir, "harness")
	if err := os.MkdirAll(filepath.Join(harness, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.write(filepath.Join(harness, "a", "SKILL.md"), "a")
	f.in.HarnessSkillsDir = harness
	f.run()
	if len(f.nixCalls) != 1 {
		t.Fatalf("nix ran %d times, want 1", len(f.nixCalls))
	}
	if _, err := os.Stat(f.in.Assembly.SkillsDir); err != nil {
		t.Errorf("home layout never ran: %v", err)
	}
}

func TestToolchain_HintPrecedesTheProbeAndItsOutcome(t *testing.T) {
	f := newFixture(t)
	f.target("go.sum", "flake.nix")
	f.knobs["DEV_SHELL_PROBE_TIMEOUT"] = "5m"
	f.run()
	got := f.lines()
	if len(got) < 3 || got[0] != goHintLine || got[1] != probingLine || got[2] != foundLine {
		t.Errorf("stdout = %q, want the hint, the probing line, then the outcome", got)
	}
}
