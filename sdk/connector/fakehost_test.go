//go:build !wasip1

package connector

import (
	"errors"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

var testRoutes = []Route{
	{Method: "GET", Host: "jira.example.com", Path: "/rest/api/2"},
	{Method: "POST", Host: "jira.example.com", Path: "/rest/api/2/issue"},
	{Method: "GET", Host: "other.example.com:8443", Path: "/"},
}

func TestAdmit(t *testing.T) {
	h := NewFakeHost(testRoutes)
	const methodReason = "method is not one of GET, HEAD, POST, PUT, PATCH, DELETE"
	tests := []struct {
		name, method, url string
		want              string // empty: admitted; else the exact reason
	}{
		{"root itself", "GET", "https://jira.example.com/rest/api/2", ""},
		{"under root", "GET", "https://jira.example.com/rest/api/2/issue/X-1", ""},
		{"host case-insensitive", "GET", "https://JIRA.example.com/rest/api/2", ""},
		{"blanket root with port", "GET", "https://other.example.com:8443/anything/at/all", ""},
		{"query is not path", "GET", "https://jira.example.com/rest/api/2?x=1", ""},
		{"method outside the set", "TRACE", "https://jira.example.com/rest/api/2", methodReason},
		{"method is exact", "get", "https://jira.example.com/rest/api/2", methodReason},
		{"wrong method for route", "POST", "https://jira.example.com/rest/api/2", "no route admits POST jira.example.com/rest/api/2"},
		{"http scheme", "GET", "http://jira.example.com/rest/api/2", "GET jira.example.com/rest/api/2: only https urls without userinfo or fragment are allowed"},
		{"userinfo", "GET", "https://u:p@jira.example.com/rest/api/2", "GET jira.example.com/rest/api/2: only https urls without userinfo or fragment are allowed"},
		{"fragment", "GET", "https://jira.example.com/rest/api/2#f", "GET jira.example.com/rest/api/2: only https urls without userinfo or fragment are allowed"},
		{"unparseable", "GET", "https://jira.example.com/%zz", "GET: unparseable url"},
		{"wrong host", "GET", "https://evil.example.com/rest/api/2", "no route admits GET evil.example.com/rest/api/2"},
		{"port is significant", "GET", "https://other.example.com/x", "no route admits GET other.example.com/x"},
		{"dot-dot", "GET", "https://jira.example.com/rest/api/2/a/../b", "no route admits GET jira.example.com/rest/api/2/a/../b"},
		{"trailing slash", "GET", "https://jira.example.com/rest/api/2/", "no route admits GET jira.example.com/rest/api/2/"},
		{"double slash", "GET", "https://jira.example.com/rest/api/2/a//b", "no route admits GET jira.example.com/rest/api/2/a//b"},
		{"escaped slash", "GET", "https://jira.example.com/rest/api/2/a%2Fb", "no route admits GET jira.example.com/rest/api/2/a/b"},
		{"empty path", "GET", "https://jira.example.com", "no route admits GET jira.example.com"},
		{"outside the subtree", "GET", "https://jira.example.com/rest/api/2issuefoo", "no route admits GET jira.example.com/rest/api/2issuefoo"},
		{"sibling prefix", "POST", "https://jira.example.com/rest/api/2/issuefoo", "no route admits POST jira.example.com/rest/api/2/issuefoo"},
		{"semicolon", "GET", "https://jira.example.com/rest/api/2/..;/x", "no route admits GET jira.example.com/rest/api/2/..;/x"},
		{"overlong url", "GET", "https://jira.example.com/rest/api/2/" + strings.Repeat("a", 8<<10), "url is 8228 bytes, over the 8192-byte limit"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := h.Admit(Request{Method: tc.method, URL: tc.url})
			if tc.want == "" {
				if err != nil {
					t.Fatalf("Admit = %v, want admitted", err)
				}
				return
			}
			if !errors.Is(err, ErrRouteRefused) {
				t.Fatalf("err = %v, want ErrRouteRefused", err)
			}
			if want := ErrRouteRefused.Error() + ": " + tc.want; err.Error() != want {
				t.Errorf("err = %q, want %q", err, want)
			}
		})
	}
}

// stubTB records Fatalf and unwinds the goroutine like testing.T does.
type stubTB struct {
	cleanups []func()
	fatal    string
}

func (s *stubTB) Helper()          {}
func (s *stubTB) Cleanup(f func()) { s.cleanups = append(s.cleanups, f) }
func (s *stubTB) Fatalf(format string, args ...any) {
	s.fatal = format
	if len(args) > 0 {
		s.fatal = args[0].(error).Error()
	}
	runtime.Goexit()
}

