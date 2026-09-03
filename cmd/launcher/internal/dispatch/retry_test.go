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

// retryConfig returns a Config with retry knobs and a default
// OpenPRForIssue set explicitly.
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

// fakeClock returns a Clock with a fixed Now and a Sleep that records
// durations into calls.
func fakeClock(now time.Time, calls *[]time.Duration) Clock {
	return Clock{
		Now:   func() time.Time { return now },
		Sleep: func(d time.Duration) { *calls = append(*calls, d) },
	}
}

// newTestDispatch builds a Dispatch wired to fr and drv with the given retry
// config and clock, without going through a Factory (so tests can inject a
// fake Clock, which Factory's constructor doesn't expose a seam for
// bypassing the real cache).
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

// newTestDispatchDiscard is newTestDispatch with the Factory's heartbeat
// sink set to io.Discard before New(), mirroring the console entry point
// (issue #1583) so tests can assert retry/hold status lines are suppressed
// from stdout the same way dispatch-start announce lines are (issue #1829).
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

// TestDispatchWithRetry_SuccessOnFirstRun verifies that a successful run
// whose box reports an outcome line returns it without any classify or
// sleep calls.
func TestDispatchWithRetry_SuccessOnFirstRun(t *testing.T) {
	fr := runner.NewFake() // RunErr = nil → success
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

// TestDispatchWithRetry_SuccessWithCommentLinePopulatesResult verifies that a
// single-line, nonce-guarded SPINDRIFT_COMMENT alongside the outcome line in
// the box's log surfaces on Result.Comment/CommentFound — the host-mediated
// write channel for a local Dispatch's verdict/blocked comment (ADR 0032,
// issue #1692), now carried as one nonce-bearing base64 line instead of a
// multi-line block (issue #1940).
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

// TestDispatchWithRetry_CommentLineWithWrongNonceNotFound verifies that a
// SPINDRIFT_COMMENT line carrying a nonce that doesn't match this run's own
// is ignored — never surfaced on Result.Comment — the same guarantee
// LastCommentLineInLog documents (issue #1940).
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

// TestDispatchWithRetry_CommentLineWithWrongNoncePopulatesRejectedCount
// verifies that a SPINDRIFT_COMMENT line carrying a nonce that doesn't
// match this run's own -- while never surfacing on Result.Comment -- still
// counts on Result.CommentRejected, so a caller can settle-log a warning
// distinguishing "no comment signal at all" from "a comment signal was
// present but failed nonce verification" (issue #2976).
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

// TestDispatchWithRetry_SuccessWithPRIntentLinePopulatesResult verifies that
// a single-line, nonce-guarded SPINDRIFT_PR_INTENT control signal alongside
// the outcome line in the box's log surfaces on Result.PRIntent/PRIntentFound
// — the host-mediated draft-PR-create channel a read-only github Box uses in
// place of its own `gh pr create` (issue #1919), now carried as one
// base64-encoded line rather than a multi-line block (issue #1938).
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

// TestDispatchWithRetry_SelfReportSurvivesSyntheticBackstop verifies that the
// driver's own genuine near-miss self-report ("SPINDRIFT_OUTCOME: success",
// no nonce, paraphrasing the grammar) survives on Result.SelfReport even
// though a synthetic backstop line (ADR 0036) appended after it wins the
// authoritative Result.Outcome via last-line-wins (issue #2223/#2224). The
// backstop line carries this run's own nonce so LastInLog accepts it as
// authoritative.
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

// TestDispatchOutcomeResult_SelfReportErrorLoggedToStderr verifies that
// outcomeResult surfaces resolved.SelfReportError (issue #2343 slice 1's
// previously-swallowed self-report I/O error) to stderr with the
// "self-report scan" message — restoring, on the live dispatch path, the
// exact operator-visible warning the pre-refactor code printed. A single log
// file can't make the genuine/synthetic tier succeed while the self-report
// tier independently hits an I/O error (same file, same read shape), so this
// exercises outcomeResult directly with an injected Resolved rather than
// driving it through d.Run().
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

// TestDispatchWithRetry_SuccessWithIssueIntentLinesPopulatesResult verifies
// that multiple single-line, nonce-guarded SPINDRIFT_ISSUE_INTENT control
// signals alongside the outcome line in the box's log all surface on
// Result.IssueIntents/IssueIntentsFound — the host-mediated issue-filing
// relay channel (issue #2018). Unlike PR-intent/comment, this is 1-to-many:
// every verifying line contributes its own payload.
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

// TestDispatchWithRetry_NoIssueIntentLinesLeavesResultEmpty verifies a run
// with no SPINDRIFT_ISSUE_INTENT line at all leaves IssueIntentsFound false
// and IssueIntents nil — read-write/read-only runs today emit no such
// signal, so this must be the default shape (issue #2018's "no existing run
// path changes" acceptance criterion).
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

// TestDispatchWithRetry_IssueIntentLineWithWrongNoncePopulatesRejectedCount
// verifies that a SPINDRIFT_ISSUE_INTENT line carrying a nonce that doesn't
// match this run's own -- while never surfacing on Result.IssueIntents/
// IssueIntentsFound -- still counts on Result.IssueIntentsRejected, so a
// caller can settle-log a warning distinguishing "no issue-intent signal at
// all" from "an issue-intent signal was present but failed nonce
// verification" (issue #2976).
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

// TestDispatchWithRetry_PRIntentLineWithWrongNonceNotFound verifies that a
// SPINDRIFT_PR_INTENT line carrying a nonce that doesn't match this run's
// own is ignored — never surfaced on Result.PRIntent — mirroring
// TestDispatchWithRetry_CommentLineWithWrongNonceNotFound for the PR-intent
// signal.
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

// TestDispatchWithRetry_PRIntentLineWithWrongNoncePopulatesRejectedCount
// verifies that a SPINDRIFT_PR_INTENT line carrying a nonce that doesn't
// match this run's own -- while never surfacing on Result.PRIntent -- still
// counts on Result.PRIntentRejected, so a caller can settle-log a warning
// distinguishing "no PR-intent signal at all" from "a PR-intent signal was
// present but failed nonce verification" (issue #2976).
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

// TestDispatchWithRetry_SuccessWithoutOutcomeClassifies verifies that a
// zero-exit box that wrote no outcome line still gets a best-effort
// classification, so gateIssue-style callers can explain what happened
// without touching the log themselves.
func TestDispatchWithRetry_SuccessWithoutOutcomeClassifies(t *testing.T) {
	fr := runner.NewFake() // RunErr = nil → success, no outcome line written
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

// TestDispatchWithRetry_SuccessWithMalformedOutcomeSetsParseErr verifies
// that a zero-exit box whose log has an unparseable SPINDRIFT_OUTCOME line
// (missing required fields) surfaces ParseErr without attempting
// classification.
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

// TestDispatchWithRetry_TerminalNeverRetried verifies that a terminal
// failure exits after one attempt without retrying.
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
	// The fake box wrote nothing to its log (no WriteToOutput set), so this
	// is the "box never launched" case (issue #3119): the error once()
	// returned must surface on Result.Err.
	if !errors.Is(result.Err, boxErr) {
		t.Errorf("Err: got %v, want boxErr", result.Err)
	}
}

// TestDispatchWithRetry_TerminalWithNonEmptyLogLeavesErrNil verifies that a
// terminal failure whose box actually produced log output (it ran and
// genuinely failed, as opposed to never launching) leaves Result.Err nil --
// only a pre-launch failure with no log content is surfaced this way (issue
// #3119).
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

// TestDispatchWithRetry_TerminalWithoutKillSignalLeavesKilledBySignalFalse
// verifies that a terminal failure from an ordinary (non-signal) error, such
// as TerminalNeverRetried's plain boxErr, leaves KilledBySignal false.
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

// TestDispatchWithRetry_TerminalWithKillSignalSetsKilledBySignal verifies
// that a terminal failure whose underlying error is a *runner.RunError with
// a signal-kill exit code (issue #2378) sets Result.KilledBySignal.
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

// TestDispatchWithRetry_HoldThenSuccess verifies that a 429 with resetsAt
// causes a hold sleep and re-dispatch, and that the hold does not consume
// the retry cap when the re-dispatch succeeds.
func TestDispatchWithRetry_HoldThenSuccess(t *testing.T) {
	fixedNow := time.Unix(1_000_000, 0).UTC()
	resetAt := fixedNow.Add(2 * time.Hour)

	fr := runner.NewFake()
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.RateLimit, ResetAt: &resetAt}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, drv, fakeClock(fixedNow, &sleeps))                                                                    // holdJitter=0 for determinism
	writeOutcomeOnFinalCall(fr, []error{boxErr, nil}, nonceLine(d, "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok")) // first fails with no outcome, second succeeds

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

