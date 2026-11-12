package connectorhost_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/registryvocab"
)

func TestRouteTableValidation(t *testing.T) {
	good := connectorhost.Route{Method: "GET", Host: "api.example.com", Path: "/repos"}
	if err := connectorhost.ValidateRoutes([]connectorhost.Route{good, {Method: "POST", Host: "127.0.0.1:8443", Path: "/a/b"}}); err != nil {
		t.Fatalf("good table refused: %v", err)
	}
	bad := map[string]struct {
		r    connectorhost.Route
		want error
	}{
		"blanket root":   {connectorhost.Route{"GET", "h.io", "/"}, registryvocab.ErrPatternBlanketRoot},
		"trailing slash": {connectorhost.Route{"GET", "h.io", "/a/"}, registryvocab.ErrPatternNotCanonical},
		"dot-dot":        {connectorhost.Route{"GET", "h.io", "/a/../b"}, registryvocab.ErrPatternNotCanonical},
		"relative":       {connectorhost.Route{"GET", "h.io", "a/b"}, registryvocab.ErrPatternNotCanonical},
		"query":          {connectorhost.Route{"GET", "h.io", "/a?x"}, registryvocab.ErrPatternURLDelimiter},
		"fragment":       {connectorhost.Route{"GET", "h.io", "/a#x"}, registryvocab.ErrPatternURLDelimiter},
		"lowercase verb": {connectorhost.Route{"get", "h.io", "/a"}, nil},
		"empty verb":     {connectorhost.Route{"", "h.io", "/a"}, nil},
		"empty host":     {connectorhost.Route{"GET", "", "/a"}, nil},
		"scheme host":    {connectorhost.Route{"GET", "https://h.io", "/a"}, nil},
		"path in host":   {connectorhost.Route{"GET", "h.io/x", "/a"}, nil},
		"userinfo host":  {connectorhost.Route{"GET", "u@h.io", "/a"}, nil},
		"empty port":     {connectorhost.Route{"GET", "h.io:", "/a"}, nil},
	}
	for name, c := range bad {
		err := connectorhost.ValidateRoutes([]connectorhost.Route{good, c.r})
		if !errors.Is(err, connectorhost.ErrRouteTable) {
			t.Errorf("%s: err = %v, want ErrRouteTable", name, err)
			continue
		}
		if c.want != nil && !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", name, err, c.want)
		}
		if !strings.Contains(err.Error(), c.r.Host) {
			t.Errorf("%s: error does not name the route: %v", name, err)
		}
	}
}

// backend is a TLS server counting the requests it saw.
type backend struct {
	*httptest.Server
	hits atomic.Int32
	last atomic.Pointer[http.Request]
}

func newBackend(t *testing.T, h http.HandlerFunc) *backend {
	t.Helper()
	b := &backend{}
	b.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.hits.Add(1)
		b.last.Store(r)
		h(w, r)
	}))
	t.Cleanup(b.Close)
	return b
}

func (b *backend) host() string { return b.Listener.Addr().String() }

func netFor(b *backend, audit *bytes.Buffer, routes ...connectorhost.Route) *connectorhost.Network {
	return &connectorhost.Network{
		Name:   "demo",
		Routes: routes,
		Authorize: func(r *http.Request) error {
			r.Header.Set("Authorization", "Bearer s3cret")
			return nil
		},
		Client: b.Client(),
		Audit:  connectorhost.NewAuditLog(audit),
	}
}

func auditLines(t *testing.T, buf *bytes.Buffer) []connectorhost.AuditEntry {
	t.Helper()
	var out []connectorhost.AuditEntry
	for _, l := range strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n") {
		if l == "" {
			continue
		}
		var e connectorhost.AuditEntry
		if err := json.Unmarshal([]byte(l), &e); err != nil {
			t.Fatalf("audit line %q: %v", l, err)
		}
		out = append(out, e)
	}
	return out
}

