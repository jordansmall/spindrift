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

// A refusal that cannot name a host and path still names what it can, so an
// operator reading the log or the audit reason can tell which request it was.
func TestNetworkRefusalNamesWhatItCan(t *testing.T) {
	b := newBackend(t, func(http.ResponseWriter, *http.Request) {})
	route := connectorhost.Route{Method: "GET", Host: b.host(), Path: "/repos"}
	cases := map[string]struct {
		raw  string
		want []string
	}{
		"write to undeclared path": {fmt.Sprintf(`{"method":"POST","url":"%s/admin"}`, b.URL), []string{"POST", b.host(), "/admin"}},
		"write to undeclared host": {`{"method":"PUT","url":"https://api.example.com/v1/x"}`, []string{"PUT", "api.example.com", "/v1/x"}},
		"unparseable url":          {`{"method":"POST","url":"https://exa mple.com/x"}`, []string{"POST", "unparseable"}},
		"plaintext":                {`{"method":"POST","url":"http://api.example.com/x"}`, []string{"POST", "api.example.com", "/x"}},
		"unknown field":            {`{"method":"GET","cred":"x"}`, []string{"cred"}},
		"truncated":                {`{"method":`, []string{"unexpected"}},
		"trailing data":            {`{"method":"GET"}{}`, []string{"trailing"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var audit bytes.Buffer
			n := netFor(b, &audit, route)
			_, _, err := n.Do(context.Background(), []byte(tc.raw), 1<<10)
			if !errors.Is(err, connectorhost.ErrRouteRefused) {
				t.Fatalf("err = %v, want ErrRouteRefused", err)
			}
			lines := auditLines(t, &audit)
			if len(lines) != 1 {
				t.Fatalf("audit = %+v, want one entry", lines)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %v lacks %q", err, want)
				}
				if !strings.Contains(lines[0].Reason, want) {
					t.Errorf("audit reason %q lacks %q", lines[0].Reason, want)
				}
			}
		})
	}
}

func TestNetworkRefusalDoesNotEchoAHostileURL(t *testing.T) {
	b := newBackend(t, func(http.ResponseWriter, *http.Request) {})
	hostile := "https://exa mple.com/x?secret=" + strings.Repeat("A", 4096)
	raw, err := json.Marshal(map[string]string{"method": "POST", "url": hostile})
	if err != nil {
		t.Fatal(err)
	}
	var audit bytes.Buffer
	n := netFor(b, &audit, connectorhost.Route{Method: "GET", Host: b.host(), Path: "/repos"})
	_, _, derr := n.Do(context.Background(), raw, 1<<20)
	if !errors.Is(derr, connectorhost.ErrRouteRefused) {
		t.Fatalf("err = %v, want ErrRouteRefused", derr)
	}
	lines := auditLines(t, &audit)
	if len(lines) != 1 {
		t.Fatalf("audit = %+v, want one entry", lines)
	}
	for _, got := range []string{derr.Error(), lines[0].Reason} {
		if strings.Contains(got, "secret") || len(got) > 200 {
			t.Errorf("refusal echoes the guest url: %.200q (len %d)", got, len(got))
		}
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

func TestNetworkAuditShadowMarkAndObserve(t *testing.T) {
	b := newBackend(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") })
	route := connectorhost.Route{Method: "GET", Host: b.host(), Path: "/repos"}
	sent := []byte(fmt.Sprintf(`{"method":"GET","url":"%s/repos"}`, b.URL))
	for _, shadow := range []bool{false, true} {
		var audit bytes.Buffer
		var seen []connectorhost.AuditEntry
		n := netFor(b, &audit, route)
		n.Shadow = shadow
		n.Observe = func(e connectorhost.AuditEntry) { seen = append(seen, e) }
		if _, _, err := n.Do(context.Background(), sent, 1<<10); err != nil {
			t.Fatal(err)
		}
		if _, _, err := n.Do(context.Background(), []byte(`{`), 1<<10); !errors.Is(err, connectorhost.ErrRouteRefused) {
			t.Fatal(err)
		}
		n.RecordFailure(connectorhost.DecisionFailed, errors.New("boom"))
		lines := auditLines(t, &audit)
		if len(lines) != 3 || len(seen) != 3 {
			t.Fatalf("shadow=%v: %d audit lines, %d observed, want 3 each", shadow, len(lines), len(seen))
		}
		for i := range lines {
			if lines[i].Shadow != shadow || seen[i] != lines[i] {
				t.Errorf("shadow=%v entry %d: audit %+v, observed %+v", shadow, i, lines[i], seen[i])
			}
		}
		if got := strings.Contains(audit.String(), `"shadow":true`); got != shadow {
			t.Errorf("shadow=%v: shadow key present = %v in %q", shadow, got, audit.String())
		}
		if !shadow && strings.Contains(audit.String(), `"shadow"`) {
			t.Errorf("non-shadow audit carries a shadow key: %q", audit.String())
		}
	}
}

func TestNetworkAuditClassifiesRateLimits(t *testing.T) {
	cases := map[string]struct {
		status         int
		headers        map[string]string
		body           string
		wantRateLimit  string
		wantRetryAfter string
	}{
		"primary":              {403, map[string]string{"X-RateLimit-Remaining": "0"}, "", "primary", ""},
		"primary beats retry":  {403, map[string]string{"X-RateLimit-Remaining": "0", "Retry-After": "30"}, "", "primary", "30"},
		"secondary header":     {403, map[string]string{"Retry-After": "60"}, "", "secondary", "60"},
		"secondary 429 header": {429, map[string]string{"Retry-After": "5"}, "", "secondary", "5"},
		"secondary body 403":   {403, nil, `{"message":"You have exceeded a Secondary Rate Limit"}`, "secondary", ""},
		"secondary body 429":   {429, nil, "You have exceeded a secondary rate limit", "secondary", ""},
		"plain 403":            {403, map[string]string{"X-RateLimit-Remaining": "4999"}, `{"message":"Forbidden"}`, "", ""},
		"plain 429":            {429, nil, "", "secondary", ""},
		"ok ignores headers":   {200, map[string]string{"X-RateLimit-Remaining": "0", "Retry-After": "9"}, "", "", ""},
		"404 ignores headers":  {404, map[string]string{"Retry-After": "9"}, "secondary rate limit", "", ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			b := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
				for k, v := range c.headers {
					w.Header().Set(k, v)
				}
				w.WriteHeader(c.status)
				_, _ = io.WriteString(w, c.body)
			})
			var audit bytes.Buffer
			n := netFor(b, &audit, connectorhost.Route{Method: "GET", Host: b.host(), Path: "/repos"})
			status, _, err := n.Do(context.Background(), []byte(fmt.Sprintf(`{"method":"GET","url":"%s/repos"}`, b.URL)), 1<<10)
			if err != nil || status != c.status {
				t.Fatalf("got %d %v", status, err)
			}
			l := auditLines(t, &audit)
			if len(l) != 1 || l[0].RateLimit != c.wantRateLimit || l[0].RetryAfter != c.wantRetryAfter {
				t.Errorf("audit = %+v, want rate_limit %q retry_after %q", l, c.wantRateLimit, c.wantRetryAfter)
			}
		})
	}
}
