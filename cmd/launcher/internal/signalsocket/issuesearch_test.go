package signalsocket_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/signalsocket"
	"spindrift.dev/launcher/internal/signalwire"
)

// recordingSearcher is a forge.IssueSearcher that records its calls.
type recordingSearcher struct {
	calls []string
	out   []forge.Issue
	err   error
}

func (s *recordingSearcher) SearchIssues(text string, state forge.IssueState, label string, limit int) ([]forge.Issue, error) {
	s.calls = append(s.calls, fmt.Sprintf("%q/%s/%q/%d", text, state, label, limit))
	return s.out, s.err
}

func searchHandler(t *testing.T, s forge.IssueSearcher, log io.Writer, warn io.Writer) http.Handler {
	t.Helper()
	var is signalsocket.IssueSearcher
	if s != nil {
		is = signalsocket.NewTrackerIssueSearcher(s, warn)
	}
	return signalsocket.NewHandler(newAll(t), log, nil, is)
}

func TestIssueSearchReturnsNumberTitleStateAndLabels(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "7", Title: "Flaky retry", Body: "b", Labels: []string{"agent-review-finding"}, State: forge.IssueClosed})
	f.SetIssue(forge.Issue{Number: "8", Title: "Flaky open", Labels: []string{"agent-review-finding"}, State: forge.IssueOpen})
	var log strings.Builder
	h := searchHandler(t, f, &log, io.Discard)

	w := do(t, h, http.MethodGet, "/issue-search?state=closed&label=agent-review-finding&text="+url.QueryEscape("flaky"), "")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", w.Code, w.Body)
	}
	var got signalwire.IssueSearchReply
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := signalwire.IssueSearchResult{Number: "7", Title: "Flaky retry", State: "closed", Labels: []string{"agent-review-finding"}}
	if len(got.Issues) != 1 || got.Truncated || got.Issues[0].Number != want.Number || got.Issues[0].Title != want.Title || got.Issues[0].State != want.State || len(got.Issues[0].Labels) != 1 {
		t.Errorf("got %+v, want just %+v", got, want)
	}
	if strings.Contains(w.Body.String(), `"body"`) {
		t.Errorf("reply %q carries a body", w.Body)
	}
	ops := mirroredOps(t, log.String())
	if len(ops) != 1 || ops[0].Kind != signalwire.IssueSearchRoute || ops[0].Decision != "read" {
		t.Fatalf("ops = %+v, want one issue-search/read", ops)
	}
	if ops[0].Size != len(strings.TrimSpace(w.Body.String())) || !strings.HasPrefix(ops[0].Hash, "sha256:") {
		t.Errorf("mirrored size/hash = %d/%q", ops[0].Size, ops[0].Hash)
	}
}

func TestIssueSearchRejectsBadQueriesWithoutCallingTheTracker(t *testing.T) {
	base := "text=x&state=open"
	cases := []struct{ name, query string }{
		{"label outside the set", base + "&label=agent-trigger"},
		{"empty label", base + "&label="},
		{"repeated label", base + "&label=agent-review-finding&label=agent-butler-finding"},
		{"foreign repo key", base + "&repo=other%2Fx"},
		{"owner key", base + "&owner=other"},
		{"limit key", base + "&limit=1000"},
		{"missing state", "text=x"},
		{"bad state", "text=x&state=all"},
		{"repeated state", "text=x&state=open&state=closed"},
		{"missing text", "state=open"},
		{"blank text", "state=open&text=%20"},
		{"repeated text", base + "&text=y"},
		{"invalid utf-8", "state=open&text=%ff"},
		{"text too long", "state=open&text=" + strings.Repeat("a", signalwire.MaxIssueSearchTextBytes+1)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &recordingSearcher{}
			var log strings.Builder
			h := searchHandler(t, s, &log, io.Discard)
			wantReject(t, do(t, h, http.MethodGet, "/issue-search?"+c.query, ""), "invalid_query", http.StatusBadRequest)
			if len(s.calls) != 0 {
				t.Errorf("tracker called: %v", s.calls)
			}
			if ops := mirroredOps(t, log.String()); len(ops) != 1 || ops[0].Kind != signalwire.IssueSearchRoute || ops[0].Decision != "reject" {
				t.Fatalf("ops = %+v, want one issue-search/reject", ops)
			}
		})
	}
}