func (s *stubTB) runCleanups() {
	for i := len(s.cleanups) - 1; i >= 0; i-- {
		s.cleanups[i]()
	}
}

// inGoroutine runs f where a Goexit from stubTB.Fatalf unwinds only f.
func inGoroutine(f func()) {
	done := make(chan struct{})
	go func() { defer close(done); f() }()
	<-done
}

func TestServeAnswersAndRecordsAnAdmittedRequest(t *testing.T) {
	h := NewFakeHost(testRoutes)
	sent := h.Serve(t, func(req Request) (Response, error) {
		return Response{Status: 201, Body: []byte("made " + req.URL)}, nil
	})
	resp, err := HTTPRequest(Request{Method: "POST", URL: "https://jira.example.com/rest/api/2/issue", Body: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 201 || string(resp.Body) != "made https://jira.example.com/rest/api/2/issue" {
		t.Errorf("resp = %d %q", resp.Status, resp.Body)
	}
	if len(*sent) != 1 || (*sent)[0].Method != "POST" || (*sent)[0].Body != "x" {
		t.Errorf("sent = %+v, want the one POST", *sent)
	}
}

func TestServeAnswerErrorBecomesTheHTTPRequestError(t *testing.T) {
	h := NewFakeHost(testRoutes)
	h.Serve(t, func(Request) (Response, error) { return Response{}, errors.New("dial refused") })
	_, err := HTTPRequest(Request{Method: "GET", URL: "https://jira.example.com/rest/api/2"})
	if err == nil || err.Error() != "dial refused" {
		t.Fatalf("err = %v, want the answer's text", err)
	}
}

func TestServeRefusedRequestFailsTheTestAndIsNotAnswered(t *testing.T) {
	h := NewFakeHost(testRoutes)
	tb := &stubTB{}
	answered := false
	var sent *[]Request
	inGoroutine(func() {
		sent = h.Serve(tb, func(Request) (Response, error) { answered = true; return Response{Status: 200}, nil })
		HTTPRequest(Request{Method: "GET", URL: "https://evil.example.com/x"})
		t.Error("HTTPRequest returned after a refusal")
	})
	tb.runCleanups()
	if !strings.HasSuffix(tb.fatal, "no route admits GET evil.example.com/x") {
		t.Errorf("Fatalf = %q, want the refusal", tb.fatal)
	}
	if answered || len(*sent) != 0 {
		t.Errorf("answered=%v sent=%v, want neither", answered, *sent)
	}
}

func TestUnusedListsDeclaredRoutesNoAdmittedRequestMatched(t *testing.T) {
	h := NewFakeHost(testRoutes)
	if got := h.Unused(); !reflect.DeepEqual(got, testRoutes) {
		t.Fatalf("Unused before any request = %v, want all", got)
	}
	h.Serve(t, func(Request) (Response, error) { return Response{Status: 200}, nil })
	for _, r := range []Request{
		{Method: "GET", URL: "https://jira.example.com/rest/api/2/search"},
		{Method: "GET", URL: "https://other.example.com:8443/x"},
	} {
		if _, err := HTTPRequest(r); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := h.Unused(), testRoutes[1:2]; !reflect.DeepEqual(got, want) {
		t.Errorf("Unused = %v, want %v", got, want)
	}
}

func TestServeRestoresHostCallOnCleanup(t *testing.T) {
	h := NewFakeHost(testRoutes)
	tb := &stubTB{}
	h.Serve(tb, func(Request) (Response, error) { return Response{Status: 200}, nil })
	tb.runCleanups()
	if _, err := HTTPRequest(Request{Method: "GET", URL: "https://jira.example.com/rest/api/2"}); !errors.Is(err, ErrNoHost) {
		t.Errorf("err = %v, want ErrNoHost once the fake is gone", err)
	}
}

func TestLoadRoutes(t *testing.T) {
	t.Setenv(RoutesEnv, `[{"method":"GET","host":"h","path":"/p","followRedirect":true}]`)
	got, err := LoadRoutes()
	if err != nil {
		t.Fatal(err)
	}
	if want := []Route{{Method: "GET", Host: "h", Path: "/p", FollowRedirect: true}}; !reflect.DeepEqual(got, want) {
		t.Errorf("routes = %v, want %v", got, want)
	}
	for name, val := range map[string]string{"unset": "", "unknown field": `[{"method":"GET","hosts":"h"}]`, "not json": "nope"} {
		t.Setenv(RoutesEnv, val)
		if _, err := LoadRoutes(); err == nil || !strings.Contains(err.Error(), RoutesEnv) {
			t.Errorf("%s: err = %v, want one naming %s", name, err, RoutesEnv)
		}
	}
}

func TestResolveRoutes(t *testing.T) {
	routes := []Route{
		{Method: "GET", Host: "{base}", Path: "/"},
		{Method: "GET", Host: "{base}", Path: "/api/v1/repos/{repo}/issues"},
		{Method: "GET", Host: "fixed.example.com", Path: "/x"},
	}
	got, err := ResolveRoutes(routes, "https://git.example.com", "o/r")
	if err != nil {
		t.Fatal(err)
	}
	want := []Route{
		{Method: "GET", Host: "git.example.com", Path: "/"},
		{Method: "GET", Host: "git.example.com", Path: "/api/v1/repos/o/r/issues"},
		{Method: "GET", Host: "fixed.example.com", Path: "/x"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("no prefix: got %v, want %v", got, want)
	}
	got, err = ResolveRoutes(routes, "https://git.example.com:8443/gitea/", "o/r")
	if err != nil {
		t.Fatal(err)
	}
	want = []Route{
		{Method: "GET", Host: "git.example.com:8443", Path: "/gitea"},
		{Method: "GET", Host: "git.example.com:8443", Path: "/gitea/api/v1/repos/o/r/issues"},
		{Method: "GET", Host: "fixed.example.com", Path: "/x"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("prefix: got %v, want %v", got, want)
	}
	if routes[0].Host != "{base}" {
		t.Error("ResolveRoutes mutated its input")
	}
	for _, base := range []string{"", "http://git.example.com", "https://u@git.example.com", "https://git.example.com?x=1", "https://git.example.com/a/../b", "https://git.example.com:"} {
		if _, err := ResolveRoutes(routes, base, "o/r"); err == nil {
			t.Errorf("base %q: err = nil, want a refusal", base)
		}
	}
	if _, err := ResolveRoutes(routes[2:], "", ""); err != nil {
		t.Errorf("no {base} route needs no base, got %v", err)
	}
}

func TestAdmitRequestHeaders(t *testing.T) {
	h := NewFakeHost([]Route{
		{Method: "GET", Host: "h.example.com", Path: "/a", RequestHeaders: []string{"x-trace-id"}},
		{Method: "GET", Host: "h.example.com", Path: "/b"},
		{Method: "GET", Host: "h.example.com", Path: "/c", RequestHeaders: []string{"Cookie"}},
	})
	tests := []struct {
		name, path string
		headers    map[string]string
		want       string // empty: admitted; else the exact reason
	}{
		{"no headers", "/a", nil, ""},
		{"fixed", "/b", map[string]string{"Accept": "application/json", "content-type": "x"}, ""},
		{"declared, other case", "/a", map[string]string{"X-Trace-Id": "1"}, ""},
		{"declared elsewhere", "/b", map[string]string{"X-Trace-Id": "1"}, `GET h.example.com/b: request header "X-Trace-Id" is not declared by the route`},
		{"undeclared names the first sorted", "/a", map[string]string{"Zeta": "1", "Alpha": "1", "Accept": "x"}, `GET h.example.com/a: request header "Alpha" is not declared by the route`},
		{"never declarable", "/b", map[string]string{"Authorization": "x"}, `GET h.example.com/b: request header "Authorization" can never be declared`},
		{"never declarable even if declared", "/c", map[string]string{"Cookie": "x"}, `GET h.example.com/c: request header "Cookie" can never be declared`},
		{"overlong name is bounded", "/b", map[string]string{strings.Repeat("a", 300): "x"}, `GET h.example.com/b: request header "` + strings.Repeat("a", 256) + `..." is not declared by the route`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := h.Admit(Request{Method: "GET", URL: "https://h.example.com" + tc.path, Headers: tc.headers})
			if tc.want == "" {
				if err != nil {
					t.Fatalf("Admit = %v, want admitted", err)
				}
				return
			}
			if !errors.Is(err, ErrRouteRefused) {
				t.Fatalf("err = %v, want ErrRouteRefused", err)
			}
			if got := strings.TrimPrefix(err.Error(), ErrRouteRefused.Error()+": "); got != tc.want {
				t.Errorf("reason = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLoadRoutesDecodesHeaderDeclarations(t *testing.T) {
	t.Setenv(RoutesEnv, `[{"method":"GET","host":"h","path":"/p","requestHeaders":["X-A"],"responseHeaders":["X-B"]}]`)
	got, err := LoadRoutes()
	if err != nil {
		t.Fatal(err)
	}
	want := []Route{{Method: "GET", Host: "h", Path: "/p", RequestHeaders: []string{"X-A"}, ResponseHeaders: []string{"X-B"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("routes = %v, want %v", got, want)
	}
	resolved, err := ResolveRoutes(got, "", "")
	if err != nil || !reflect.DeepEqual(resolved, want) {
		t.Errorf("ResolveRoutes = %v, %v; want the declarations kept", resolved, err)
	}
}