// TestDispatchWithRetry_HoldReDispatchSetsResumeAfterHold verifies that the
// re-dispatch following a 429 hold carries RESUME_AFTER_HOLD=1 in the box
// env, while the initial dispatch does not, so the box resumes its pinned
// session instead of re-pinning after a hold.
func TestDispatchWithRetry_HoldReDispatchSetsResumeAfterHold(t *testing.T) {
	fixedNow := time.Unix(1_000_000, 0).UTC()
	resetAt := fixedNow.Add(2 * time.Hour)

	fr := runner.NewFake()
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.RateLimit, ResetAt: &resetAt}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, drv, fakeClock(fixedNow, &sleeps))                                                                    // holdJitter=0 for determinism
	writeOutcomeOnFinalCall(fr, []error{boxErr, nil}, nonceLine(d, "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok")) // first fails with no outcome, second succeeds

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

// TestDispatchWithRetry_HoldResumeCountsBothAttemptsUsage drives a real
// Run() through an actual hold-then-resume cycle (like
// TestDispatchWithRetry_HoldReDispatchSetsResumeAfterHold above), except the
// held first attempt itself writes genuine, non-empty result-bearing content
// before dying with no parseable outcome -- mirroring a box that burned real
// tokens before hitting the rate limit. It then verifies that content is
// still visible to AllAttemptLogPaths/CumulativeUsage after Run() returns,
// proving Run's `!resumeAfterHold` guard on quarantinePriorRunLogs (box.go)
// held: the resumed second attempt's own dispatch must NOT re-fire
// quarantine and rename the first attempt's just-rotated .1 sibling log out
// of scanning range, which would silently drop it from both the run-usage
// comment and the self-heal budget gate (issue #2575 AC3/AC4).
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
			// First (held) attempt: writes real result-bearing content, so
			// it has genuine, non-empty usage data, but exits non-zero with
			// no parseable outcome -- the rate limit killed the box before
			// it could print a verdict -- so the hold path fires.
			box.Output.Write(firstAttemptResult) //nolint:errcheck
			return boxErr
		}
		// Second (resumed) attempt: succeeds with a genuine, nonce-bearing
		// outcome.
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

