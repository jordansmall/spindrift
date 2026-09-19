package dispatch

import (
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/passmanifest"
	"spindrift.dev/launcher/internal/retry"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/testutil"
)

// noOpenPR is the default OpenPRForIssue: no PR exists yet, so a zero-exit
// transient classification proceeds to retry rather than short-circuiting.
func noOpenPR(string) (bool, error) { return false, nil }

func retryConfig(max, backoffSecs, holdJitter int) Config {
	return Config{
		Policy: retry.Policy{
			Max:    max,
			Unit:   time.Duration(backoffSecs) * time.Second,
			Jitter: time.Duration(holdJitter) * time.Second,
		},
		OpenPRForIssue: noOpenPR,
	}
}

func fakeClock(now time.Time, calls *[]time.Duration) Clock {
	return Clock{
		Now:   func() time.Time { return now },
		Sleep: func(d time.Duration) { *calls = append(*calls, d) },
	}
}

// newTestDispatch builds a Dispatch through NewFactory with an injected fake
// Clock, which the ordinary constructors do not allow.
func newTestDispatch(t *testing.T, cfg Config, fr runner.Runner, drv fakeDriver, clock Clock) *Dispatch {
	t.Helper()
	dir := tempLogDir(t)
	f, err := NewFactory(cfg, dir, fr, drv, clock)
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	t.Cleanup(f.Cleanup)
	return f.New("1", "t")
}

// newTestDispatchDiscard discards the Factory's heartbeat sink the way the
// console entry point does (issue #1583), so a test can assert that retry and
// hold status lines stay off stdout (issue #1829).
func newTestDispatchDiscard(t *testing.T, cfg Config, fr runner.Runner, drv fakeDriver, clock Clock) *Dispatch {
	t.Helper()
	dir := tempLogDir(t)
	f, err := NewFactory(cfg, dir, fr, drv, clock)
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	t.Cleanup(f.Cleanup)
	f.SetHeartbeatOut(io.Discard)
	return f.New("1", "t")
}

func TestDispatchWithRetry_SuccessOnFirstRun(t *testing.T) {
	fr := runner.NewFake()
	called := false
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		called = true
		return driver.Classification{}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, drv, fakeClock(time.Time{}, &sleeps))
	fr.WriteToOutput = nonceLine(d, "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok")

	result := d.Run()

	if !result.Success {
		t.Error("want Success=true, got false")
	}
	if !result.Resolved.Found {
		t.Fatal("want OutcomeFound=true")
	}
	if result.Resolved.Outcome.Status != "ready" {
		t.Errorf("Outcome.Status: got %q, want %q", result.Resolved.Outcome.Status, "ready")
	}
	if len(fr.RunCalls) != 1 {
		t.Errorf("RunCalls: got %d, want 1", len(fr.RunCalls))
	}
	if called {
		t.Error("classify should not be called when an outcome line was found")
	}
	if len(sleeps) != 0 {
		t.Errorf("sleep calls: got %d, want 0", len(sleeps))
	}
}

// A single-line, nonce-guarded SPINDRIFT_COMMENT is the host-mediated write
// channel for a local Dispatch's verdict or blocked comment (ADR 0032, issue
// #1692), carried as one base64 line instead of a multi-line block (issue
// #1940).
func TestDispatchWithRetry_SuccessWithCommentLinePopulatesResult(t *testing.T) {
	fr := runner.NewFake()
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, drv, fakeClock(time.Time{}, &sleeps))
	encoded := base64.StdEncoding.EncodeToString([]byte("verdict body"))
	fr.WriteToOutput = append([]byte("SPINDRIFT_COMMENT "+d.nonce+" "+encoded+"\n"),
		nonceLine(d, "SPINDRIFT_OUTCOME issue=1 landing=none status=recommend note=ok")...)

	result := d.Run()

	if !result.Resolved.Found {
		t.Fatal("want OutcomeFound=true")
	}
	if !result.CommentFound {
		t.Fatal("want CommentFound=true")
	}
	if result.Comment != "verdict body" {
		t.Errorf("Comment: got %q, want %q", result.Comment, "verdict body")
	}
}

// A SPINDRIFT_COMMENT line whose nonce does not match this run's own never
// surfaces on Result.Comment, the same guarantee LastCommentLineInLog
// documents (issue #1940).
func TestDispatchWithRetry_CommentLineWithWrongNonceNotFound(t *testing.T) {
	fr := runner.NewFake()
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, drv, fakeClock(time.Time{}, &sleeps))
	encoded := base64.StdEncoding.EncodeToString([]byte("attacker-controlled"))
	fr.WriteToOutput = append([]byte("SPINDRIFT_COMMENT not-this-runs-nonce "+encoded+"\n"),
		nonceLine(d, "SPINDRIFT_OUTCOME issue=1 landing=none status=recommend note=ok")...)

	result := d.Run()

	if !result.Resolved.Found {
		t.Fatal("want OutcomeFound=true")
	}
	if result.CommentFound {
		t.Fatal("want CommentFound=false for a nonce mismatch")
	}
	if result.Comment != "" {
		t.Errorf("Comment: got %q, want empty", result.Comment)
	}
}

// A nonce mismatch still counts on Result.CommentRejected, so a caller can
// log a warning that distinguishes no comment signal at all from a comment
// signal that failed nonce verification (issue #2976).
func TestDispatchWithRetry_CommentLineWithWrongNoncePopulatesRejectedCount(t *testing.T) {
	fr := runner.NewFake()
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, drv, fakeClock(time.Time{}, &sleeps))
	encoded := base64.StdEncoding.EncodeToString([]byte("attacker-controlled"))
	fr.WriteToOutput = append([]byte("SPINDRIFT_COMMENT not-this-runs-nonce "+encoded+"\n"),
		nonceLine(d, "SPINDRIFT_OUTCOME issue=1 landing=none status=recommend note=ok")...)

	result := d.Run()

	if !result.Resolved.Found {
		t.Fatal("want OutcomeFound=true")
	}
	if result.CommentRejected != 1 {
		t.Errorf("CommentRejected: got %d, want 1", result.CommentRejected)
	}
}