func TestNetworkSendsAdmittedRequestWithHostCredential(t *testing.T) {
	b := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello")
	})
	var audit bytes.Buffer
	n := netFor(b, &audit, connectorhost.Route{Method: "GET", Host: b.host(), Path: "/repos"})
	raw := fmt.Sprintf(`{"method":"GET","url":"%s/repos/x?q=1","headers":{"Accept":"text/plain","Authorization":"Bearer guest"}}`, b.URL)
	status, body, err := n.Do(context.Background(), []byte(raw), 1<<10)
	if err != nil {
		t.Fatal(err)
	}
	if status != 200 || string(body) != "hello" {
		t.Fatalf("got %d %q", status, body)
	}
	r := b.last.Load()
	if got := r.Header.Get("Authorization"); got != "Bearer s3cret" {
		t.Errorf("Authorization = %q, want the host credential", got)
	}
	if r.Header.Get("Accept") != "text/plain" || r.URL.RawQuery != "q=1" || r.URL.Path != "/repos/x" {
		t.Errorf("request not forwarded: %v %v", r.Header, r.URL)
	}
	lines := auditLines(t, &audit)
	want := connectorhost.AuditEntry{Connector: "demo", Route: "GET " + b.host() + " /repos", Method: "GET", Host: b.host(), Path: "/repos/x", Query: "q=1", Status: 200, Size: 5, Decision: connectorhost.DecisionSent}
	if len(lines) != 1 || lines[0] != want {
		t.Fatalf("audit = %+v, want [%+v]", lines, want)
	}
	if strings.Contains(audit.String(), "s3cret") {
		t.Error("credential leaked into the audit log")
	}
}

func TestNetworkRefusesUnroutedRequestsWithoutSending(t *testing.T) {
	b := newBackend(t, func(w http.ResponseWriter, r *http.Request) {})
	route := connectorhost.Route{Method: "GET", Host: b.host(), Path: "/repos"}
	cases := map[string]string{
		"undeclared host":   `{"method":"GET","url":"https://other.example.com/repos"}`,
		"undeclared method": fmt.Sprintf(`{"method":"POST","url":"%s/repos"}`, b.URL),
		"undeclared path":   fmt.Sprintf(`{"method":"GET","url":"%s/admin"}`, b.URL),
		"sibling prefix":    fmt.Sprintf(`{"method":"GET","url":"%s/reposx"}`, b.URL),
		"plaintext":         fmt.Sprintf(`{"method":"GET","url":"http://%s/repos"}`, b.host()),
		"userinfo":          fmt.Sprintf(`{"method":"GET","url":"https://u:p@%s/repos"}`, b.host()),
		"fragment":          fmt.Sprintf(`{"method":"GET","url":"%s/repos#x"}`, b.URL),
		"encoded smuggle":   fmt.Sprintf(`{"method":"GET","url":"%s/repos%%2F..%%2Fadmin"}`, b.URL),
		"escaped dot":       fmt.Sprintf(`{"method":"GET","url":"%s/repos/%%2e%%2e/admin"}`, b.URL),
		"dot-dot":           fmt.Sprintf(`{"method":"GET","url":"%s/repos/../admin"}`, b.URL),
		"semicolon":         fmt.Sprintf(`{"method":"GET","url":"%s/repos/..;/admin"}`, b.URL),
		"no path":           fmt.Sprintf(`{"method":"GET","url":"%s"}`, b.URL),
		"unknown field":     fmt.Sprintf(`{"method":"GET","url":"%s/repos","cred":"x"}`, b.URL),
		"malformed":         `{"method":`,
		"trailing data":     fmt.Sprintf(`{"method":"GET","url":"%s/repos"}{}`, b.URL),
	}
	for name, raw := range cases {
		var audit bytes.Buffer
		n := netFor(b, &audit, route)
		status, body, err := n.Do(context.Background(), []byte(raw), 1<<10)
		if !errors.Is(err, connectorhost.ErrRouteRefused) || status != 0 || body != nil {
			t.Errorf("%s: got %d %q %v, want ErrRouteRefused", name, status, body, err)
			continue
		}
		if !strings.Contains(err.Error(), `"demo"`) {
			t.Errorf("%s: error does not name the Connector: %v", name, err)
		}
		lines := auditLines(t, &audit)
		if len(lines) != 1 || lines[0].Decision != connectorhost.DecisionRefused || lines[0].Route != "" || lines[0].Status != 0 || lines[0].Reason == "" {
			t.Errorf("%s: audit = %+v", name, lines)
		}
	}
	if got := b.hits.Load(); got != 0 {
		t.Fatalf("backend saw %d requests, want 0", got)
	}
}

