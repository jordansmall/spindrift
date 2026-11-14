package connectorhost_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
)

func TestHostCallsHTTPSendFailureIsAFixedReasonTheGuestAnswers(t *testing.T) {
	b := newBackend(t, func(http.ResponseWriter, *http.Request) {})
	var audit bytes.Buffer
	n := netFor(b, &audit, connectorhost.Route{Method: "GET", Host: b.host(), Path: "/repos"})
	n.Client = &http.Client{} // does not trust the test certificate
	h := connectorhost.NewHostCalls(n)
	raw := fmt.Sprintf(`{"method":"GET","url":"%s/repos"}`, b.URL)
	status, body, hdr, err := h.HTTPRequest(context.Background(), []byte(raw), 1<<10)
	if err != nil || status != 0 || hdr != nil || string(body) != connectorhost.ErrHTTPSend.Error() {
		t.Fatalf("got %d %q %v %v, want 0, the fixed reason, no header, no error", status, body, hdr, err)
	}
	if h.Failed() {
		t.Error("a guest-answered failure marked the call failed")
	}
	if l := auditLines(t, &audit); len(l) != 1 || l[0].Decision != connectorhost.DecisionFailed {
		t.Errorf("audit = %+v", l)
	}
}

func TestHostCallsWithholdSetCookie(t *testing.T) {
	b := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Set-Cookie", "session=s3cret")
		w.Header().Set("X-OAuth-Scopes", "repo")
	})
	n := netFor(b, &bytes.Buffer{}, connectorhost.Route{Method: "GET", Host: b.host(), Path: "/repos"})
	raw := fmt.Sprintf(`{"method":"GET","url":"%s/repos"}`, b.URL)
	_, _, hdr, err := connectorhost.NewHostCalls(n).HTTPRequest(context.Background(), []byte(raw), 1<<10)
	if err != nil {
		t.Fatal(err)
	}
	if hdr.Get("X-OAuth-Scopes") != "repo" || len(hdr.Values("Set-Cookie")) != 0 {
		t.Fatalf("header = %v, want X-OAuth-Scopes kept and Set-Cookie gone", hdr)
	}
}

func TestHostCallsRefusedRouteIsFatalAndMarksTheCallAudited(t *testing.T) {
	b := newBackend(t, func(http.ResponseWriter, *http.Request) {})
	n := netFor(b, &bytes.Buffer{}, connectorhost.Route{Method: "GET", Host: b.host(), Path: "/repos"})
	h := connectorhost.NewHostCalls(n)
	raw := fmt.Sprintf(`{"method":"DELETE","url":"%s/repos"}`, b.URL)
	if _, _, _, err := h.HTTPRequest(context.Background(), []byte(raw), 1<<10); !errors.Is(err, connectorhost.ErrRouteRefused) {
		t.Fatalf("err = %v, want ErrRouteRefused", err)
	}
	if !h.Failed() {
		t.Error("a fatal failure was not marked audited")
	}
}

func TestHostCallsRefuseAuditsARefusedEntry(t *testing.T) {
	b := newBackend(t, func(http.ResponseWriter, *http.Request) {})
	var audit bytes.Buffer
	h := connectorhost.NewHostCalls(netFor(b, &audit))
	err := h.Refuse("http_request", "request points outside module memory")
	if !errors.Is(err, connectorhost.ErrMalformedOutput) {
		t.Fatalf("err = %v, want ErrMalformedOutput", err)
	}
	if !h.Failed() {
		t.Error("a refusal was not marked audited")
	}
	if l := auditLines(t, &audit); len(l) != 1 || l[0].Decision != connectorhost.DecisionRefused {
		t.Errorf("audit = %+v", l)
	}
}

func TestHostCallsOversizedRequestIsRefusedBeforeItIsServed(t *testing.T) {
	b := newBackend(t, func(http.ResponseWriter, *http.Request) {})
	var audit bytes.Buffer
	h := connectorhost.NewHostCalls(netFor(b, &audit))
	raw := make([]byte, connectorhost.MaxRequest+1)
	if _, _, _, err := h.HTTPRequest(context.Background(), raw, 1<<10); !errors.Is(err, connectorhost.ErrMalformedOutput) {
		t.Fatalf("err = %v, want ErrMalformedOutput", err)
	}
	if !h.Failed() {
		t.Error("a refusal was not marked audited")
	}
	if l := auditLines(t, &audit); len(l) != 1 || l[0].Decision != connectorhost.DecisionRefused {
		t.Errorf("audit = %+v", l)
	}
}
