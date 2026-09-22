package dispatch

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/signalsocket"
	"spindrift.dev/launcher/internal/signalwire"
)

var allKinds = []signalwire.Kind{signalwire.KindComment, signalwire.KindPRIntent, signalwire.KindIssueIntent}

// newSocketDispatch builds a Dispatch configured for BOX_SIGNAL_CARRIER=socket,
// mirroring newTestDispatch but with the knob set and no listener actually
// started -- tests wire buf onto d.signalBuffer directly, the way
// startSignalSocket does at runtime.
func newSocketDispatch(t *testing.T) *Dispatch {
	t.Helper()
	fr := runner.NewFake()
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{}, nil
	}}
	var sleeps []time.Duration
	cfg := retryConfig(3, 0, 0)
	cfg.SignalCarrier = "socket"
	return newTestDispatch(t, cfg, fr, drv, fakeClock(time.Time{}, &sleeps))
}

// resolvedReady is the outcome.Resolved shared by these tests: outcomeResult
// only cares that resolved is Found and genuine, never its Status.
var resolvedReady = outcome.Resolved{Found: true, Provenance: outcome.ProvenanceGenuine, Outcome: outcome.Outcome{Status: "ready"}}

// writeEmptyLog creates an empty log file at d.logPath -- the scanners need
// an openable file, and an empty one carries no marker lines.
func writeEmptyLog(t *testing.T, d *Dispatch) string {
	t.Helper()
	p := d.logPath()
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatalf("write empty log: %v", err)
	}
	return p
}

// markerLines renders comment/prIntent/issueIntents as the marker lines a
// log-carrier Box would have printed for d's nonce, the log-mode fixture the
// carrier-equivalence tests compare against.
func markerLines(d *Dispatch, comment *signalwire.Comment, prIntent *signalwire.PRIntent, issueIntents []signalwire.IssueIntent) string {
	var b strings.Builder
	if comment != nil {
		b.WriteString("SPINDRIFT_COMMENT " + d.nonce + " " + base64.StdEncoding.EncodeToString([]byte(comment.Body)) + "\n")
	}
	if prIntent != nil {
		payload := prIntent.Title + "\n\n" + prIntent.Body
		b.WriteString("SPINDRIFT_PR_INTENT " + d.nonce + " " + base64.StdEncoding.EncodeToString([]byte(payload)) + "\n")
	}
	for _, i := range issueIntents {
		raw, err := json.Marshal(i)
		if err != nil {
			panic(err)
		}
		b.WriteString("SPINDRIFT_ISSUE_INTENT " + d.nonce + " " + base64.StdEncoding.EncodeToString(raw) + "\n")
	}
	return b.String()
}

// bufferWith builds a Buffer pre-loaded with comment/prIntent/issueIntents,
// nil-skipping whichever channel a case leaves absent.
func bufferWith(t *testing.T, comment *signalwire.Comment, prIntent *signalwire.PRIntent, issueIntents []signalwire.IssueIntent) *signalsocket.Buffer {
	t.Helper()
	buf := signalsocket.New(signalsocket.Config{Consumes: allKinds})
	if comment != nil {
		if _, rej := buf.AcceptComment(*comment); rej != nil {
			t.Fatalf("AcceptComment rejected: %+v", rej)
		}
	}
	if prIntent != nil {
		if _, rej := buf.AcceptPRIntent(*prIntent); rej != nil {
			t.Fatalf("AcceptPRIntent rejected: %+v", rej)
		}
	}
	for _, i := range issueIntents {
		if _, rej := buf.AcceptIssueIntent(i); rej != nil {
			t.Fatalf("AcceptIssueIntent rejected: %+v", rej)
		}
	}
	return buf
}

