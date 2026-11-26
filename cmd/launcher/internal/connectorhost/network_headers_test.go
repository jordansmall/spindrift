package connectorhost_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
)

func TestRouteValidationRejectsUndeclarableHeaders(t *testing.T) {
	base := connectorhost.Route{Method: "GET", Host: "h.io", Path: "/a"}
	ok := base
	ok.RequestHeaders, ok.ResponseHeaders = []string{"X-GitHub-Api-Version"}, []string{"X-OAuth-Scopes"}
	if err := connectorhost.ValidateRoutes([]connectorhost.Route{ok}); err != nil {
		t.Fatalf("declared extras refused: %v", err)
	}
	for name, c := range map[string]struct {
		req, resp []string
		want      string
	}{
		"cookie":            {req: []string{"Cookie"}, want: `"Cookie" can never be declared`},
		"lowercase cookie":  {req: []string{"cookie"}, want: `"cookie" can never be declared`},
		"authorization":     {req: []string{"Authorization"}, want: `"Authorization" can never be declared`},
		"host":              {req: []string{"Host"}, want: `"Host" can never be declared`},
		"forwarded":         {req: []string{"Forwarded"}, want: `"Forwarded" can never be declared`},
		"via":               {req: []string{"Via"}, want: `"Via" can never be declared`},
		"real ip":           {req: []string{"X-Real-IP"}, want: `"X-Real-IP" can never be declared`},
		"x-forwarded-*":     {req: []string{"X-Forwarded-For"}, want: `"X-Forwarded-For" can never be declared`},
		"proxy-*":           {req: []string{"Proxy-Authorization"}, want: `"Proxy-Authorization" can never be declared`},
		"set-cookie":        {resp: []string{"Set-Cookie"}, want: `"Set-Cookie" can never be declared`},
		"set-cookie2":       {resp: []string{"set-cookie2"}, want: `"set-cookie2" can never be declared`},
		"request token":     {req: []string{"X Bad"}, want: `"X Bad" is not a valid header name`},
		"empty request":     {req: []string{""}, want: "is not a valid header name"},
		"response token":    {resp: []string{"X:Bad"}, want: `"X:Bad" is not a valid header name`},
		"duplicate request": {req: []string{"X-A", "x-a"}, want: `"x-a" is declared twice`},
	} {
		r := base
		r.RequestHeaders, r.ResponseHeaders = c.req, c.resp
		err := connectorhost.ValidateRoutes([]connectorhost.Route{r})
		if !errors.Is(err, connectorhost.ErrRouteTable) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want ErrRouteTable naming %s", name, err, c.want)
		}
	}
}

func TestRouteStringShowsDeclaredHeaders(t *testing.T) {
	r := connectorhost.Route{Method: "GET", Host: "h.io", Path: "/a", RequestHeaders: []string{"X-B", "x-a"}, ResponseHeaders: []string{"X-OAuth-Scopes"}}
	if got, want := r.String(), "GET h.io /a (sends X-A, X-B) (reads X-Oauth-Scopes)"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestNetworkSendsOnlyAllowedRequestHeaders(t *testing.T) {
	route := func(b *backend) connectorhost.Route {
		return connectorhost.Route{Method: "GET", Host: b.host(), Path: "/repos", RequestHeaders: []string{"X-GitHub-Api-Version"}}
	}
	for _, name := range []string{"X-Custom", "Cookie", "cookie", "X-Forwarded-For", "Proxy-Authorization", "Authorization", "Host", "Via", "X-Real-Ip"} {
		t.Run("refuses "+name, func(t *testing.T) {
			b := newBackend(t, func(http.ResponseWriter, *http.Request) {})
			var audit bytes.Buffer
			n := netFor(b, &audit, route(b))
			raw := fmt.Sprintf(`{"method":"GET","url":"%s/repos/x","headers":{"Accept":"text/plain","%s":"v"}}`, b.URL, name)
			_, _, err := n.Do(context.Background(), []byte(raw), 1<<10)
			if !errors.Is(err, connectorhost.ErrRouteRefused) || !strings.Contains(err.Error(), name) {
				t.Fatalf("err = %v, want ErrRouteRefused naming %q", err, name)
			}
			if b.hits.Load() != 0 {
				t.Errorf("backend saw %d requests, want none", b.hits.Load())
			}
			lines := auditLines(t, &audit)
			if len(lines) != 1 || lines[0].Decision != connectorhost.DecisionRefused || !strings.Contains(lines[0].Reason, name) || len(lines[0].Headers) != 0 {
				t.Errorf("audit = %+v, want one refused entry naming %q and no sent headers", lines, name)
			}
		})
	}
	t.Run("sends the fixed set and a declared extra", func(t *testing.T) {
		b := newBackend(t, func(http.ResponseWriter, *http.Request) {})
		var audit bytes.Buffer
		n := netFor(b, &audit, route(b))
		raw := fmt.Sprintf(`{"method":"GET","url":"%s/repos/x","headers":{"accept":"text/plain","If-None-Match":"\"e\"","x-github-api-version":"2022-11-28"}}`, b.URL)
		if _, _, err := n.Do(context.Background(), []byte(raw), 1<<10); err != nil {
			t.Fatal(err)
		}
		r := b.last.Load()
		if r.Header.Get("Accept") != "text/plain" || r.Header.Get("If-None-Match") != `"e"` || r.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
			t.Errorf("headers = %v, want the allowed ones sent", r.Header)
		}
		want := []string{"Accept", "Authorization", "If-None-Match", "X-Github-Api-Version"}
		if lines := auditLines(t, &audit); len(lines) != 1 || !reflect.DeepEqual(lines[0].Headers, want) {
			t.Errorf("audit = %+v, want headers %v", lines, want)
		}
		if strings.Contains(audit.String(), "s3cret") || strings.Contains(audit.String(), "2022-11-28") {
			t.Error("a header value reached the audit log")
		}
	})
}