// SPINDRIFT_PR_INTENT is the host-mediated draft-PR-create channel a
// read-only github Box uses in place of its own gh pr create (issue #1919),
// carried as one base64-encoded line instead of a multi-line block (issue
// #1938).
func TestDispatchWithRetry_SuccessWithPRIntentLinePopulatesResult(t *testing.T) {
	fr := runner.NewFake()
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, drv, fakeClock(time.Time{}, &sleeps))

	payload := base64.StdEncoding.EncodeToString([]byte("feat: add widget\n\nAdds a widget."))
	fr.WriteToOutput = append([]byte("SPINDRIFT_PR_INTENT "+d.nonce+" "+payload+"\n"),
		nonceLine(d, "SPINDRIFT_OUTCOME issue=1 landing=agent/issue-1 status=ready note=ok")...)

	result := d.Run()

	if !result.Resolved.Found {
		t.Fatal("want OutcomeFound=true")
	}
	if !result.PRIntentFound {
		t.Fatal("want PRIntentFound=true")
	}
	want := "feat: add widget\n\nAdds a widget."
	if result.PRIntent != want {
		t.Errorf("PRIntent: got %q, want %q", result.PRIntent, want)
	}
}

// A synthetic backstop line (ADR 0036) wins the authoritative outcome by
// last-line-wins, but the driver's own near-miss self-report must still
// survive on Result.SelfReport (issues #2223 and #2224). The backstop line
// carries this run's own nonce so LastInLog accepts it as authoritative.
func TestDispatchWithRetry_SelfReportSurvivesSyntheticBackstop(t *testing.T) {
	fr := runner.NewFake()
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, drv, fakeClock(time.Time{}, &sleeps))

	fr.WriteToOutput = append([]byte("SPINDRIFT_OUTCOME: success\n"),
		nonceLine(d, "SPINDRIFT_OUTCOME issue=1 landing=agent/issue-1 status=blocked synthetic=true note=backstop")...)

	result := d.Run()

	if !result.Resolved.Found {
		t.Fatal("want OutcomeFound=true")
	}
	if !result.Resolved.Outcome.Synthetic {
		t.Error("Outcome.Synthetic: got false, want true")
	}
	if result.Resolved.Outcome.Status != "blocked" {
		t.Errorf("Outcome.Status: got %q, want %q", result.Resolved.Outcome.Status, "blocked")
	}
	if !result.Resolved.SelfReportFound {
		t.Fatal("want SelfReportFound=true")
	}
	if result.Resolved.SelfReport.Status != "success" {
		t.Errorf("SelfReport.Status: got %q, want %q", result.Resolved.SelfReport.Status, "success")
	}
	if result.Resolved.SelfReport.Parsed {
		t.Error("SelfReport.Parsed: got true, want false (near-miss line does not parse the full grammar)")
	}
}

// outcomeResult must print resolved.SelfReportError to stderr with the
// "self-report scan" message, the warning the pre-refactor code printed
// (issue #2343). One log file cannot make the outcome tier succeed while the
// self-report tier hits an I/O error, so this calls outcomeResult directly
// with an injected Resolved instead of driving it through d.Run().
func TestDispatchOutcomeResult_SelfReportErrorLoggedToStderr(t *testing.T) {
	fr := runner.NewFake()
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, drv, fakeClock(time.Time{}, &sleeps))

	logPath := d.logPath()
	if err := os.WriteFile(logPath, nil, 0o644); err != nil {
		t.Fatalf("write empty log: %v", err)
	}

	selfReportErr := errors.New("self-report boom")
	resolved := outcome.Resolved{
		Found:           true,
		Provenance:      outcome.ProvenanceGenuine,
		Outcome:         outcome.Outcome{Status: "ready"},
		SelfReportError: selfReportErr,
	}

	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	d.outcomeResult(logPath, resolved)
	w.Close()
	os.Stderr = old
	captured, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read captured stderr: %v", err)
	}
	stderr := string(captured)

	if !strings.Contains(stderr, "self-report scan") {
		t.Errorf("stderr must contain %q, got: %s", "self-report scan", stderr)
	}
	if !strings.Contains(stderr, selfReportErr.Error()) {
		t.Errorf("stderr must contain the injected error text %q, got: %s", selfReportErr.Error(), stderr)
	}
}

// SPINDRIFT_ISSUE_INTENT is the host-mediated issue-filing relay (issue
// #2018). Unlike PR-intent and comment it is one-to-many: every verifying
// line contributes its own payload.
func TestDispatchWithRetry_SuccessWithIssueIntentLinesPopulatesResult(t *testing.T) {
	fr := runner.NewFake()
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, drv, fakeClock(time.Time{}, &sleeps))

	first := base64.StdEncoding.EncodeToString([]byte(`{"title":"first"}`))
	second := base64.StdEncoding.EncodeToString([]byte(`{"title":"second"}`))
	fr.WriteToOutput = append([]byte("SPINDRIFT_ISSUE_INTENT "+d.nonce+" "+first+"\n"),
		append([]byte("SPINDRIFT_ISSUE_INTENT "+d.nonce+" "+second+"\n"),
			nonceLine(d, "SPINDRIFT_OUTCOME issue=1 landing=agent/issue-1 status=ready note=ok")...)...)

	result := d.Run()

	if !result.Resolved.Found {
		t.Fatal("want OutcomeFound=true")
	}
	if !result.IssueIntentsFound {
		t.Fatal("want IssueIntentsFound=true")
	}
	want := []string{`{"title":"first"}`, `{"title":"second"}`}
	if len(result.IssueIntents) != len(want) || result.IssueIntents[0] != want[0] || result.IssueIntents[1] != want[1] {
		t.Errorf("IssueIntents: got %v, want %v", result.IssueIntents, want)
	}
}

// Read-write and read-only runs emit no issue-intent signal, so false and nil
// must be the default shape (issue #2018's "no existing run path changes"
// acceptance criterion).
func TestDispatchWithRetry_NoIssueIntentLinesLeavesResultEmpty(t *testing.T) {
	fr := runner.NewFake()
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, drv, fakeClock(time.Time{}, &sleeps))
	fr.WriteToOutput = nonceLine(d, "SPINDRIFT_OUTCOME issue=1 landing=agent/issue-1 status=ready note=ok")

	result := d.Run()

	if !result.Resolved.Found {
		t.Fatal("want OutcomeFound=true")
	}
	if result.IssueIntentsFound {
		t.Fatal("want IssueIntentsFound=false")
	}
	if len(result.IssueIntents) != 0 {
		t.Errorf("IssueIntents: got %v, want empty", result.IssueIntents)
	}
}