// TestDispatchWithRetry_TransientBackoffReDispatchSetsResumeAfterHold verifies
// that the re-dispatch following a 529/backoff transient (not a 429 hold)
// ALSO carries RESUME_AFTER_HOLD=1 in the box env, so a cold restart on the
// backoff path doesn't re-pin --session-id on a possibly-existing session.
func TestDispatchWithRetry_TransientBackoffReDispatchSetsResumeAfterHold(t *testing.T) {
	fr := runner.NewFake()
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.Overloaded}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 10, 0), fr, drv, fakeClock(time.Time{}, &sleeps))                                                                // backoffSecs=10
	writeOutcomeOnFinalCall(fr, []error{boxErr, nil}, nonceLine(d, "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok")) // first fails (529, no outcome), second succeeds

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

// TestDispatchWithRetry_NonZeroExitWithOutcomeSettles verifies that a box
// which prints a valid, nonce-bearing SPINDRIFT_OUTCOME but then exits
// non-zero settles on that printed outcome (issue #2075) rather than being
// reclassified into a hold or an agent-failed: the run is returned with
// Success and OutcomeFound true, with no classify, no sleep, and no
// re-dispatch.
func TestDispatchWithRetry_NonZeroExitWithOutcomeSettles(t *testing.T) {
	fr := runner.NewFake()
	fr.RunErr = boxErr // every run exits non-zero
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

// TestDispatchWithRetry_HoldJitterAdded verifies that Policy.Jitter is
// added to the hold sleep duration.
func TestDispatchWithRetry_HoldJitterAdded(t *testing.T) {
	fixedNow := time.Unix(1_000_000, 0).UTC()
	resetAt := fixedNow.Add(1 * time.Hour)

	fr := runner.NewFake()
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.RateLimit, ResetAt: &resetAt}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 10), fr, drv, fakeClock(fixedNow, &sleeps)) // holdJitter=10s
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

