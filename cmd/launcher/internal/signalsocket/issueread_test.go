package signalsocket_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/signalsocket"
	"spindrift.dev/launcher/internal/signalwire"
)

// trackerHandler serves the real handler over a tracker-backed reader scoped
// to scope, the way the Pass builds it.
func trackerHandler(t *testing.T, tr forge.IssueTracker, scope string, warn io.Writer) http.Handler {
	t.Helper()
	return signalsocket.NewHandler(newAll(t), nil, signalsocket.NewTrackerIssueReader(tr, scope, warn), nil)
}

func readIssue(t *testing.T, h http.Handler, ref string) (int, []byte) {
	t.Helper()
	w := do(t, h, http.MethodGet, "/issue?ref="+url.QueryEscape(ref), "")
	return w.Code, w.Body.Bytes()
}

func TestTrackerIssueReaderReadsAnIssueInTheDispatchRepository(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "7", Title: "t", Body: "b", Labels: []string{"bug"}, State: forge.IssueOpen})
	var cs []forge.Comment
	for i := range 13 {
		cs = append(cs, forge.Comment{Author: "a", CreatedAt: "2026-01-01T00:00:00Z", Body: fmt.Sprintf("c%02d", i)})
	}
	cs[12].Minimized = true
	f.CommentsFor = map[string][]forge.Comment{"7": cs}
	h := trackerHandler(t, f, "owner/repo", io.Discard)

	for _, ref := range []string{"7", "#7", "owner/repo#7", "Owner/Repo#7"} {
		t.Run(ref, func(t *testing.T) {
			code, body := readIssue(t, h, ref)
			if code != http.StatusOK {
				t.Fatalf("status = %d (%s), want 200", code, body)
			}
			var got signalwire.IssueSnapshot
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatal(err)
			}
			if got.Ref != "7" || got.Title != "t" || got.Body != "b" || got.State != "open" || len(got.Labels) != 1 || got.Truncated {
				t.Errorf("got %+v", got)
			}
			if len(got.Comments) != 10 || got.Comments[0].Body != "c02" || got.Comments[9].Body != "c11" {
				t.Errorf("comments = %+v, want c02..c11 (last 10 live)", got.Comments)
			}
		})
	}
}

func TestTrackerIssueReaderReportsAClosedIssue(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "7", State: forge.IssueClosed})
	code, body := readIssue(t, trackerHandler(t, f, "owner/repo", io.Discard), "7")
	if code != http.StatusOK || !strings.Contains(string(body), `"state":"closed"`) {
		t.Fatalf("got %d %s, want a closed snapshot", code, body)
	}
}

func TestTrackerIssueReaderRefusesAnotherRepositoryWithoutAToolCall(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "5", Title: "secret"})
	h := trackerHandler(t, f, "owner/repo", io.Discard)

	for _, ref := range []string{"other/repo#5", "owner/other#5", "OTHER-5", "owner/repo-evil#5"} {
		t.Run(ref, func(t *testing.T) {
			w := do(t, h, http.MethodGet, "/issue?ref="+url.QueryEscape(ref), "")
			rej := wantReject(t, w, "foreign_ref", http.StatusForbidden)
			if strings.Contains(rej.Reason, ref) || strings.Contains(w.Body.String(), "secret") {
				t.Errorf("reply %q echoes the ref or the issue", w.Body)
			}
		})
	}
	if len(f.IssueCalls) != 0 {
		t.Errorf("tracker saw Issue calls %v, want none", f.IssueCalls)
	}
}

func TestTrackerIssueReaderJiraScope(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "PROJ-12", Title: "jira"})
	h := trackerHandler(t, f, "PROJ", io.Discard)

	for _, ref := range []string{"PROJ-12", "proj-12", "12", "#12", "PROJ#12"} {
		if code, body := readIssue(t, h, ref); code != http.StatusOK || !strings.Contains(string(body), `"title":"jira"`) {
			t.Errorf("ref %q: got %d %s, want the PROJ-12 snapshot", ref, code, body)
		}
	}
	for _, ref := range []string{"OTHER-12", "owner/repo#12"} {
		wantReject(t, do(t, h, http.MethodGet, "/issue?ref="+url.QueryEscape(ref), ""), "foreign_ref", http.StatusForbidden)
	}
	for _, c := range f.IssueCalls {
		if c != "PROJ-12" {
			t.Errorf("tracker saw Issue(%q), want only PROJ-12", c)
		}
	}
}