// A nonce mismatch still counts on Result.IssueIntentsRejected, so a caller
// can log a warning that distinguishes no issue-intent signal at all from one
// that failed nonce verification (issue #2976).
func TestDispatchWithRetry_IssueIntentLineWithWrongNoncePopulatesRejectedCount(t *testing.T) {
	fr := runner.NewFake()
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, drv, fakeClock(time.Time{}, &sleeps))
	encoded := base64.StdEncoding.EncodeToString([]byte(`{"title":"evil"}`))
	fr.WriteToOutput = append([]byte("SPINDRIFT_ISSUE_INTENT not-this-runs-nonce "+encoded+"\n"),
		nonceLine(d, "SPINDRIFT_OUTCOME issue=1 landing=agent/issue-1 status=ready note=ok")...)

	result := d.Run()

	if !result.Resolved.Found {
		t.Fatal("want OutcomeFound=true")
	}
	if result.IssueIntentsFound {
		t.Fatal("want IssueIntentsFound=false for a nonce mismatch")
	}
	if result.IssueIntentsRejected != 1 {
		t.Errorf("IssueIntentsRejected: got %d, want 1", result.IssueIntentsRejected)
	}
}

func TestDispatchWithRetry_PRIntentLineWithWrongNonceNotFound(t *testing.T) {
	fr := runner.NewFake()
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, drv, fakeClock(time.Time{}, &sleeps))
	encoded := base64.StdEncoding.EncodeToString([]byte("evil title\n\nevil body"))
	fr.WriteToOutput = append([]byte("SPINDRIFT_PR_INTENT not-this-runs-nonce "+encoded+"\n"),
		nonceLine(d, "SPINDRIFT_OUTCOME issue=1 landing=agent/issue-1 status=ready note=ok")...)

	result := d.Run()

	if !result.Resolved.Found {
		t.Fatal("want OutcomeFound=true")
	}
	if result.PRIntentFound {
		t.Fatal("want PRIntentFound=false for a nonce mismatch")
	}
	if result.PRIntent != "" {
		t.Errorf("PRIntent: got %q, want empty", result.PRIntent)
	}
}

// A nonce mismatch still counts on Result.PRIntentRejected, so a caller can
// log a warning that distinguishes no PR-intent signal at all from one that
// failed nonce verification (issue #2976).
func TestDispatchWithRetry_PRIntentLineWithWrongNoncePopulatesRejectedCount(t *testing.T) {
	fr := runner.NewFake()
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, drv, fakeClock(time.Time{}, &sleeps))
	encoded := base64.StdEncoding.EncodeToString([]byte("evil title\n\nevil body"))
	fr.WriteToOutput = append([]byte("SPINDRIFT_PR_INTENT not-this-runs-nonce "+encoded+"\n"),
		nonceLine(d, "SPINDRIFT_OUTCOME issue=1 landing=agent/issue-1 status=ready note=ok")...)

	result := d.Run()

	if !result.Resolved.Found {
		t.Fatal("want OutcomeFound=true")
	}
	if result.PRIntentRejected != 1 {
		t.Errorf("PRIntentRejected: got %d, want 1", result.PRIntentRejected)
	}
}

// A zero-exit box that wrote no outcome line still gets a best-effort
// classification, so gateIssue-style callers can explain what happened
// without reading the log themselves.
func TestDispatchWithRetry_SuccessWithoutOutcomeClassifies(t *testing.T) {
	fr := runner.NewFake()
	wantCls := driver.Classification{Class: driver.Terminal, Reason: driver.TaskFailed}
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return wantCls, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, drv, fakeClock(time.Time{}, &sleeps))

	result := d.Run()

	if !result.Success {
		t.Error("want Success=true, got false")
	}
	if result.Resolved.Found {
		t.Fatal("want OutcomeFound=false")
	}
	if result.Classification != wantCls {
		t.Errorf("Classification: got %+v, want %+v", result.Classification, wantCls)
	}
}

func TestDispatchWithRetry_SuccessWithMalformedOutcomeSetsParseErr(t *testing.T) {
	fr := runner.NewFake()
	called := false
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		called = true
		return driver.Classification{}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, drv, fakeClock(time.Time{}, &sleeps))
	fr.WriteToOutput = nonceLine(d, "SPINDRIFT_OUTCOME issue=1") // missing landing= and status=, but carries the nonce

	result := d.Run()

	if !result.Success {
		t.Error("want Success=true, got false")
	}
	if result.ParseErr == nil {
		t.Fatal("want ParseErr set for an unparseable outcome line")
	}
	if result.Resolved.Found {
		t.Error("want OutcomeFound=false for an unparseable outcome line")
	}
	if called {
		t.Error("classify should not be called when the outcome line failed to parse")
	}
}

func TestDispatchWithRetry_TerminalNeverRetried(t *testing.T) {
	fr := runner.NewFake()
	fr.RunErr = boxErr
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Terminal, Reason: driver.TaskFailed}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, drv, fakeClock(time.Time{}, &sleeps))

	result := d.Run()

	if result.Success {
		t.Error("want Success=false (terminal failure), got true")
	}
	if len(fr.RunCalls) != 1 {
		t.Errorf("RunCalls: got %d, want 1 (no retry on terminal)", len(fr.RunCalls))
	}
	if len(sleeps) != 0 {
		t.Errorf("sleep calls: got %d, want 0 (no sleep on terminal)", len(sleeps))
	}
	// The fake box wrote nothing to its log, so this is the "box never
	// launched" case (issue #3119): the error once() returned must surface on
	// Result.Err.
	if !errors.Is(result.Err, boxErr) {
		t.Errorf("Err: got %v, want boxErr", result.Err)
	}
}

// A terminal failure whose box produced log output ran and genuinely failed,
// so Result.Err stays nil. Only a pre-launch failure with no log content
// surfaces there (issue #3119).
func TestDispatchWithRetry_TerminalWithNonEmptyLogLeavesErrNil(t *testing.T) {
	fr := runner.NewFake()
	fr.RunErr = boxErr
	fr.WriteToOutput = []byte("some box output before it failed\n")
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Terminal, Reason: driver.TaskFailed}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, drv, fakeClock(time.Time{}, &sleeps))

	result := d.Run()

	if result.Success {
		t.Error("want Success=false (terminal failure), got true")
	}
	if result.Err != nil {
		t.Errorf("Err: got %v, want nil (box ran and produced log output)", result.Err)
	}
}