// TestDispatchWithRetry_ConsecutiveHoldsConsumeCapAndFail verifies that a
// series of consecutive 429s without progress eventually exhausts the hold
// cap and returns Success=false.
func TestDispatchWithRetry_ConsecutiveHoldsConsumeCapAndFail(t *testing.T) {
	fixedNow := time.Unix(1_000_000, 0).UTC()
	resetAt := fixedNow.Add(30 * time.Minute)

	fr := runner.NewFake()
	fr.RunErr = boxErr // all runs fail
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.RateLimit, ResetAt: &resetAt}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, drv, fakeClock(fixedNow, &sleeps)) // max=3

	result := d.Run()

	if result.Success {
		t.Error("want Success=false (cap exhausted), got true")
	}
	// With max=3: run1→429(free), run2→429(count=1), run3→429(count=2),
	// run4→429(count=3 >= 3) → fail before 4th sleep.
	// Total runs: 4, total sleeps: 3.
	if len(fr.RunCalls) != 4 {
		t.Errorf("RunCalls: got %d, want 4", len(fr.RunCalls))
	}
	if len(sleeps) != 3 {
		t.Errorf("sleep calls: got %d, want 3 (one per hold before cap)", len(sleeps))
	}
}

// TestDispatchWithRetry_HoldCapExhaustedSuppressedWhenDiscardConfigured
// verifies that the "hold cap exhausted" status line (retry.go) routes
// through the same humanOut() sink as the dispatch-start announce line
// (issue #1829): a Factory with its heartbeat sink discarded (the console
// entry point) writes no hold-cap status to stdout (issue #1847).
func TestDispatchWithRetry_HoldCapExhaustedSuppressedWhenDiscardConfigured(t *testing.T) {
	fixedNow := time.Unix(1_000_000, 0).UTC()
	resetAt := fixedNow.Add(30 * time.Minute)

	fr := runner.NewFake()
	fr.RunErr = boxErr // all runs fail
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.RateLimit, ResetAt: &resetAt}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatchDiscard(t, retryConfig(3, 0, 0), fr, drv, fakeClock(fixedNow, &sleeps)) // max=3

	var result Result
	out := testutil.CaptureStdout(t, func() { result = d.Run() })

	if result.Success {
		t.Error("want Success=false (cap exhausted), got true")
	}
	if strings.Contains(out, "hold cap exhausted") {
		t.Errorf("stdout should carry no hold-cap-exhausted status line when discarded, got %q", out)
	}
}

// TestDispatchWithRetry_RateLimitHoldSuppressedWhenDiscardConfigured
// verifies that the "rate limit; holding until" status line (retry.go)
// routes through humanOut(): a Factory with its heartbeat sink discarded
// writes no rate-limit-hold status to stdout (issue #1847).
func TestDispatchWithRetry_RateLimitHoldSuppressedWhenDiscardConfigured(t *testing.T) {
	fixedNow := time.Unix(1_000_000, 0).UTC()
	resetAt := fixedNow.Add(30 * time.Minute)

	fr := runner.NewFake()
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.RateLimit, ResetAt: &resetAt}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatchDiscard(t, retryConfig(3, 0, 0), fr, drv, fakeClock(fixedNow, &sleeps))
	writeOutcomeOnFinalCall(fr, []error{boxErr, nil}, nonceLine(d, "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok")) // hold once (no outcome), then succeed

	var result Result
	out := testutil.CaptureStdout(t, func() { result = d.Run() })

	if !result.Success {
		t.Error("want Success=true (succeeded after hold), got false")
	}
	if strings.Contains(out, "rate limit; holding") {
		t.Errorf("stdout should carry no rate-limit-hold status line when discarded, got %q", out)
	}
}