// The allowed set is per Route: a header declared on one route is refused on
// another to the same host.
func TestNetworkRefusesAHeaderDeclaredOnAnotherRoute(t *testing.T) {
	b := newBackend(t, func(http.ResponseWriter, *http.Request) {})
	var audit bytes.Buffer
	n := netFor(b, &audit,
		connectorhost.Route{Method: "GET", Host: b.host(), Path: "/repos", RequestHeaders: []string{"X-GitHub-Api-Version"}},
		connectorhost.Route{Method: "POST", Host: b.host(), Path: "/repos"})
	const hdr = "X-GitHub-Api-Version"
	post := fmt.Sprintf(`{"method":"POST","url":"%s/repos/x","headers":{"%s":"v"},"body":"{}"}`, b.URL, hdr)
	_, _, err := n.Do(context.Background(), []byte(post), 1<<10)
	if !errors.Is(err, connectorhost.ErrRouteRefused) || !strings.Contains(err.Error(), hdr) {
		t.Fatalf("POST err = %v, want ErrRouteRefused naming %q", err, hdr)
	}
	if b.hits.Load() != 0 {
		t.Errorf("backend saw %d requests, want none", b.hits.Load())
	}
	if lines := auditLines(t, &audit); len(lines) != 1 || lines[0].Decision != connectorhost.DecisionRefused {
		t.Errorf("audit = %+v, want one refused entry", lines)
	}
	get := fmt.Sprintf(`{"method":"GET","url":"%s/repos/x","headers":{"%s":"v"}}`, b.URL, hdr)
	if _, _, err := n.Do(context.Background(), []byte(get), 1<<10); err != nil {
		t.Fatalf("GET err = %v, want the declared header sent", err)
	}
	if b.hits.Load() != 1 || b.last.Load().Header.Get(hdr) != "v" {
		t.Errorf("hits = %d, header = %q, want GET sent with it", b.hits.Load(), b.last.Load().Header.Get(hdr))
	}
}