func TestDispatchWithRetry_TerminalWithoutKillSignalLeavesKilledBySignalFalse(t *testing.T) {
	fr := runner.NewFake()
	fr.RunErr = boxErr
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Terminal, Reason: driver.TaskFailed}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, drv, fakeClock(time.Time{}, &sleeps))

	result := d.Run()

	if result.Success {
		t.Error("want Success=false (terminal failure), got true")
	}
	if result.KilledBySignal {
		t.Error("want KilledBySignal=false (plain error, not a signal kill), got true")
	}
}

// A terminal failure whose underlying error is a *runner.RunError with a
// signal-kill exit code sets Result.KilledBySignal (issue #2378).
func TestDispatchWithRetry_TerminalWithKillSignalSetsKilledBySignal(t *testing.T) {
	fr := runner.NewFake()
	fr.RunErr = &runner.RunError{ExitCode: 143}
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Terminal, Reason: driver.TaskFailed}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, drv, fakeClock(time.Time{}, &sleeps))

	result := d.Run()

	if result.Success {
		t.Error("want Success=false (terminal failure), got true")
	}
	if !result.KilledBySignal {
		t.Error("want KilledBySignal=true (RunError ExitCode=143, SIGTERM), got false")
	}
}

// The hold a 429 with resetsAt causes does not consume the retry cap when the
// re-dispatch succeeds.
func TestDispatchWithRetry_HoldThenSuccess(t *testing.T) {
	fixedNow := time.Unix(1_000_000, 0).UTC()
	resetAt := fixedNow.Add(2 * time.Hour)

	fr := runner.NewFake()
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.RateLimit, ResetAt: &resetAt}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, drv, fakeClock(fixedNow, &sleeps)) // holdJitter=0 for determinism
	writeOutcomeOnFinalCall(fr, []error{boxErr, nil}, nonceLine(d, "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok"))

	result := d.Run()

	if !result.Success {
		t.Error("want Success=true (success after hold), got false")
	}
	if len(fr.RunCalls) != 2 {
		t.Errorf("RunCalls: got %d, want 2 (initial + hold re-dispatch)", len(fr.RunCalls))
	}
	if len(sleeps) != 1 {
		t.Fatalf("sleep calls: got %d, want 1", len(sleeps))
	}
	wantSleep := 2 * time.Hour // resetAt - fixedNow, jitter=0
	if sleeps[0] != wantSleep {
		t.Errorf("sleep duration: got %v, want %v", sleeps[0], wantSleep)
	}
}

// Only the re-dispatch following a 429 hold carries RESUME_AFTER_HOLD=1, so
// the box resumes its pinned session instead of re-pinning.
func TestDispatchWithRetry_HoldReDispatchSetsResumeAfterHold(t *testing.T) {
	fixedNow := time.Unix(1_000_000, 0).UTC()
	resetAt := fixedNow.Add(2 * time.Hour)

	fr := runner.NewFake()
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.RateLimit, ResetAt: &resetAt}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, drv, fakeClock(fixedNow, &sleeps)) // holdJitter=0 for determinism
	writeOutcomeOnFinalCall(fr, []error{boxErr, nil}, nonceLine(d, "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok"))

	d.Run()

	if len(fr.RunCalls) != 2 {
		t.Fatalf("RunCalls: got %d, want 2 (initial + hold re-dispatch)", len(fr.RunCalls))
	}
	if _, ok := fr.RunCalls[0].Env["RESUME_AFTER_HOLD"]; ok {
		t.Errorf("initial dispatch env has RESUME_AFTER_HOLD set, want absent: %v", fr.RunCalls[0].Env)
	}
	if got := fr.RunCalls[1].Env["RESUME_AFTER_HOLD"]; got != "1" {
		t.Errorf("hold re-dispatch env RESUME_AFTER_HOLD: got %q, want \"1\"", got)
	}
}

// The held first attempt burns real tokens before dying, and that content
// must still reach AllAttemptLogPaths and CumulativeUsage after the resumed
// attempt runs: Run's !resumeAfterHold guard on quarantinePriorRunLogs
// (box.go) stops the second dispatch from renaming the first attempt's
// rotated .1 log out of scanning range (issue #2575 AC3/AC4).
func TestDispatchWithRetry_HoldResumeCountsBothAttemptsUsage(t *testing.T) {
	fixedNow := time.Unix(1_000_000, 0).UTC()
	resetAt := fixedNow.Add(2 * time.Hour)

	fr := runner.NewFake()
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.RateLimit, ResetAt: &resetAt}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, drv, fakeClock(fixedNow, &sleeps)) // holdJitter=0 for determinism

	firstAttemptResult := []byte(`{"type":"result","num_turns":1,"total_cost_usd":3.00,"usage":{"input_tokens":5000,"output_tokens":250}}` + "\n")
	secondAttemptOutcome := nonceLine(d, "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok")

	calls := 0
	fr.RunFunc = func(box runner.Box) error {
		i := calls
		calls++
		if i == 0 {
			// The rate limit killed the box before it could print a verdict,
			// so the hold path fires while the log still holds real usage
			// data.
			box.Output.Write(firstAttemptResult) //nolint:errcheck
			return boxErr
		}
		box.Output.Write(secondAttemptOutcome) //nolint:errcheck
		return nil
	}

	result := d.Run()

	if len(fr.RunCalls) != 2 {
		t.Fatalf("RunCalls: got %d, want 2 (initial + hold re-dispatch)", len(fr.RunCalls))
	}
	if !result.Success || !result.Resolved.Found {
		t.Fatalf("want a settled, successful outcome from the resumed attempt; got: %+v", result)
	}

	paths := AllAttemptLogPaths(d.pwd, d.number)
	if len(paths) != 2 {
		t.Fatalf("AllAttemptLogPaths: got %d entries, want 2 (the held first attempt's rotated .1 sibling plus the resumed second attempt's bare log): %+v", len(paths), paths)
	}

	got := d.CumulativeUsage()
	if diff := got.TotalCostUSD - 3.00; diff > 0.0001 || diff < -0.0001 {
		t.Errorf("TotalCostUSD = %v, want ~3.00 (the held first attempt's spend must still be counted, not lost to a wrongly re-fired quarantine)", got.TotalCostUSD)
	}
	if got.InputTokens != 5000 {
		t.Errorf("InputTokens = %d, want 5000 (the held first attempt's tokens must still be counted)", got.InputTokens)
	}
	if got.OutputTokens != 250 {
		t.Errorf("OutputTokens = %d, want 250 (the held first attempt's tokens must still be counted)", got.OutputTokens)
	}
}