// TestDispatchWithRetry_ConsecutiveHoldsEmitToStdoutWithoutOverride verifies
// that the hold/rate-limit status lines still reach stdout unchanged when no
// heartbeat sink override is configured -- the non-console CLI dispatch path
// (issue #1847, matching #1829's precedent for the announce line).
func TestDispatchWithRetry_ConsecutiveHoldsEmitToStdoutWithoutOverride(t *testing.T) {
	fixedNow := time.Unix(1_000_000, 0).UTC()
	resetAt := fixedNow.Add(30 * time.Minute)

	fr := runner.NewFake()
	fr.RunErr = boxErr // all runs fail
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.RateLimit, ResetAt: &resetAt}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, drv, fakeClock(fixedNow, &sleeps)) // max=3

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

// TestDispatchWithRetry_TransientRetriesEmitToStdoutWithoutOverride verifies
// that the transient-backoff and transient-cap-exhausted status lines still
// reach stdout unchanged when no heartbeat sink override is configured --
// the non-console CLI dispatch path (issue #1847).
func TestDispatchWithRetry_TransientRetriesEmitToStdoutWithoutOverride(t *testing.T) {
	fr := runner.NewFake()
	fr.RunErr = boxErr // all runs fail
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.Network}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(2, 5, 0), fr, drv, fakeClock(time.Time{}, &sleeps)) // max=2

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

// TestDispatchWithRetry_HoldNotCountedAfterProgress verifies that holdCount
// resets after a non-429 outcome: a hold-then-different-transient-then-
// success sequence does not accumulate cap from the first hold.
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
	d := newTestDispatch(t, retryConfig(1, 0, 0), fr, drv, fakeClock(fixedNow, &sleeps)) // tight cap
	// run1 fails (429, no outcome), run2 fails (529 — different class, no outcome), run3 succeeds
	writeOutcomeOnFinalCall(fr, []error{boxErr, boxErr, nil}, nonceLine(d, "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok"))

	result := d.Run()

	// Even with max=1, the sequence succeeds because:
	// - run1 → 429 hold (free, prevWasHold=true)
	// - run2 → 529 (prevWasHold reset to false, transientCount=1 ≤ 1)
	// - run3 → success
	if !result.Success {
		t.Error("want Success=true (succeeded after mixed transients), got false")
	}
	if len(fr.RunCalls) != 3 {
		t.Errorf("RunCalls: got %d, want 3", len(fr.RunCalls))
	}
}

// TestDispatchWithRetry_TransientBackoffRetryAndSucceed verifies that a
// 529/network transient is retried with backoff and succeeds on
// re-dispatch.
func TestDispatchWithRetry_TransientBackoffRetryAndSucceed(t *testing.T) {
	fr := runner.NewFake()
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.Overloaded}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 10, 0), fr, drv, fakeClock(time.Time{}, &sleeps))                                                                // backoffSecs=10
	writeOutcomeOnFinalCall(fr, []error{boxErr, nil}, nonceLine(d, "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok")) // first fails (529, no outcome), second succeeds

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

// TestDispatchWithRetry_TransientBackoffRetrySuppressedWhenDiscardConfigured
// verifies that the "transient (...); retry" backoff status line
// (retry.go) routes through humanOut(): a Factory with its heartbeat sink
// discarded writes no transient-backoff status to stdout (issue #1847).
func TestDispatchWithRetry_TransientBackoffRetrySuppressedWhenDiscardConfigured(t *testing.T) {
	fr := runner.NewFake()
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.Overloaded}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatchDiscard(t, retryConfig(3, 10, 0), fr, drv, fakeClock(time.Time{}, &sleeps))
	writeOutcomeOnFinalCall(fr, []error{boxErr, nil}, nonceLine(d, "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok")) // first fails (529, no outcome), second succeeds

	var result Result
	out := testutil.CaptureStdout(t, func() { result = d.Run() })

	if !result.Success {
		t.Error("want Success=true (success after backoff retry), got false")
	}
	if strings.Contains(out, "transient (overloaded); retry") {
		t.Errorf("stdout should carry no transient-backoff status line when discarded, got %q", out)
	}
}