func TestIssueSearchAcceptsEveryFixedLabelAndTextAtTheLimit(t *testing.T) {
	for _, l := range []string{"agent-review-finding", "agent-research-reject", "agent-research-finding", "agent-butler-finding"} {
		s := &recordingSearcher{}
		h := searchHandler(t, s, nil, io.Discard)
		w := do(t, h, http.MethodGet, "/issue-search?state=open&label="+l+"&text="+strings.Repeat("a", signalwire.MaxIssueSearchTextBytes), "")
		if w.Code != http.StatusOK || len(s.calls) != 1 {
			t.Errorf("%s: status %d (%s), calls %v", l, w.Code, w.Body, s.calls)
		}
	}
}

func TestIssueSearchCapsResultsAndReportsTruncation(t *testing.T) {
	s := &recordingSearcher{}
	for i := range 3 * signalwire.MaxIssueSearchResults {
		s.out = append(s.out, forge.Issue{Number: fmt.Sprint(i + 1), Title: "t", State: forge.IssueOpen})
	}
	h := searchHandler(t, s, nil, io.Discard)

	w := do(t, h, http.MethodGet, "/issue-search?state=open&text=t", "")

	var got signalwire.IssueSearchReply
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Issues) != signalwire.MaxIssueSearchResults || !got.Truncated {
		t.Errorf("got %d results truncated=%v, want %d and true", len(got.Issues), got.Truncated, signalwire.MaxIssueSearchResults)
	}
	if want := fmt.Sprintf("%q/%s/%q/%d", "t", forge.IssueOpen, "", signalwire.MaxIssueSearchResults+1); len(s.calls) != 1 || s.calls[0] != want {
		t.Errorf("calls = %v, want [%s]", s.calls, want)
	}
}

func TestIssueSearchExactlyTheCapIsNotTruncated(t *testing.T) {
	s := &recordingSearcher{}
	for i := range signalwire.MaxIssueSearchResults {
		s.out = append(s.out, forge.Issue{Number: fmt.Sprint(i + 1)})
	}
	w := do(t, searchHandler(t, s, nil, io.Discard), http.MethodGet, "/issue-search?state=open&text=t", "")
	var got signalwire.IssueSearchReply
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || len(got.Issues) != signalwire.MaxIssueSearchResults || got.Truncated {
		t.Errorf("got %+v, %v", got, err)
	}
	if !strings.Contains(w.Body.String(), `"labels":[]`) {
		t.Errorf("reply %q: nil labels must encode as []", w.Body)
	}
}

func TestIssueSearchRouteRefusalIs403AndFailureIs502WithoutDetail(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status string
		code   int
	}{
		{"route table refusal", fmt.Errorf("%w: connector %q: secret-host", connectorhost.ErrRouteRefused, "jira"), "refused", http.StatusForbidden},
		{"tracker failure", errors.New("dial tcp secret-host: boom"), "tracker_error", http.StatusBadGateway},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var warn strings.Builder
			h := searchHandler(t, &recordingSearcher{err: c.err}, nil, &warn)
			w := do(t, h, http.MethodGet, "/issue-search?state=open&text=t", "")
			wantReject(t, w, c.status, c.code)
			if strings.Contains(w.Body.String(), "secret-host") || !strings.Contains(warn.String(), "secret-host") {
				t.Errorf("body %q / warn %q: detail must reach only warn", w.Body, warn.String())
			}
		})
	}
}

func TestGatedHandlerIssueSearchRequiresTheSecret(t *testing.T) {
	s := &recordingSearcher{out: []forge.Issue{{Number: "7", Title: "t", State: forge.IssueOpen}}}
	h, err := signalsocket.NewGatedHandler(newAll(t), nil, "s3cret", nil, signalsocket.NewTrackerIssueSearcher(s, io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	const path = "/issue-search?state=open&text=t"

	wantReject(t, do(t, h, http.MethodGet, path, ""), "unauthorized", http.StatusUnauthorized)
	if len(s.calls) != 0 {
		t.Fatalf("searcher was called %d times without the secret", len(s.calls))
	}

	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Header.Set(signalwire.SecretHeader, "s3cret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || len(s.calls) != 1 {
		t.Fatalf("status with the secret = %d (%s), calls = %d, want 200 and 1", w.Code, w.Body, len(s.calls))
	}
}

func TestIssueSearchWithoutASearcherIsUnsupported(t *testing.T) {
	wantReject(t, do(t, searchHandler(t, nil, nil, nil), http.MethodGet, "/issue-search?state=open&text=t", ""), "unsupported", http.StatusNotImplemented)
}

func TestIssueSearchRefusesNonGET(t *testing.T) {
	s := &recordingSearcher{}
	h := searchHandler(t, s, nil, io.Discard)
	wantReject(t, do(t, h, http.MethodPost, "/issue-search?state=open&text=t", ""), "method_not_allowed", http.StatusMethodNotAllowed)
	if len(s.calls) != 0 {
		t.Errorf("tracker called: %v", s.calls)
	}
}
