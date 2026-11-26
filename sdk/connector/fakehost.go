package connector

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/textproto"
	"net/url"
	"os"
	"path"
	"slices"
	"strings"
)

// Route mirrors the host's Route JSON shape: a Connector's declared grant to
// send Method requests to Host under the subtree rooted at Path.
//
// RequestHeaders and ResponseHeaders declare the headers beyond the fixed sets
// the Connector may send and read on this Route.
//
// Documents is the host's POST-only GraphQL query allowlist; the fake decodes
// it but does not yet apply it.
type Route struct {
	Method          string            `json:"method"`
	Host            string            `json:"host"`
	Path            string            `json:"path"`
	FollowRedirect  bool              `json:"followRedirect,omitempty"`
	RequestHeaders  []string          `json:"requestHeaders,omitempty"`
	ResponseHeaders []string          `json:"responseHeaders,omitempty"`
	Documents       map[string]string `json:"documents,omitempty"`
}

// RoutesEnv names the environment variable a Connector's checks export its
// declared Route table in, as JSON.
const RoutesEnv = "SPINDRIFT_BACKEND_CONNECTOR_ROUTES"

// ErrRouteRefused is the host's refusal of a request no Route admits.
var ErrRouteRefused = errors.New("connector http request refused")

const (
	routeBaseHost = "{base}"
	routeRepoVar  = "{repo}"
)

// httpMethods, fixedRequestHeaders and neverRequestHeader mirror the host's
// (cmd/launcher/internal/connectorhost); the SDK module cannot import them, so a
// change there lands here too. The response-side allowlist (ResponseHeaders) is
// deliberately not applied: the fake serves no response headers, so a
// conformance run against it does not check response reads.
var httpMethods = []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE"}

var fixedRequestHeaders = []string{
	"Accept", "Accept-Language", "Cache-Control", "Content-Type",
	"If-Match", "If-Modified-Since", "If-None-Match", "If-Unmodified-Since",
}

// neverRequestHeader reports a request header no Route may declare; name is
// canonical. The host rejects such a declaration when it loads the Route table,
// which the fake does not, so the fake refuses the header even if declared.
func neverRequestHeader(name string) bool {
	switch name {
	case "Authorization", "Cookie", "Host", "Forwarded", "Via", "X-Real-Ip":
		return true
	}
	return strings.HasPrefix(name, "X-Forwarded-") || strings.HasPrefix(name, "Proxy-")
}

// LoadRoutes decodes the declared Route table from RoutesEnv.
func LoadRoutes() ([]Route, error) {
	raw := os.Getenv(RoutesEnv)
	if raw == "" {
		return nil, fmt.Errorf("connector: %s is not set", RoutesEnv)
	}
	var routes []Route
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&routes); err != nil {
		return nil, fmt.Errorf("connector: %s is not a Route table: %w", RoutesEnv, err)
	}
	return routes, nil
}

// ResolveRoutes returns routes as the host holds the Connector to them: a Route
// whose Host is "{base}" takes the host of base (a bare https URL) and its path
// prefix, and "{repo}" in a path becomes repo. base is read only when a Route
// uses "{base}".
func ResolveRoutes(routes []Route, base, repo string) ([]Route, error) {
	out := slices.Clone(routes)
	var host, prefix string
	if slices.ContainsFunc(out, func(r Route) bool { return r.Host == routeBaseHost }) {
		u, err := url.Parse(base)
		switch {
		case err != nil:
			return nil, fmt.Errorf("connector: base %q is not a URL", base)
		case u.Scheme != "https" || u.User != nil || u.RawPath != "" || strings.ContainsAny(base, "?#") || !bareHost(u):
			return nil, fmt.Errorf("connector: base %q must be a bare https URL", base)
		}
		host, prefix = u.Host, strings.TrimSuffix(u.Path, "/")
		if prefix != "" && path.Clean(prefix) != prefix {
			return nil, fmt.Errorf("connector: base %q must have a canonical path", base)
		}
	}
	for i := range out {
		out[i].Path = strings.ReplaceAll(out[i].Path, routeRepoVar, repo)
		if out[i].Host != routeBaseHost {
			continue
		}
		out[i].Host = host
		// "/" is the base's own subtree root: prefix+"/" is not canonical.
		if out[i].Path == "/" && prefix != "" {
			out[i].Path = prefix
		} else {
			out[i].Path = prefix + out[i].Path
		}
	}
	return out, nil
}

// TB is the part of *testing.T a FakeHost needs; declared here so the SDK's
// non-test code never imports "testing" into a Connector's wasm.
type TB interface {
	Helper()
	Cleanup(func())
	Fatalf(format string, args ...any)
}

// FakeHost stands in for the host's http_request call under native go test. It
// is a test aid only: the real host is the authority and applies the Route rule
// (plus its address policy) to every request. The fake re-implements the core
// rule and can lag it, so a request it admits is not proof the host will.
//
// Build one per test package so Unused covers every test that ran.
//
// Serial, same-goroutine use only: Serve swaps a package-global and updates
// Unused's state unlocked, so no t.Parallel and no requests from other goroutines.
type FakeHost struct {
	routes []Route
	used   []bool
}