// TestDispatchWithRetry_TransientCapExhausted verifies that a 529/network
// transient that never recovers exhausts the cap and returns Success=false.
func TestDispatchWithRetry_TransientCapExhausted(t *testing.T) {
	fr := runner.NewFake()
	fr.RunErr = boxErr // all runs fail
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.Network}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(2, 5, 0), fr, drv, fakeClock(time.Time{}, &sleeps)) // max=2, backoffSecs=5

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
	// Linear backoff: retry1 = 5s*1, retry2 = 5s*2
	if sleeps[0] != 5*time.Second {
		t.Errorf("sleep[0]: got %v, want %v", sleeps[0], 5*time.Second)
	}
	if sleeps[1] != 10*time.Second {
		t.Errorf("sleep[1]: got %v, want %v", sleeps[1], 10*time.Second)
	}
	// The cap-exhaustion path already prints its own "!!" status line
	// (retry.go); Result.Err must stay nil so a caller doesn't duplicate it
	// (issue #3119).
	if result.Err != nil {
		t.Errorf("Err: got %v, want nil (cap-exhaustion path prints its own message)", result.Err)
	}
}

// TestDispatchWithRetry_TransientCapExhaustedSuppressedWhenDiscardConfigured
// verifies that the "transient retry cap exhausted" status line (retry.go)
// routes through humanOut(): a Factory with its heartbeat sink discarded
// writes no transient-cap status to stdout (issue #1847).
func TestDispatchWithRetry_TransientCapExhaustedSuppressedWhenDiscardConfigured(t *testing.T) {
	fr := runner.NewFake()
	fr.RunErr = boxErr // all runs fail
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.Network}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatchDiscard(t, retryConfig(2, 5, 0), fr, drv, fakeClock(time.Time{}, &sleeps)) // max=2

	var result Result
	out := testutil.CaptureStdout(t, func() { result = d.Run() })

	if result.Success {
		t.Error("want Success=false (cap exhausted), got true")
	}
	if strings.Contains(out, "transient retry cap exhausted") {
		t.Errorf("stdout should carry no transient-cap-exhausted status line when discarded, got %q", out)
	}
}

// TestDispatchWithRetry_RateLimitWithoutResetAtUsesBackoff verifies that a
// 429 with no resetsAt is treated as a plain transient (backoff retry, not
// hold).
func TestDispatchWithRetry_RateLimitWithoutResetAtUsesBackoff(t *testing.T) {
	fr := runner.NewFake()
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.RateLimit, ResetAt: nil}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 15, 0), fr, drv, fakeClock(time.Time{}, &sleeps)) // backoffSecs=15
	writeOutcomeOnFinalCall(fr, []error{boxErr, nil}, nonceLine(d, "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok"))

	result := d.Run()

	if !result.Success {
		t.Error("want Success=true (success after backoff for 429 without resetsAt), got false")
	}
	if len(sleeps) != 1 {
		t.Fatalf("sleep calls: got %d, want 1", len(sleeps))
	}
	// Should use backoff, not hold: 15s * 1
	if sleeps[0] != 15*time.Second {
		t.Errorf("sleep duration: got %v, want 15s (backoff, not hold)", sleeps[0])
	}
}

// TestDispatchWithRetry_HoldWithPastResetUsesJitterOnly verifies that when
// resetsAt is in the past the sleep is clamped to Policy.Jitter.
func TestDispatchWithRetry_HoldWithPastResetUsesJitterOnly(t *testing.T) {
	fixedNow := time.Unix(2_000_000, 0).UTC()
	resetAt := fixedNow.Add(-1 * time.Hour) // in the past

	fr := runner.NewFake()
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.RateLimit, ResetAt: &resetAt}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 7), fr, drv, fakeClock(fixedNow, &sleeps)) // holdJitter=7s
	writeOutcomeOnFinalCall(fr, []error{boxErr, nil}, nonceLine(d, "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok"))

	d.Run()

	if len(sleeps) != 1 {
		t.Fatalf("sleep calls: got %d, want 1", len(sleeps))
	}
	if sleeps[0] != 7*time.Second {
		t.Errorf("sleep duration: got %v, want 7s (clamped to jitter)", sleeps[0])
	}
}