// TestOutcomeResult_SocketCarrierEquivalentToLogMarkerLines is this ticket's
// central contract: a socket-carrier Result built from a buffer must equal
// the log-carrier Result built from equivalent marker lines, field for field,
// whichever of the three channels are present or absent.
func TestOutcomeResult_SocketCarrierEquivalentToLogMarkerLines(t *testing.T) {
	comment := &signalwire.Comment{Body: "verdict body"}
	prIntent := &signalwire.PRIntent{Title: "feat: widget", Body: "Adds a widget."}
	issueIntents := []signalwire.IssueIntent{
		{Title: "first", Body: "first body", Type: "bug"},
		{Title: "second", Body: "second body", Type: "chore"},
	}

	cases := []struct {
		name         string
		comment      *signalwire.Comment
		prIntent     *signalwire.PRIntent
		issueIntents []signalwire.IssueIntent
	}{
		{"all present", comment, prIntent, issueIntents},
		{"comment absent", nil, prIntent, issueIntents},
		{"pr intent absent", comment, nil, issueIntents},
		{"issue intents absent", comment, prIntent, nil},
		{"all absent", nil, nil, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dSocket := newSocketDispatch(t)
			dSocket.signalBuffer = bufferWith(t, tc.comment, tc.prIntent, tc.issueIntents)
			socketLog := writeEmptyLog(t, dSocket)
			gotSocket := dSocket.outcomeResult(socketLog, resolvedReady)

			var sleeps []time.Duration
			dLog := newTestDispatch(t, retryConfig(3, 0, 0), runner.NewFake(),
				fakeDriver{ClassifyFn: func(string) (driver.Classification, error) { return driver.Classification{}, nil }},
				fakeClock(time.Time{}, &sleeps))
			logPath := dLog.logPath()
			if err := os.WriteFile(logPath, []byte(markerLines(dLog, tc.comment, tc.prIntent, tc.issueIntents)), 0o644); err != nil {
				t.Fatalf("write marker log: %v", err)
			}
			gotLog := dLog.outcomeResult(logPath, resolvedReady)

			if gotSocket.Comment != gotLog.Comment || gotSocket.CommentFound != gotLog.CommentFound {
				t.Errorf("Comment: socket=(%q,%v) log=(%q,%v)", gotSocket.Comment, gotSocket.CommentFound, gotLog.Comment, gotLog.CommentFound)
			}
			if gotSocket.PRIntent != gotLog.PRIntent || gotSocket.PRIntentFound != gotLog.PRIntentFound {
				t.Errorf("PRIntent: socket=(%q,%v) log=(%q,%v)", gotSocket.PRIntent, gotSocket.PRIntentFound, gotLog.PRIntent, gotLog.PRIntentFound)
			}
			if len(gotSocket.IssueIntents) != len(gotLog.IssueIntents) || gotSocket.IssueIntentsFound != gotLog.IssueIntentsFound {
				t.Fatalf("IssueIntents: socket=%v (found=%v) log=%v (found=%v)", gotSocket.IssueIntents, gotSocket.IssueIntentsFound, gotLog.IssueIntents, gotLog.IssueIntentsFound)
			}
			for i := range gotSocket.IssueIntents {
				if gotSocket.IssueIntents[i] != gotLog.IssueIntents[i] {
					t.Errorf("IssueIntents[%d]: socket=%q log=%q", i, gotSocket.IssueIntents[i], gotLog.IssueIntents[i])
				}
			}
		})
	}
}

// TestSignalResultFromBuffer_ReplaceSemantics pins that a second comment and
// a second PR intent through the buffer leave only the last, the same
// last-line-wins the log carrier's scanners give (issue #3725).
func TestSignalResultFromBuffer_ReplaceSemantics(t *testing.T) {
	buf := signalsocket.New(signalsocket.Config{Consumes: allKinds})
	if _, rej := buf.AcceptComment(signalwire.Comment{Body: "first"}); rej != nil {
		t.Fatalf("first AcceptComment rejected: %+v", rej)
	}
	if _, rej := buf.AcceptComment(signalwire.Comment{Body: "second"}); rej != nil {
		t.Fatalf("second AcceptComment rejected: %+v", rej)
	}
	if _, rej := buf.AcceptPRIntent(signalwire.PRIntent{Title: "first title", Body: "first body"}); rej != nil {
		t.Fatalf("first AcceptPRIntent rejected: %+v", rej)
	}
	if _, rej := buf.AcceptPRIntent(signalwire.PRIntent{Title: "second title", Body: "second body"}); rej != nil {
		t.Fatalf("second AcceptPRIntent rejected: %+v", rej)
	}

	comment, commentFound, prIntent, prIntentFound, _ := signalResultFromBuffer(buf)
	if !commentFound || comment != "second" {
		t.Errorf("Comment: got (%q,%v), want (%q,true)", comment, commentFound, "second")
	}
	wantPR := "second title\n\nsecond body"
	if !prIntentFound || prIntent != wantPR {
		t.Errorf("PRIntent: got (%q,%v), want (%q,true)", prIntent, prIntentFound, wantPR)
	}
}

