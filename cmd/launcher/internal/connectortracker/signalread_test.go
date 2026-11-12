package connectortracker

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/signalsocket"
)

func connectorIssueHandler(t *testing.T, replies map[string]string, guestErr error) (http.Handler, *[]recordedCall) {
	t.Helper()
	call, calls := stubGuest(t, replies)
	guest := call
	if guestErr != nil {
		guest = func(context.Context, string, any, any) error {
			return guestErr
		}
	}
	tr := mustTracker(t, guest, newNative(), testLifecycle, testTarget, nil, true)
	rd := signalsocket.NewTrackerIssueReader(tr, "o/r", io.Discard)
	return signalsocket.NewHandler(signalsocket.New(signalsocket.Config{}), nil, rd, nil), calls
}

func TestSignalIssueReadThroughAConnectorTracker(t *testing.T) {
	h, calls := connectorIssueHandler(t, map[string]string{
		"tracker.issue": `{"number":"7","title":"from the connector","state":"open","labels":[]}`,
	}, nil)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/issue?ref=o%2Fr%237", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "from the connector") {
		t.Fatalf("got %d %s, want the connector's issue", w.Code, w.Body)
	}

	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/issue?ref=other%2Frepo%237", nil))
	if w.Code != http.StatusForbidden || len(*calls) != 1 {
		t.Fatalf("foreign ref: got %d with %d guest calls, want 403 and no further call", w.Code, len(*calls))
	}
}

// The host's http_request path records the audit entry before it returns
// ErrRouteRefused (connectorhost's TestNetworkFixtureRefusesUndeclaredRequestsUnsent
// pins DecisionRefused); the tracker read must surface it as a refusal, not a
// generic tracker failure.
func TestSignalIssueReadRefusesWhatTheRouteTableDoesNotAdmit(t *testing.T) {
	refused := fmt.Errorf("%w: %w: connector \"c\": GET /secret not admitted", connectorhost.ErrCall, connectorhost.ErrRouteRefused)
	h, _ := connectorIssueHandler(t, map[string]string{"tracker.issue": `{}`}, refused)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/issue?ref=7", nil))
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"refused"`) {
		t.Fatalf("got %d %s, want a 403 refused", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "secret") {
		t.Errorf("reply %q leaks the refused route", w.Body)
	}
}

func connectorSearchHandler(t *testing.T, replies map[string]string, guestErr error) (http.Handler, *[]recordedCall) {
	t.Helper()
	call, calls := stubGuest(t, replies)
	guest := call
	if guestErr != nil {
		guest = func(context.Context, string, any, any) error {
			return guestErr
		}
	}
	tr := mustTracker(t, guest, newNative(), testLifecycle, testTarget, []string{CapSearchIssues}, true)
	c := forge.ResolveCapabilities(nil, tr, backend.Descriptor{}, backend.Descriptor{})
	if c.IssueSearcher == nil {
		t.Fatal("a Connector advertising tracker.searchIssues resolved no IssueSearcher")
	}
	s := signalsocket.NewTrackerIssueSearcher(c.IssueSearcher, io.Discard)
	return signalsocket.NewHandler(signalsocket.New(signalsocket.Config{}), nil, nil, s), calls
}

func TestSignalIssueSearchThroughAConnectorTracker(t *testing.T) {
	h, calls := connectorSearchHandler(t, map[string]string{
		"tracker.searchIssues": `{"issues":[{"number":"7","title":"from the connector","state":"closed","labels":["agent-review-finding"]}]}`,
	}, nil)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/issue-search?text=flaky&state=closed&label=agent-review-finding", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "from the connector") {
		t.Fatalf("got %d %s, want the connector's hit", w.Code, w.Body)
	}
	if len(*calls) != 1 || (*calls)[0].method != CapSearchIssues || (*calls)[0].params["text"] != "flaky" || (*calls)[0].params["state"] != "closed" {
		t.Fatalf("guest calls = %+v, want one tracker.searchIssues with the query", *calls)
	}
}

func TestSignalIssueSearchRefusesWhatTheRouteTableDoesNotAdmit(t *testing.T) {
	refused := fmt.Errorf("%w: %w: connector \"c\": GET /secret not admitted", connectorhost.ErrCall, connectorhost.ErrRouteRefused)
	h, _ := connectorSearchHandler(t, map[string]string{"tracker.searchIssues": `{}`}, refused)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/issue-search?text=flaky&state=open", nil))
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"refused"`) {
		t.Fatalf("got %d %s, want a 403 refused", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "secret") {
		t.Errorf("reply %q leaks the refused route", w.Body)
	}
}

func TestSignalIssueSearchDropsHitsTheConnectorDidNotFilter(t *testing.T) {
	h, _ := connectorSearchHandler(t, map[string]string{
		"tracker.searchIssues": `{"issues":[
			{"number":"1","title":"keeps","state":"closed","labels":["agent-review-finding"]},
			{"number":"2","title":"unlabelled","state":"closed","labels":["bug"]},
			{"number":"3","title":"wrong state","state":"open","labels":["agent-review-finding"]}]}`,
	}, nil)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/issue-search?text=flaky&state=closed&label=agent-review-finding", nil))
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, "keeps") || strings.Contains(body, "unlabelled") || strings.Contains(body, "wrong state") {
		t.Fatalf("got %d %s, want only the closed, labelled hit", w.Code, body)
	}
}