// NewFakeHost returns a FakeHost enforcing routes.
func NewFakeHost(routes []Route) *FakeHost {
	return &FakeHost{routes: slices.Clone(routes), used: make([]bool, len(routes))}
}

// Admit reports whether some Route admits req: nil, or an error wrapping
// ErrRouteRefused ending in the host's reason. The fake reads `connector http
// request refused: <reason>`; the host inserts `connector "<name>": ` before the
// reason, so only the reason suffix matches.
func (h *FakeHost) Admit(req Request) error {
	_, err := h.match(req)
	return err
}

func (h *FakeHost) match(in Request) (int, error) {
	refuse := func(format string, args ...any) (int, error) {
		return -1, fmt.Errorf("%w: %s", ErrRouteRefused, fmt.Sprintf(format, args...))
	}
	if len(in.URL) > MaxHTTPURL {
		return refuse("url is %d bytes, over the %d-byte limit", len(in.URL), MaxHTTPURL)
	}
	if !slices.Contains(httpMethods, in.Method) {
		return refuse("method is not one of %s", strings.Join(httpMethods, ", "))
	}
	u, err := url.Parse(in.URL)
	if err != nil {
		return refuse("%s: unparseable url", in.Method)
	}
	if u.Scheme != "https" || u.User != nil || u.Fragment != "" {
		return refuse("%s %s%s: only https urls without userinfo or fragment are allowed", in.Method, u.Host, u.Path)
	}
	// The host cleans the path before matching, so a non-canonical or escaped
	// form is refused first: the server would resolve it differently.
	if strings.HasPrefix(u.Path, "/") && path.Clean(u.Path) == u.Path && u.RawPath == "" {
		for i, r := range h.routes {
			if r.Method == in.Method && strings.EqualFold(r.Host, u.Host) && admitsPath(r.Path, u.Path) {
				// Sorted, so the refusal names the same header whatever the map order.
				for _, k := range slices.Sorted(maps.Keys(in.Headers)) {
					c := textproto.CanonicalMIMEHeaderKey(k)
					name := k
					if len(name) > MaxHeaderName {
						name = name[:MaxHeaderName] + "..."
					}
					if neverRequestHeader(c) {
						return refuse("%s %s%s: request header %q can never be declared", in.Method, u.Host, u.Path, name)
					}
					declared := slices.Contains(fixedRequestHeaders, c) ||
						slices.ContainsFunc(r.RequestHeaders, func(d string) bool { return textproto.CanonicalMIMEHeaderKey(d) == c })
					if !declared {
						return refuse("%s %s%s: request header %q is not declared by the route", in.Method, u.Host, u.Path, name)
					}
				}
				return i, nil
			}
		}
	}
	return refuse("no route admits %s %s%s", in.Method, u.Host, u.Path)
}

// admitsPath is the host's subtree test for one root. ";" is refused because
// servlet containers strip path parameters before normalizing.
func admitsPath(root, p string) bool {
	if strings.Contains(p, ";") {
		return false
	}
	return root == "/" || p == root || strings.HasPrefix(p, root+"/")
}

// Serve routes HTTPRequest through the fake for the rest of the test, restored
// by t.Cleanup. A request no Route admits fails the test with the refusal, as
// the real host aborts the call; an admitted one is recorded in the returned
// slice and answered by answer, whose error reaches HTTPRequest as a transport
// failure.
func (h *FakeHost) Serve(t TB, answer func(Request) (Response, error)) *[]Request {
	t.Helper()
	sent := &[]Request{}
	saved := hostCall
	t.Cleanup(func() { hostCall = saved })
	hostCall = func(raw, resp []byte) (uint64, error) {
		var req Request
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			t.Fatalf("connector: fake host cannot decode the request: %v", err)
		}
		i, err := h.match(req)
		if err != nil {
			t.Fatalf("%v", err)
		}
		h.used[i] = true
		*sent = append(*sent, req)
		out, err := answer(req)
		if err != nil {
			return uint64(copy(resp, err.Error())), nil
		}
		if len(out.Body) > len(resp) {
			t.Fatalf("connector: fake host answer is %d bytes, over the %d-byte response limit", len(out.Body), len(resp))
		}
		return uint64(out.Status)<<32 | uint64(copy(resp, out.Body)), nil
	}
	return sent
}

// Unused returns the declared Routes, in declaration order, that no admitted
// request has matched.
func (h *FakeHost) Unused() []Route {
	var out []Route
	for i, r := range h.routes {
		if !h.used[i] {
			out = append(out, r)
		}
	}
	return out
}

// bareHost mirrors the host's check: a non-empty host[:port] that survives a
// round trip as an authority, with no empty trailing port.
func bareHost(u *url.URL) bool {
	r, err := url.Parse("https://" + u.Host)
	return err == nil && r.Host == u.Host && u.Hostname() != "" && !strings.HasSuffix(u.Host, ":")
}