// The re-dispatch following a 529 backoff transient, not just a 429 hold,
// also carries RESUME_AFTER_HOLD=1, so a cold restart on the backoff path
// does not re-pin --session-id on a possibly-existing session.
func TestDispatchWithRetry_TransientBackoffReDispatchSetsResumeAfterHold(t *testing.T) {
	fr := runner.NewFake()
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.Overloaded}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 10, 0), fr, drv, fakeClock(time.Time{}, &sleeps))
	writeOutcomeOnFinalCall(fr, []error{boxErr, nil}, nonceLine(d, "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok"))

	d.Run()

	if len(fr.RunCalls) != 2 {
		t.Fatalf("RunCalls: got %d, want 2 (initial + backoff re-dispatch)", len(fr.RunCalls))
	}
	if _, ok := fr.RunCalls[0].Env["RESUME_AFTER_HOLD"]; ok {
		t.Errorf("initial dispatch env has RESUME_AFTER_HOLD set, want absent: %v", fr.RunCalls[0].Env)
	}
	if got := fr.RunCalls[1].Env["RESUME_AFTER_HOLD"]; got != "1" {
		t.Errorf("backoff re-dispatch env RESUME_AFTER_HOLD: got %q, want \"1\"", got)
	}
}

// A box that prints a valid, nonce-bearing SPINDRIFT_OUTCOME but then exits
// non-zero settles on that printed outcome (issue #2075) instead of being
// reclassified into a hold or an agent-failed.
func TestDispatchWithRetry_NonZeroExitWithOutcomeSettles(t *testing.T) {
	fr := runner.NewFake()
	fr.RunErr = boxErr
	classified := false
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		classified = true
		return driver.Classification{Class: driver.Transient, Reason: driver.RateLimit}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, drv, fakeClock(time.Time{}, &sleeps))
	fr.WriteToOutput = nonceLine(d, "SPINDRIFT_OUTCOME issue=1 landing=agent/issue-1 status=ready note=done")

	result := d.Run()

	if !result.Resolved.Found {
		t.Fatal("want OutcomeFound=true (printed outcome settles despite non-zero exit)")
	}
	if !result.Success {
		t.Error("want Success=true so the wave engine routes to Settle, not FAILED")
	}
	if result.Resolved.Outcome.Status != "ready" {
		t.Errorf("Outcome.Status: got %q, want \"ready\"", result.Resolved.Outcome.Status)
	}
	if classified {
		t.Error("classify was called; want the printed outcome to settle before classification")
	}
	if len(fr.RunCalls) != 1 {
		t.Errorf("RunCalls: got %d, want 1 (no re-dispatch)", len(fr.RunCalls))
	}
	if len(sleeps) != 0 {
		t.Errorf("sleep calls: got %d, want 0 (no hold)", len(sleeps))
	}
}

func TestDispatchWithRetry_HoldJitterAdded(t *testing.T) {
	fixedNow := time.Unix(1_000_000, 0).UTC()
	resetAt := fixedNow.Add(1 * time.Hour)

	fr := runner.NewFake()
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.RateLimit, ResetAt: &resetAt}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 10), fr, drv, fakeClock(fixedNow, &sleeps))
	writeOutcomeOnFinalCall(fr, []error{boxErr, nil}, nonceLine(d, "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok"))

	d.Run()

	if len(sleeps) != 1 {
		t.Fatalf("sleep calls: got %d, want 1", len(sleeps))
	}
	wantSleep := 1*time.Hour + 10*time.Second
	if sleeps[0] != wantSleep {
		t.Errorf("sleep duration: got %v, want %v", sleeps[0], wantSleep)
	}
}

func TestDispatchWithRetry_ConsecutiveHoldsConsumeCapAndFail(t *testing.T) {
	fixedNow := time.Unix(1_000_000, 0).UTC()
	resetAt := fixedNow.Add(30 * time.Minute)

	fr := runner.NewFake()
	fr.RunErr = boxErr
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.RateLimit, ResetAt: &resetAt}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, drv, fakeClock(fixedNow, &sleeps))

	result := d.Run()

	if result.Success {
		t.Error("want Success=false (cap exhausted), got true")
	}
	// With max=3 the first hold is free and the next three count, so the
	// fourth 429 hits the cap before it sleeps: 4 runs, 3 sleeps.
	if len(fr.RunCalls) != 4 {
		t.Errorf("RunCalls: got %d, want 4", len(fr.RunCalls))
	}
	if len(sleeps) != 3 {
		t.Errorf("sleep calls: got %d, want 3 (one per hold before cap)", len(sleeps))
	}
}

// The "hold cap exhausted" status line routes through the same humanOut()
// sink as the dispatch-start announce line (issue #1829), so a Factory with
// its heartbeat sink discarded writes nothing to stdout (issue #1847).
func TestDispatchWithRetry_HoldCapExhaustedSuppressedWhenDiscardConfigured(t *testing.T) {
	fixedNow := time.Unix(1_000_000, 0).UTC()
	resetAt := fixedNow.Add(30 * time.Minute)

	fr := runner.NewFake()
	fr.RunErr = boxErr
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.RateLimit, ResetAt: &resetAt}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatchDiscard(t, retryConfig(3, 0, 0), fr, drv, fakeClock(fixedNow, &sleeps))

	var result Result
	out := testutil.CaptureStdout(t, func() { result = d.Run() })

	if result.Success {
		t.Error("want Success=false (cap exhausted), got true")
	}
	if strings.Contains(out, "hold cap exhausted") {
		t.Errorf("stdout should carry no hold-cap-exhausted status line when discarded, got %q", out)
	}
}