// TestDispatchWithRetry_ZeroExitRateLimitHoldsAndRedispatches verifies issue
// #565: a box that exits zero but writes no SPINDRIFT_OUTCOME line, whose log
// nonetheless classifies as a rate limit with a known resetsAt, is held and
// re-dispatched exactly like a non-zero 429 exit — instead of dead-ending as
// status=missing.
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
		return nil // always exits zero, first attempt writes no outcome line
	}
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.RateLimit, ResetAt: &resetAt}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, drv, fakeClock(fixedNow, &sleeps)) // holdJitter=0

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

// TestDispatchWithRetry_ZeroExitTransientWithoutResetAtUsesBackoff verifies
// issue #565's third acceptance criterion: a zero-exit, no-outcome run whose
// log carries a transient marker but no resetsAt (or a non-rate-limit
// transient) follows the existing backoff-retry path rather than an
// indefinite hold or an immediate status=missing.
func TestDispatchWithRetry_ZeroExitTransientWithoutResetAtUsesBackoff(t *testing.T) {
	fr := runner.NewFake()
	calls := 0
	fr.RunFunc = func(box runner.Box) error {
		calls++
		if calls == 2 && box.Output != nil {
			box.Output.Write([]byte("SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok nonce=" + box.Env["RUN_NONCE"] + "\n")) //nolint:errcheck
		}
		return nil // always exits zero
	}
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.Overloaded}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 15, 0), fr, drv, fakeClock(time.Time{}, &sleeps)) // backoffSecs=15

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

// TestDispatchWithRetry_ZeroExitConsecutiveHoldsConsumeCapAndFail verifies
// issue #565's second acceptance criterion: consecutive zero-exit rate-limit
// holds that never recover count against the transient retry cap, landing on
// Success=false rather than a silent or confusing status=missing.
func TestDispatchWithRetry_ZeroExitConsecutiveHoldsConsumeCapAndFail(t *testing.T) {
	fixedNow := time.Unix(1_000_000, 0).UTC()
	resetAt := fixedNow.Add(30 * time.Minute)

	fr := runner.NewFake() // always exits zero, never writes an outcome line
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.RateLimit, ResetAt: &resetAt}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, drv, fakeClock(fixedNow, &sleeps)) // max=3

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

// TestDispatchWithRetry_ZeroExitTransientSkipsRetryWhenPRExists verifies
// issue #565's safety guard: a zero-exit, no-outcome box that classifies as
// transient is NOT re-dispatched when OpenPRForIssue reports an open PR
// already exists for the branch -- the box's work already landed, so retrying
// would duplicate it. The Result passes through unchanged, exactly as before
// #565, letting settle's own PR lookup route it.
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

// TestDispatchWithRetry_AppliesToFixToo verifies the behavior change called
// out in issue #441: a 429 during a fix pass now holds until reset instead
// of burning a fix attempt, because the retry policy applies uniformly to
// Fix as it does to Run.
func TestDispatchWithRetry_AppliesToFixToo(t *testing.T) {
	fixedNow := time.Unix(1_000_000, 0).UTC()
	resetAt := fixedNow.Add(1 * time.Hour)

	fr := runner.NewFake()
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.RateLimit, ResetAt: &resetAt}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, drv, fakeClock(fixedNow, &sleeps))
	writeOutcomeOnFinalCall(fr, []error{boxErr, nil}, nonceLine(d, "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok")) // fix pass fails once (429, no outcome), then succeeds

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

// TestDispatchWithRetry_ParsesPassManifestFromOutbox verifies the issue
// #2983 wiring: when the box's outbox holds a manifest.json written by a
// prior orchestrator run, a successful dispatch (reaching outcomeResult via
// successResult) parses it into Result.Passes.
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

// TestDispatchWithRetry_MissingPassManifestDegradesToNil verifies the
// pass-blind degrade contract (issue #2983 AC2): when no manifest.json ever
// lands in the outbox -- the ordinary case, since every OTHER test in this
// file never writes one -- Result.Passes is nil and every other field on
// Result behaves exactly as it did before Passes existed.
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
