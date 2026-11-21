package settle

import (
	"encoding/json"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/signalsocket"
	"spindrift.dev/launcher/internal/signalwire"
)

// The log carrier's raw SPINDRIFT_ISSUE_INTENT JSON and the socket carrier's
// signalwire.IssueIntent, round-tripped through signalsocket.Buffer as
// dispatch.signalResultFromBuffer does, must settle the same finding with the
// same title, body (backlink and dedup marker included) and labels, since both
// carriers share one wire type and one Validate (issue #3992).
func TestFileIssueIntentsDetailed_SameFindingFromEitherCarrier(t *testing.T) {
	const rawLog = `{"title":"nil deref on the hot path","body":"a nil check is missing before the dereference","dedupTerms":["a.go:X"],"type":"bug","class":"error-handling","concurrence":"agree"}`

	i := signalwire.IssueIntent{
		Title:       "nil deref on the hot path",
		Body:        "a nil check is missing before the dereference",
		DedupTerms:  []string{"a.go:X"},
		Type:        "bug",
		Class:       "error-handling",
		Concurrence: "agree",
	}
	buf := signalsocket.New(signalsocket.Config{Consumes: []signalwire.Kind{signalwire.KindIssueIntent}})
	if _, rej := buf.AcceptIssueIntent(i); rej != nil {
		t.Fatalf("AcceptIssueIntent: rejected %+v", rej)
	}
	stored := buf.IssueIntents()
	if len(stored) != 1 {
		t.Fatalf("IssueIntents() = %+v, want 1", stored)
	}
	rawSocketBytes, err := json.Marshal(stored[0])
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	rawSocket := string(rawSocketBytes)

	fcLog := forge.NewFake(testDispatchLabels)
	fcLog.PostIssueURL = "https://github.com/owner/repo/issues/1"
	fileIssueIntentsDetailed(StdoutProgress{}, fcLog.AsIssueFiler(), "1", dispatch.Result{IssueIntentsFound: true, IssueIntents: []string{rawLog}}, "agent-review-finding", "")

	fcSocket := forge.NewFake(testDispatchLabels)
	fcSocket.PostIssueURL = "https://github.com/owner/repo/issues/1"
	fileIssueIntentsDetailed(StdoutProgress{}, fcSocket.AsIssueFiler(), "1", dispatch.Result{IssueIntentsFound: true, IssueIntents: []string{rawSocket}}, "agent-review-finding", "")

	if len(fcLog.PostIssueCalls) != 1 || len(fcSocket.PostIssueCalls) != 1 {
		t.Fatalf("PostIssueCalls: log=%+v socket=%+v, want exactly 1 each", fcLog.PostIssueCalls, fcSocket.PostIssueCalls)
	}
	log, sock := fcLog.PostIssueCalls[0], fcSocket.PostIssueCalls[0]
	if log.Title != sock.Title {
		t.Errorf("Title differs: log=%q socket=%q", log.Title, sock.Title)
	}
	if log.Body != sock.Body {
		t.Errorf("Body differs: log=%q socket=%q", log.Body, sock.Body)
	}
	if strings.Join(log.Labels, ",") != strings.Join(sock.Labels, ",") {
		t.Errorf("Labels differ: log=%v socket=%v", log.Labels, sock.Labels)
	}
}

// The other half of issue #3992: an invalid Class once was blanked and the
// finding filed anyway. Validate rejects the whole intent, so it is skipped
// like any malformed payload, and the skip line names the reject's reason.
func TestFileIssueIntentsDetailed_InvalidClassSkipsRatherThanClears(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.PostIssueURL = "https://github.com/owner/repo/issues/1"

	result := dispatch.Result{
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"a finding with a bogus class","body":"body","class":"Not A Slug"}`,
		},
	}

	var detailed []filedIntent
	stderr := captureStderr(t, func() {
		detailed = fileIssueIntentsDetailed(StdoutProgress{}, fc.AsIssueFiler(), "1", result, "agent-review-finding", "")
	})

	if len(fc.PostIssueCalls) != 0 {
		t.Fatalf("PostIssueCalls = %+v, want none: an invalid class must skip the whole intent", fc.PostIssueCalls)
	}
	if len(detailed) != 0 {
		t.Fatalf("detailed = %+v, want no filedIntent entry", detailed)
	}
	if !strings.Contains(stderr, "skipping invalid issue-intent payload: class") {
		t.Errorf("stderr = %q, want a skip line naming the invalid_class reason", stderr)
	}
}