// The "rate limit; holding until" status line routes through humanOut(), so a
// Factory with its heartbeat sink discarded writes nothing to stdout (issue
// #1847).
func TestDispatchWithRetry_RateLimitHoldSuppressedWhenDiscardConfigured(t *testing.T) {
	fixedNow := time.Unix(1_000_000, 0).UTC()
	resetAt := fixedNow.Add(30 * time.Minute)

	fr := runner.NewFake()
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.RateLimit, ResetAt: &resetAt}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatchDiscard(t, retryConfig(3, 0, 0), fr, drv, fakeClock(fixedNow, &sleeps))
	writeOutcomeOnFinalCall(fr, []error{boxErr, nil}, nonceLine(d, "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok"))

	var result Result
	out := testutil.CaptureStdout(t, func() { result = d.Run() })

	if !result.Success {
		t.Error("want Success=true (succeeded after hold), got false")
	}
	if strings.Contains(out, "rate limit; holding") {
		t.Errorf("stdout should carry no rate-limit-hold status line when discarded, got %q", out)
	}
}

// With no heartbeat sink override, the hold and rate-limit status lines still
// reach stdout: the non-console CLI dispatch path (issue #1847, matching
// #1829's precedent for the announce line).
func TestDispatchWithRetry_ConsecutiveHoldsEmitToStdoutWithoutOverride(t *testing.T) {
	fixedNow := time.Unix(1_000_000, 0).UTC()
	resetAt := fixedNow.Add(30 * time.Minute)

	fr := runner.NewFake()
	fr.RunErr = boxErr
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.RateLimit, ResetAt: &resetAt}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, drv, fakeClock(fixedNow, &sleeps))

	var result Result
	out := testutil.CaptureStdout(t, func() { result = d.Run() })

	if result.Success {
		t.Error("want Success=false (cap exhausted), got true")
	}
	if !strings.Contains(out, "rate limit; holding until") {
		t.Errorf("stdout missing rate-limit-hold status line, got %q", out)
	}
	if !strings.Contains(out, "hold cap exhausted") {
		t.Errorf("stdout missing hold-cap-exhausted status line, got %q", out)
	}
}

// With no heartbeat sink override, the transient-backoff and
// transient-cap-exhausted status lines still reach stdout: the non-console
// CLI dispatch path (issue #1847).
func TestDispatchWithRetry_TransientRetriesEmitToStdoutWithoutOverride(t *testing.T) {
	fr := runner.NewFake()
	fr.RunErr = boxErr
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.Network}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(2, 5, 0), fr, drv, fakeClock(time.Time{}, &sleeps))

	var result Result
	out := testutil.CaptureStdout(t, func() { result = d.Run() })

	if result.Success {
		t.Error("want Success=false (cap exhausted), got true")
	}
	if !strings.Contains(out, "transient (network); retry") {
		t.Errorf("stdout missing transient-backoff status line, got %q", out)
	}
	if !strings.Contains(out, "transient retry cap exhausted") {
		t.Errorf("stdout missing transient-cap-exhausted status line, got %q", out)
	}
}

// holdCount resets after a non-429 outcome, so a hold, then a different
// transient, then a success does not accumulate cap from the first hold.
func TestDispatchWithRetry_HoldNotCountedAfterProgress(t *testing.T) {
	fixedNow := time.Unix(1_000_000, 0).UTC()
	resetAt := fixedNow.Add(30 * time.Minute)

	fr := runner.NewFake()

	rateLimitCls := driver.Classification{Class: driver.Transient, Reason: driver.RateLimit, ResetAt: &resetAt}
	overloadedCls := driver.Classification{Class: driver.Transient, Reason: driver.Overloaded}
	calls := 0
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		calls++
		if calls == 1 {
			return rateLimitCls, nil
		}
		return overloadedCls, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(1, 0, 0), fr, drv, fakeClock(fixedNow, &sleeps))
	writeOutcomeOnFinalCall(fr, []error{boxErr, boxErr, nil}, nonceLine(d, "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok"))

	result := d.Run()

	// Even with max=1 the sequence succeeds: run 1 holds on a 429 for free and
	// sets prevWasHold, run 2 is a 529 that resets prevWasHold and counts as
	// the first transient, and run 3 succeeds.
	if !result.Success {
		t.Error("want Success=true (succeeded after mixed transients), got false")
	}
	if len(fr.RunCalls) != 3 {
		t.Errorf("RunCalls: got %d, want 3", len(fr.RunCalls))
	}
}

func TestDispatchWithRetry_TransientBackoffRetryAndSucceed(t *testing.T) {
	fr := runner.NewFake()
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.Overloaded}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 10, 0), fr, drv, fakeClock(time.Time{}, &sleeps))
	writeOutcomeOnFinalCall(fr, []error{boxErr, nil}, nonceLine(d, "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok"))

	result := d.Run()

	if !result.Success {
		t.Error("want Success=true (success after backoff retry), got false")
	}
	if len(fr.RunCalls) != 2 {
		t.Errorf("RunCalls: got %d, want 2", len(fr.RunCalls))
	}
	if len(sleeps) != 1 {
		t.Fatalf("sleep calls: got %d, want 1", len(sleeps))
	}
	if sleeps[0] != 10*time.Second {
		t.Errorf("sleep duration: got %v, want %v", sleeps[0], 10*time.Second)
	}
}

// The "transient (...); retry" backoff status line routes through humanOut(),
// so a Factory with its heartbeat sink discarded writes nothing to stdout
// (issue #1847).
func TestDispatchWithRetry_TransientBackoffRetrySuppressedWhenDiscardConfigured(t *testing.T) {
	fr := runner.NewFake()
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.Overloaded}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatchDiscard(t, retryConfig(3, 10, 0), fr, drv, fakeClock(time.Time{}, &sleeps))
	writeOutcomeOnFinalCall(fr, []error{boxErr, nil}, nonceLine(d, "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok"))

	var result Result
	out := testutil.CaptureStdout(t, func() { result = d.Run() })

	if !result.Success {
		t.Error("want Success=true (success after backoff retry), got false")
	}
	if strings.Contains(out, "transient (overloaded); retry") {
		t.Errorf("stdout should carry no transient-backoff status line when discarded, got %q", out)
	}
}