// TestSignalResultFromBuffer_AppendWithDedup pins that a repeated
// byte-identical issue intent appears once while a different one appends, in
// order -- logic that lives in signalsocket.Buffer and must not be
// reimplemented here.
func TestSignalResultFromBuffer_AppendWithDedup(t *testing.T) {
	buf := signalsocket.New(signalsocket.Config{Consumes: allKinds})
	a := signalwire.IssueIntent{Title: "a", Body: "a body", Type: "bug"}
	b := signalwire.IssueIntent{Title: "b", Body: "b body", Type: "chore"}
	if _, rej := buf.AcceptIssueIntent(a); rej != nil {
		t.Fatalf("first AcceptIssueIntent(a) rejected: %+v", rej)
	}
	if _, rej := buf.AcceptIssueIntent(a); rej != nil {
		t.Fatalf("repeat AcceptIssueIntent(a) rejected: %+v", rej)
	}
	if _, rej := buf.AcceptIssueIntent(b); rej != nil {
		t.Fatalf("AcceptIssueIntent(b) rejected: %+v", rej)
	}

	_, _, _, _, issueIntents := signalResultFromBuffer(buf)
	if len(issueIntents) != 2 {
		t.Fatalf("IssueIntents: got %d entries, want 2: %v", len(issueIntents), issueIntents)
	}
	var gotA, gotB signalwire.IssueIntent
	if err := json.Unmarshal([]byte(issueIntents[0]), &gotA); err != nil {
		t.Fatalf("decode [0]: %v", err)
	}
	if err := json.Unmarshal([]byte(issueIntents[1]), &gotB); err != nil {
		t.Fatalf("decode [1]: %v", err)
	}
	if gotA != a {
		t.Errorf("issueIntents[0]: got %+v, want %+v", gotA, a)
	}
	if gotB != b {
		t.Errorf("issueIntents[1]: got %+v, want %+v", gotB, b)
	}
}

// TestOutcomeResult_SocketModeWarnsOnStaleLogMarkerLines is the ticket's
// second acceptance criterion: in socket mode a log that still carries a
// marker line for one of the three channels warns naming that channel and
// contributes no data. d.signalBuffer is left nil, the "never started a
// listener" case, which must degrade to empty signals rather than panic.
func TestOutcomeResult_SocketModeWarnsOnStaleLogMarkerLines(t *testing.T) {
	d := newSocketDispatch(t)
	comment := &signalwire.Comment{Body: "verdict body"}
	prIntent := &signalwire.PRIntent{Title: "feat: widget", Body: "Adds a widget."}
	issueIntents := []signalwire.IssueIntent{{Title: "first", Body: "first body", Type: "bug"}}
	logPath := d.logPath()
	if err := os.WriteFile(logPath, []byte(markerLines(d, comment, prIntent, issueIntents)), 0o644); err != nil {
		t.Fatalf("write marker log: %v", err)
	}

	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	got := d.outcomeResult(logPath, resolvedReady)
	w.Close()
	os.Stderr = old
	captured, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read captured stderr: %v", err)
	}
	stderr := string(captured)

	for _, want := range []string{"comment marker line", "pr-intent marker line", "issue-intent marker line"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr must name %q, got: %s", want, stderr)
		}
	}
	if got.Comment != "" || got.CommentFound {
		t.Errorf("Comment: got (%q,%v), want empty/false", got.Comment, got.CommentFound)
	}
	if got.PRIntent != "" || got.PRIntentFound {
		t.Errorf("PRIntent: got (%q,%v), want empty/false", got.PRIntent, got.PRIntentFound)
	}
	if len(got.IssueIntents) != 0 || got.IssueIntentsFound {
		t.Errorf("IssueIntents: got (%v,%v), want empty/false", got.IssueIntents, got.IssueIntentsFound)
	}
	if got.CommentRejected.Total() != 0 || got.PRIntentRejected.Total() != 0 || got.IssueIntentsRejected.Total() != 0 {
		t.Errorf("Rejected counts must stay zero in socket mode: comment=%v pr=%v issue=%v",
			got.CommentRejected, got.PRIntentRejected, got.IssueIntentsRejected)
	}
}

// TestOutcomeResult_SocketModeWarnsOnRejectedLogMarkerLines pins the same
// acceptance criterion for a marker line that attempted the grammar and
// failed to verify: a nonce-mismatched line is still a log-carrier marker
// line, so socket mode warns on it too rather than passing over it in
// silence.
func TestOutcomeResult_SocketModeWarnsOnRejectedLogMarkerLines(t *testing.T) {
	d := newSocketDispatch(t)
	// A nonce no Box of this run could have printed, so every scanner counts
	// its line as rejected rather than found.
	stale := "SPINDRIFT_COMMENT " + d.nonce + "-stale " + base64.StdEncoding.EncodeToString([]byte("verdict body")) + "\n"
	logPath := d.logPath()
	if err := os.WriteFile(logPath, []byte(stale), 0o644); err != nil {
		t.Fatalf("write marker log: %v", err)
	}

	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	got := d.outcomeResult(logPath, resolvedReady)
	w.Close()
	os.Stderr = old
	captured, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read captured stderr: %v", err)
	}

	if !strings.Contains(string(captured), "comment marker line") {
		t.Errorf("stderr must name the comment channel, got: %s", captured)
	}
	if got.Comment != "" || got.CommentFound || got.CommentRejected.Total() != 0 {
		t.Errorf("comment signals must stay empty in socket mode: (%q,%v,%v)", got.Comment, got.CommentFound, got.CommentRejected)
	}
}