func TestNetworkRefusalNamesMethodHostAndPath(t *testing.T) {
	b := newBackend(t, func(http.ResponseWriter, *http.Request) {})
	var audit bytes.Buffer
	n := netFor(b, &audit, connectorhost.Route{Method: "GET", Host: b.host(), Path: "/repos"})
	_, _, err := n.Do(context.Background(), []byte(`{"method":"DELETE","url":"https://api.example.com/v1/x"}`), 1<<10)
	for _, want := range []string{`"demo"`, "DELETE", "api.example.com", "/v1/x"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("error %v lacks %q", err, want)
		}
	}
	e := auditLines(t, &audit)[0]
	if e.Method != "DELETE" || e.Host != "api.example.com" || e.Path != "/v1/x" {
		t.Errorf("audit = %+v", e)
	}
}

func TestNetworkDoesNotFollowRedirects(t *testing.T) {
	b := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin", http.StatusFound)
	})
	var audit bytes.Buffer
	n := netFor(b, &audit, connectorhost.Route{Method: "GET", Host: b.host(), Path: "/repos"})
	status, _, err := n.Do(context.Background(), []byte(fmt.Sprintf(`{"method":"GET","url":"%s/repos"}`, b.URL)), 1<<10)
	if err != nil || status != http.StatusFound {
		t.Fatalf("got %d %v, want 302", status, err)
	}
	if got := b.hits.Load(); got != 1 {
		t.Fatalf("backend hit %d times, want 1", got)
	}
	if n.Client.CheckRedirect != nil {
		t.Error("caller's client was mutated")
	}
}

func TestNetworkOversizedResponseFails(t *testing.T) {
	b := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", 11))
	})
	var audit bytes.Buffer
	n := netFor(b, &audit, connectorhost.Route{Method: "GET", Host: b.host(), Path: "/repos"})
	_, body, err := n.Do(context.Background(), []byte(fmt.Sprintf(`{"method":"GET","url":"%s/repos"}`, b.URL)), 10)
	if !errors.Is(err, connectorhost.ErrResponseTooLarge) || body != nil {
		t.Fatalf("got %q %v, want ErrResponseTooLarge", body, err)
	}
	if l := auditLines(t, &audit); len(l) != 1 || l[0].Decision != connectorhost.DecisionFailed || l[0].Size != 11 {
		t.Errorf("audit = %+v", l)
	}
}

func TestNetworkTransportFailureAndAuthorizeFailure(t *testing.T) {
	b := newBackend(t, func(http.ResponseWriter, *http.Request) {})
	route := connectorhost.Route{Method: "GET", Host: b.host(), Path: "/repos"}
	raw := []byte(fmt.Sprintf(`{"method":"GET","url":"%s/repos"}`, b.URL))

	var audit bytes.Buffer
	n := netFor(b, &audit, route)
	n.Client = &http.Client{} // does not trust the test certificate
	if _, _, err := n.Do(context.Background(), raw, 10); !errors.Is(err, connectorhost.ErrHTTPSend) {
		t.Errorf("transport: err = %v, want ErrHTTPSend", err)
	}
	if l := auditLines(t, &audit); len(l) != 1 || l[0].Decision != connectorhost.DecisionFailed || l[0].Route == "" {
		t.Errorf("audit = %+v", l)
	}

	audit.Reset()
	n = netFor(b, &audit, route)
	n.Authorize = func(*http.Request) error { return errors.New("no token") }
	if _, _, err := n.Do(context.Background(), raw, 10); !errors.Is(err, connectorhost.ErrHTTPSend) {
		t.Errorf("authorize: err = %v, want ErrHTTPSend", err)
	}
	if b.hits.Load() != 0 {
		t.Error("request sent despite failed authorization")
	}
}

func TestAuditLogConcurrentRecordsAreWholeLines(t *testing.T) {
	var buf bytes.Buffer
	log := connectorhost.NewAuditLog(&buf)
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			log.Record(connectorhost.AuditEntry{Connector: "c", Path: strings.Repeat("p", 100+i), Decision: connectorhost.DecisionSent})
		}()
	}
	wg.Wait()
	if l := auditLines(t, &buf); len(l) != 50 {
		t.Fatalf("decoded %d lines, want 50", len(l))
	}
}