func TestTrackerIssueReaderRefusesMalformedRefsWithoutAToolCall(t *testing.T) {
	f := forge.NewFake()
	h := trackerHandler(t, f, "owner/repo", io.Discard)

	for _, ref := range []string{
		"0", "007", "+7", "-7", "7.0", "1e3", "7 ", "7/comments", "../7", "7?x=1", "7%2F..", "#", "#0", "owner/repo#", "owner/repo#07",
		"owner/repo#7#8", "https://github.com/owner/repo/issues/7", "owner/repo/issues/7", "99999999999999999999", "../x#7", "7\n8",
	} {
		t.Run(ref, func(t *testing.T) {
			w := do(t, h, http.MethodGet, "/issue?ref="+url.QueryEscape(ref), "")
			if w.Code != http.StatusBadRequest && w.Code != http.StatusForbidden {
				t.Fatalf("status = %d (%s), want a refusal", w.Code, w.Body)
			}
			if rej := decodeReject(t, w); rej.Status != "invalid_ref" && rej.Status != "foreign_ref" {
				t.Errorf("status %q, want invalid_ref or foreign_ref", rej.Status)
			}
		})
	}
	if len(f.IssueCalls) != 0 {
		t.Errorf("tracker saw Issue calls %v, want none", f.IssueCalls)
	}
}

func TestTrackerIssueReaderCapsAnOversizedIssue(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "7", Title: "big", Body: strings.Repeat("x", 3*signalwire.MaxIssueReplyBytes)})
	var cs []forge.Comment
	for range 10 {
		cs = append(cs, forge.Comment{Author: "a", Body: strings.Repeat("y", signalwire.MaxIssueReplyBytes)})
	}
	f.CommentsFor = map[string][]forge.Comment{"7": cs}

	code, body := readIssue(t, trackerHandler(t, f, "owner/repo", io.Discard), "7")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	var got signalwire.IssueSnapshot
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Truncated || got.TextBytes() > signalwire.MaxIssueReplyBytes {
		t.Errorf("truncated=%v text=%d, want a capped snapshot", got.Truncated, got.TextBytes())
	}
}

func TestTrackerIssueReaderRefusesAnIssueOfAnotherProject(t *testing.T) {
	f := forge.NewFake()
	// A numeric Jira id that resolves to a different project's issue.
	f.SetIssue(forge.Issue{Number: "OTHER-12", Title: "elsewhere"})
	tr := renumbered{f, "OTHER-12"}

	w := do(t, trackerHandler(t, tr, "PROJ", io.Discard), http.MethodGet, "/issue?ref=PROJ-12", "")
	wantReject(t, w, "foreign_ref", http.StatusForbidden)
	if strings.Contains(w.Body.String(), "elsewhere") {
		t.Errorf("reply %q leaks the foreign issue", w.Body)
	}
}

// renumbered answers every Issue call with the issue stored under key,
// whatever number was asked for.
type renumbered struct {
	*forge.Fake
	key string
}

func (r renumbered) Issue(string) (forge.Issue, error) { return r.Fake.Issue(r.key) }

func TestTrackerIssueReaderMapsTrackerErrorsWithoutLeakingDetail(t *testing.T) {
	var warn strings.Builder
	f := forge.NewFake()
	f.IssueErrByNum = map[string]error{
		"1": fmt.Errorf("token ghp_secret: %w", forge.ErrNotFound),
		"2": fmt.Errorf("dial tcp 10.0.0.1: refused"),
	}
	h := trackerHandler(t, f, "owner/repo", &warn)

	w := do(t, h, http.MethodGet, "/issue?ref=1", "")
	wantReject(t, w, "not_found", http.StatusNotFound)
	w2 := do(t, h, http.MethodGet, "/issue?ref=2", "")
	wantReject(t, w2, "tracker_error", http.StatusBadGateway)
	for _, body := range []string{w.Body.String(), w2.Body.String()} {
		if strings.Contains(body, "ghp_secret") || strings.Contains(body, "10.0.0.1") {
			t.Errorf("reply %q leaks tracker detail", body)
		}
	}
	if !strings.Contains(warn.String(), "10.0.0.1") {
		t.Errorf("warn = %q, want the detail logged host-side", warn.String())
	}
}

func TestTrackerIssueReaderFlagsUnreadableComments(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "7", Title: "t", Body: "b"})
	f.CommentsErr = map[string]error{"7": fmt.Errorf("boom")}
	code, body := readIssue(t, trackerHandler(t, f, "owner/repo", io.Discard), "7")
	if code != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", code, body)
	}
	var got signalwire.IssueSnapshot
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Truncated || got.Body != "b" || len(got.Comments) != 0 {
		t.Errorf("got %+v, want the issue with Truncated set so the Box sees the thread is incomplete", got)
	}
}
