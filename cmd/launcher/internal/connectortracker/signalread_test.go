package connectortracker

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
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