func TestDispatchWithRetry_TransientCapExhausted(t *testing.T) {
	fr := runner.NewFake()
	fr.RunErr = boxErr
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.Network}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(2, 5, 0), fr, drv, fakeClock(time.Time{}, &sleeps))

	result := d.Run()

	if result.Success {
		t.Error("want Success=false (cap exhausted), got true")
	}
	// max=2: initial run + 2 retries = 3 total runs, 2 sleeps.
	if len(fr.RunCalls) != 3 {
		t.Errorf("RunCalls: got %d, want 3", len(fr.RunCalls))
	}
	if len(sleeps) != 2 {
		t.Fatalf("sleep calls: got %d, want 2", len(sleeps))
	}
	// The backoff is linear, so retry 1 sleeps 5s and retry 2 sleeps 10s.
	if sleeps[0] != 5*time.Second {
		t.Errorf("sleep[0]: got %v, want %v", sleeps[0], 5*time.Second)
	}
	if sleeps[1] != 10*time.Second {
		t.Errorf("sleep[1]: got %v, want %v", sleeps[1], 10*time.Second)
	}
	// The cap-exhaustion path prints its own "!!" status line, so Result.Err
	// must stay nil or a caller duplicates it (issue #3119).
	if result.Err != nil {
		t.Errorf("Err: got %v, want nil (cap-exhaustion path prints its own message)", result.Err)
	}
}

// The "transient retry cap exhausted" status line routes through humanOut(),
// so a Factory with its heartbeat sink discarded writes nothing to stdout
// (issue #1847).
func TestDispatchWithRetry_TransientCapExhaustedSuppressedWhenDiscardConfigured(t *testing.T) {
	fr := runner.NewFake()
	fr.RunErr = boxErr
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.Network}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatchDiscard(t, retryConfig(2, 5, 0), fr, drv, fakeClock(time.Time{}, &sleeps))

	var result Result
	out := testutil.CaptureStdout(t, func() { result = d.Run() })

	if result.Success {
		t.Error("want Success=false (cap exhausted), got true")
	}
	if strings.Contains(out, "transient retry cap exhausted") {
		t.Errorf("stdout should carry no transient-cap-exhausted status line when discarded, got %q", out)
	}
}

// Dispatch treats a 429 with no resetsAt as a plain transient: backoff retry,
// not hold.
func TestDispatchWithRetry_RateLimitWithoutResetAtUsesBackoff(t *testing.T) {
	fr := runner.NewFake()
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.RateLimit, ResetAt: nil}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 15, 0), fr, drv, fakeClock(time.Time{}, &sleeps))
	writeOutcomeOnFinalCall(fr, []error{boxErr, nil}, nonceLine(d, "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok"))

	result := d.Run()

	if !result.Success {
		t.Error("want Success=true (success after backoff for 429 without resetsAt), got false")
	}
	if len(sleeps) != 1 {
		t.Fatalf("sleep calls: got %d, want 1", len(sleeps))
	}
	if sleeps[0] != 15*time.Second {
		t.Errorf("sleep duration: got %v, want 15s (backoff, not hold)", sleeps[0])
	}
}

func TestDispatchWithRetry_HoldWithPastResetUsesJitterOnly(t *testing.T) {
	fixedNow := time.Unix(2_000_000, 0).UTC()
	resetAt := fixedNow.Add(-1 * time.Hour)

	fr := runner.NewFake()
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.RateLimit, ResetAt: &resetAt}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 7), fr, drv, fakeClock(fixedNow, &sleeps))
	writeOutcomeOnFinalCall(fr, []error{boxErr, nil}, nonceLine(d, "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok"))

	d.Run()

	if len(sleeps) != 1 {
		t.Fatalf("sleep calls: got %d, want 1", len(sleeps))
	}
	if sleeps[0] != 7*time.Second {
		t.Errorf("sleep duration: got %v, want 7s (clamped to jitter)", sleeps[0])
	}
}

// Dispatch holds and re-dispatches a box that exits zero and writes no
// SPINDRIFT_OUTCOME line when its log classifies as a rate limit with a known
// resetsAt, the same as a non-zero 429 exit, instead of dead-ending as
// status=missing (issue #565).
func TestDispatchWithRetry_ZeroExitRateLimitHoldsAndRedispatches(t *testing.T) {
	fixedNow := time.Unix(1_000_000, 0).UTC()
	resetAt := fixedNow.Add(2 * time.Hour)

	fr := runner.NewFake()
	calls := 0
	fr.RunFunc = func(box runner.Box) error {
		calls++
		if calls == 2 && box.Output != nil {
			box.Output.Write([]byte("SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok nonce=" + box.Env["RUN_NONCE"] + "\n")) //nolint:errcheck
		}
		return nil
	}
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.RateLimit, ResetAt: &resetAt}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, drv, fakeClock(fixedNow, &sleeps))

	result := d.Run()

	if !result.Success {
		t.Error("want Success=true, got false")
	}
	if !result.Resolved.Found {
		t.Fatal("want OutcomeFound=true after hold + re-dispatch")
	}
	if calls != 2 {
		t.Errorf("Run calls: got %d, want 2 (initial zero-exit + hold re-dispatch)", calls)
	}
	if len(sleeps) != 1 {
		t.Fatalf("sleep calls: got %d, want 1", len(sleeps))
	}
	wantSleep := 2 * time.Hour
	if sleeps[0] != wantSleep {
		t.Errorf("sleep duration: got %v, want %v", sleeps[0], wantSleep)
	}
}

// A zero-exit, no-outcome run whose log carries a transient marker but no
// resetsAt follows the backoff-retry path rather than an indefinite hold or
// an immediate status=missing (issue #565's third acceptance criterion).
func TestDispatchWithRetry_ZeroExitTransientWithoutResetAtUsesBackoff(t *testing.T) {
	fr := runner.NewFake()
	calls := 0
	fr.RunFunc = func(box runner.Box) error {
		calls++
		if calls == 2 && box.Output != nil {
			box.Output.Write([]byte("SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok nonce=" + box.Env["RUN_NONCE"] + "\n")) //nolint:errcheck
		}
		return nil
	}
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.Overloaded}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 15, 0), fr, drv, fakeClock(time.Time{}, &sleeps))

	result := d.Run()

	if !result.Success {
		t.Error("want Success=true, got false")
	}
	if !result.Resolved.Found {
		t.Fatal("want OutcomeFound=true after backoff + re-dispatch")
	}
	if calls != 2 {
		t.Errorf("Run calls: got %d, want 2 (initial zero-exit + backoff re-dispatch)", calls)
	}
	if len(sleeps) != 1 {
		t.Fatalf("sleep calls: got %d, want 1", len(sleeps))
	}
	if sleeps[0] != 15*time.Second {
		t.Errorf("sleep duration: got %v, want 15s (backoff, not hold)", sleeps[0])
	}
}