// A guest's refused header never reaches the backend, on each engine, and the
// refusal is audited as such; an allowed one does.
func TestNetworkConnectorSendsOnlyAllowedRequestHeaders(t *testing.T) {
	for name, load := range netLoadersFor(t) {
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				header  string
				allowed bool
			}{
				{"X-Custom", false}, {"Cookie", false}, {"X-Forwarded-For", false},
				{"Accept", true}, {"X-GitHub-Api-Version", true},
			} {
				t.Run(tc.header, func(t *testing.T) {
					b := newBackend(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"result":{}}`) })
					var audit bytes.Buffer
					req := fmt.Sprintf(`{"method":"GET","url":"%s/repos/x","headers":{"%s":"v"}}`, b.URL, tc.header)
					n := netFor(b, &audit, connectorhost.Route{Method: "GET", Host: b.host(), Path: "/repos", RequestHeaders: []string{"X-GitHub-Api-Version"}})
					c := loadNetWith(t, load, httpCallGuest(req, netRespOff, 4096, true), n)
					err := c.Call(context.Background(), "m", nil, new(struct{}))
					lines := auditLines(t, &audit)
					if len(lines) != 1 {
						t.Fatalf("audit = %+v, want one entry", lines)
					}
					if tc.allowed {
						if err != nil || b.hits.Load() != 1 || b.last.Load().Header.Get(tc.header) != "v" || lines[0].Decision != connectorhost.DecisionSent {
							t.Errorf("err = %v, hits = %d, audit = %+v: want %s sent", err, b.hits.Load(), lines, tc.header)
						}
						if !slices.Contains(lines[0].Headers, http.CanonicalHeaderKey(tc.header)) {
							t.Errorf("audit headers = %v, want %s named", lines[0].Headers, tc.header)
						}
						return
					}
					if !errors.Is(err, connectorhost.ErrRouteRefused) || b.hits.Load() != 0 ||
						lines[0].Decision != connectorhost.DecisionRefused || !strings.Contains(lines[0].Reason, tc.header) {
						t.Errorf("err = %v, hits = %d, audit = %+v: want %s refused and audited", err, b.hits.Load(), lines, tc.header)
					}
				})
			}
		})
	}
}

func TestNetworkFixtureReadsOnlyAllowedResponseHeaders(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, header string
		declared     []string
		want         *string
	}{
		{"fixed ETag passes", "ETag", nil, ptr(`"v1"`)},
		{"undeclared extra is absent", "X-OAuth-Scopes", nil, nil},
		{"declared extra passes", "X-OAuth-Scopes", []string{"X-OAuth-Scopes"}, ptr("repo")},
		{"other extra stays absent", "X-RateLimit-Remaining", []string{"X-OAuth-Scopes"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _, _, params := loadNetworkFixtureWith(t, "network.wasm", func(n *connectorhost.Network) {
				n.Routes[0].ResponseHeaders = tc.declared
			})
			params["header"] = tc.header
			var out fetched
			if err := c.Call(context.Background(), "fetch", params, &out); err != nil {
				t.Fatal(err)
			}
			if (out.Header == nil) != (tc.want == nil) || (tc.want != nil && *out.Header != *tc.want) {
				t.Errorf("header %q = %v, want %v", tc.header, out.Header, tc.want)
			}
		})
	}
}

func TestRouteValidationChecksDeclaredDocuments(t *testing.T) {
	post := connectorhost.Route{Method: "POST", Host: "h.io", Path: "/graphql"}
	ok := post
	ok.Documents = map[string]string{"closeIssue": "mutation($id:ID!){x}", "Viewer_2": "query{viewer{login}}"}
	if err := connectorhost.ValidateRoutes([]connectorhost.Route{ok}); err != nil {
		t.Fatalf("declared documents refused: %v", err)
	}
	for name, c := range map[string]struct {
		method string
		docs   map[string]string
		want   string
	}{
		"empty":          {docs: map[string]string{}, want: "documents: must declare at least one"},
		"bad name":       {docs: map[string]string{"1bad": "query{a}"}, want: `documents: "1bad" is not a valid name`},
		"name with dash": {docs: map[string]string{"a-b": "query{a}"}, want: `documents: "a-b" is not a valid name`},
		"empty name":     {docs: map[string]string{"": "query{a}"}, want: "is not a valid name"},
		"empty text":     {docs: map[string]string{"a": ""}, want: `documents: "a" has no text`},
		"duplicate text": {docs: map[string]string{"a": "query{a}", "b": "query{a}"}, want: `documents: "a" and "b" declare the same text`},
		"get":            {method: "GET", docs: map[string]string{"a": "query{a}"}, want: "documents are allowed on POST only"},
	} {
		r := post
		if c.method != "" {
			r.Method = c.method
		}
		r.Documents = c.docs
		err := connectorhost.ValidateRoutes([]connectorhost.Route{r})
		if !errors.Is(err, connectorhost.ErrRouteTable) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want ErrRouteTable naming %s", name, err, c.want)
		}
	}
}

func TestRouteStringShowsDeclaredDocuments(t *testing.T) {
	r := connectorhost.Route{Method: "POST", Host: "h.io", Path: "/graphql", RequestHeaders: []string{"X-A"}, Documents: map[string]string{"b": "query{b}", "a": "query{a}"}}
	if got, want := r.String(), "POST h.io /graphql (sends X-A) (documents a@3b14b07a, b@0a25111c)"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}