// Consecutive zero-exit rate-limit holds that never recover count against the
// transient retry cap and land on Success=false rather than a silent or
// confusing status=missing (issue #565's second acceptance criterion).
func TestDispatchWithRetry_ZeroExitConsecutiveHoldsConsumeCapAndFail(t *testing.T) {
	fixedNow := time.Unix(1_000_000, 0).UTC()
	resetAt := fixedNow.Add(30 * time.Minute)

	fr := runner.NewFake() // always exits zero, never writes an outcome line
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.RateLimit, ResetAt: &resetAt}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, drv, fakeClock(fixedNow, &sleeps))

	result := d.Run()

	if result.Success {
		t.Error("want Success=false (cap exhausted), got true")
	}
	if len(fr.RunCalls) != 4 {
		t.Errorf("RunCalls: got %d, want 4", len(fr.RunCalls))
	}
	if len(sleeps) != 3 {
		t.Errorf("sleep calls: got %d, want 3 (one per hold before cap)", len(sleeps))
	}
}

// Issue #565's safety guard: Dispatch does not re-dispatch a zero-exit,
// no-outcome box that classifies as transient when OpenPRForIssue reports an
// open PR for the branch, because the box's work already landed and a retry
// would duplicate it. The Result passes through unchanged so settle's own PR
// lookup routes it.
func TestDispatchWithRetry_ZeroExitTransientSkipsRetryWhenPRExists(t *testing.T) {
	fixedNow := time.Unix(1_000_000, 0).UTC()
	resetAt := fixedNow.Add(2 * time.Hour)

	fr := runner.NewFake() // always exits zero, never writes an outcome line
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.RateLimit, ResetAt: &resetAt}, nil
	}}
	var sleeps []time.Duration
	cfg := retryConfig(3, 0, 0)
	cfg.OpenPRForIssue = func(string) (bool, error) { return true, nil }
	d := newTestDispatch(t, cfg, fr, drv, fakeClock(fixedNow, &sleeps))

	result := d.Run()

	if !result.Success {
		t.Error("want Success=true (zero exit passthrough), got false")
	}
	if result.Resolved.Found {
		t.Error("want OutcomeFound=false")
	}
	if result.Classification.Reason != driver.RateLimit {
		t.Errorf("Classification: got %+v, want RateLimit passthrough", result.Classification)
	}
	if len(fr.RunCalls) != 1 {
		t.Errorf("RunCalls: got %d, want 1 (no re-dispatch when a PR already exists)", len(fr.RunCalls))
	}
	if len(sleeps) != 0 {
		t.Errorf("sleep calls: got %d, want 0 (no hold when a PR already exists)", len(sleeps))
	}
}

// A 429 during a fix pass holds until reset instead of burning a fix attempt,
// because the retry policy applies to Fix as it does to Run (issue #441).
func TestDispatchWithRetry_AppliesToFixToo(t *testing.T) {
	fixedNow := time.Unix(1_000_000, 0).UTC()
	resetAt := fixedNow.Add(1 * time.Hour)

	fr := runner.NewFake()
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.RateLimit, ResetAt: &resetAt}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, drv, fakeClock(fixedNow, &sleeps))
	writeOutcomeOnFinalCall(fr, []error{boxErr, nil}, nonceLine(d, "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok"))

	result := d.Fix(1, "ci failure detail")

	if !result.Success {
		t.Error("want Success=true (fix succeeded after hold), got false")
	}
	if len(fr.RunCalls) != 2 {
		t.Errorf("RunCalls: got %d, want 2 (initial fix attempt + hold re-dispatch)", len(fr.RunCalls))
	}
	if len(sleeps) != 1 {
		t.Fatalf("sleep calls: got %d, want 1 (held instead of burning the fix attempt)", len(sleeps))
	}
	if sleeps[0] != 1*time.Hour {
		t.Errorf("sleep duration: got %v, want 1h (hold until reset)", sleeps[0])
	}
}

// When the box's outbox holds a manifest.json written by a prior orchestrator
// run, a successful dispatch parses it into Result.Passes (issue #2983).
func TestDispatchWithRetry_ParsesPassManifestFromOutbox(t *testing.T) {
	fr := runner.NewFake()
	drv := fakeDriver{}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, drv, fakeClock(time.Time{}, &sleeps))
	fr.WriteToOutput = nonceLine(d, "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok")

	want := []passmanifest.Entry{
		{Pass: 1, Kind: "implement", OutcomeFound: false},
		{Pass: 2, Kind: "land", OutcomeFound: true},
	}
	manifestPath := filepath.Join(OutboxDirFor(d.pwd, d.number), "manifest.json")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		t.Fatal(err)
	}
	passmanifest.Write(manifestPath, want)

	result := d.Run()

	if !result.Success || !result.Resolved.Found {
		t.Fatalf("want a successful, found outcome; got: %+v", result)
	}
	if !reflect.DeepEqual(result.Passes, want) {
		t.Errorf("Passes: got %+v, want %+v", result.Passes, want)
	}
}

// The pass-blind degrade contract (issue #2983 AC2): when no manifest.json
// lands in the outbox, the ordinary case for every other test here,
// Result.Passes is nil and every other field behaves as it did before Passes
// existed.
func TestDispatchWithRetry_MissingPassManifestDegradesToNil(t *testing.T) {
	fr := runner.NewFake()
	drv := fakeDriver{}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, drv, fakeClock(time.Time{}, &sleeps))
	fr.WriteToOutput = nonceLine(d, "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok")

	result := d.Run()

	if !result.Success || !result.Resolved.Found {
		t.Fatalf("want a successful, found outcome; got: %+v", result)
	}
	if len(result.Passes) != 0 {
		t.Errorf("Passes: got %+v, want nil/empty (no manifest ever written)", result.Passes)
	}
	if result.ParseErr != nil {
		t.Errorf("ParseErr: got %v, want nil (a missing manifest must never surface as a parse error)", result.ParseErr)
	}
}
