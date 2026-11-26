// In-package, like fuzz_test.go: it needs the unexported maxHTTPURL and
// httpMethods to state the invariants.

package connectorhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// stubTransport answers every request the Network admits and records it: the
// fuzz target must never dial. The route host redirects, so the one-hop path
// runs; the hop host answers 200.
type stubTransport struct{ sent []*http.Request }

func (s *stubTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	s.sent = append(s.sent, r)
	resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("ok")), Request: r}
	if strings.EqualFold(r.URL.Host, "api.example.com") {
		resp.StatusCode = 302
		resp.Header.Set("Location", "https://blob.example.com/log?sig=secret")
	}
	return resp, nil
}

// fuzzDocument is the one document the fuzz route declares.
const fuzzDocument = `query { viewer { login } }`

// FuzzHTTPRequest feeds arbitrary bytes to http_request as the guest's request
// body: request decoding, the URL cap and parse, the method set and route
// matching, against a Network whose transport is a stub. Whatever the input,
// only an https request to the route host with a method in the fixed set and
// no header outside the fixed set, the route's declared extras and the host's
// credential (a redirect hop carries only a Range) may
// reach the transport, a request to the document-constrained route carries a
// body whose "query" is the declared document, the failure must name a
// sentinel, and a refused
// oversized URL or unknown method must not be echoed into the error or the
// audit entry. The seeds are the f.Add calls below. See FuzzHandshake for the
// -fuzzminimizetime=0 advice on long sessions.
func FuzzHTTPRequest(f *testing.F) {
	for _, s := range []string{
		`{"method":"GET","url":"https://api.example.com/v1/items?q=1"}`,
		`{"method":"POST","url":"https://api.example.com/v1/items","headers":{"X":"y"},"body":"{}"}`,
		`{"method":"GET","url":"https://api.example.com/v1/items","headers":{"accept":"a","X-Declared":"d","x-declared":"e","If-None-Match":"\"t\""}}`,
		`{"method":"POST","url":"https://api.example.com/v1/items","headers":{"X-Declared":"v"},"body":"{}"}`,
		`{"method":"GET","url":"https://api.example.com/v1/items","headers":{"Accept":"a","Cookie":"c"}}`,
		`{"method":"GET","url":"https://api.example.com/v1/items","headers":{"Authorization":"Bearer guest","X-Forwarded-For":"1.2.3.4","Proxy-Authorization":"p","Host":"evil"}}`,
		`{"method":"GET","url":"https://api.example.com/v1/items","headers":{"X Bad":"v","":"v","X-Declared\n":"v"}}`,
		`{"method":"get","url":"https://api.example.com/v1/items"}`,
		`{"method":"TRACE","url":"https://api.example.com/v1/items"}`,
		`{"method":"","url":"https://api.example.com/v1/items"}`,
		`{"method":"` + strings.Repeat("M", 4096) + `","url":"https://api.example.com/v1/items"}`,
		`{"method":"GET","url":"https://api.example.com/` + strings.Repeat("a", maxHTTPURL) + `"}`,
		`{"method":"GET","url":"https://api.example.com/v1/items?` + strings.Repeat("q", maxHTTPURL-len("https://api.example.com/v1/items?")) + `"}`,
		`{"method":"POST","url":"https://api.example.com/v1/graphql","body":"{\"query\":\"query { viewer { login } }\",\"variables\":{\"a\":[1]},\"operationName\":\"V\"}"}`,
		`{"method":"POST","url":"https://api.example.com/v1/graphql","body":"{\"query\":\"query  { viewer { login } }\"}"}`,
		`{"method":"POST","url":"https://api.example.com/v1/graphql","body":"{\"query\":\"query { viewer { login } }\",\"query\":\"mutation { x }\"}"}`,
		`{"method":"POST","url":"https://api.example.com/v1/graphql","body":"{\"mutation\":1,\"Query\":\"query { viewer { login } }\"}"}`,
		`{"method":"POST","url":"https://api.example.com/v1/graphql","body":"{\"query\":\"query { viewer { login } }\"} x"}`,
		`{"method":"POST","url":"https://api.example.com/v1/graphql"}`,
		`{"method":"GET","url":`,
		`{"method":"GET","url":"https://api.example.com/v1/items"} {}`,
		`{"method":"GET","url":"https://api.example.com/v1/items","extra":1}`,
		`{"method":"GET","url":"http://api.example.com/v1/items"}`,
		`{"method":"GET","url":"https://user:pw@api.example.com/v1/items"}`,
		`{"method":"GET","url":"https://api.example.com/v1/items#frag"}`,
		`{"method":"GET","url":"https://api.example.com/v1/../v1/items"}`,
		`{"method":"GET","url":"https://api.example.com/v1/%2e%2e/items"}`,
		`{"method":"GET","url":"https://api.example.com/v1//items"}`,
		`{"method":"GET","url":"https://api.example.com:8443/v1/items"}`,
		`{"method":"GET","url":"%zz"}`,
		`{"method":"GET","url":"\u0000"}`,
		`null`,
		``,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		var audit bytes.Buffer
		stub := &stubTransport{}
		n := &Network{
			Name: "fuzz",
			Routes: []Route{
				{Method: "GET", Host: "api.example.com", Path: "/v1/items", FollowRedirect: true, RequestHeaders: []string{"X-Declared"}},
				{Method: "POST", Host: "api.example.com", Path: "/v1/items"},
				{Method: "POST", Host: "api.example.com", Path: "/v1/graphql", Documents: map[string]string{"Viewer": fuzzDocument}},
			},
			Authorize: func(r *http.Request) error {
				r.Header.Set("Authorization", "Bearer host")
				return nil
			},
			Client: &http.Client{Transport: stub},
			Audit:  NewAuditLog(&audit),
		}
		_, _, err := n.Do(context.Background(), raw, 1<<10)
		if err != nil && !errors.Is(err, ErrRouteRefused) && !errors.Is(err, ErrHTTPSend) {
			t.Fatalf("unnamed failure for %q: %v", raw, err)
		}
		for _, r := range stub.sent {
			// The route host gets the fixed set, the matched route's extras and the
			// host's credential; the redirect hop gets a Range and nothing the
			// guest set.
			// Matching by method and path prefix is sound only while the fuzz routes
			// share a host and differ by method or path; keep that or change this lookup.
			var extras []string
			for _, route := range n.Routes {
				if route.Method == r.Method && strings.HasPrefix(r.URL.Path, route.Path) {
					extras = route.RequestHeaders
				}
			}
			if r.Method == "POST" && r.URL.Path == "/v1/graphql" {
				data, _ := io.ReadAll(r.Body)
				if query, ok := sentFuzzQuery(data); !ok || query != fuzzDocument {
					t.Fatalf("constrained route sent body %q for %q", data, raw)
				}
			}
			for k := range r.Header {
				switch {
				case strings.EqualFold(r.URL.Host, "api.example.com"):
					if !slices.Contains(fixedRequestHeaders, k) && !slices.Contains(extras, k) && k != "Authorization" {
						t.Fatalf("sent header %q for %q", k, raw)
					}
				case k != "Range":
					t.Fatalf("redirect hop carried header %q for %q", k, raw)
				}
			}
			if r.URL.Scheme != "https" || r.URL.User != nil || r.URL.Fragment != "" || len(r.URL.String()) > maxHTTPURL {
				t.Fatalf("sent %s for %q", r.URL.Redacted(), raw)
			}
			if strings.EqualFold(r.URL.Host, "api.example.com") && !slices.Contains(httpMethods, r.Method) {
				t.Fatalf("sent method %q for %q", r.Method, raw)
			}
			if h := strings.ToLower(r.URL.Host); h != "api.example.com" && h != "blob.example.com" {
				t.Fatalf("sent to host %q for %q", r.URL.Host, raw)
			}
		}

		lines := bytes.Split(bytes.TrimSuffix(audit.Bytes(), []byte("\n")), []byte("\n"))
		if len(lines) != 1 {
			t.Fatalf("%d audit lines for %q", len(lines), raw)
		}
		var e AuditEntry
		if jerr := json.Unmarshal(lines[0], &e); jerr != nil {
			t.Fatalf("audit line %q: %v", lines[0], jerr)
		}

		// Echoing is judged only on a request that decodes as the host decodes
		// it: the JSON decoder quotes a malformed one back, bounded by the
		// request size rather than the URL cap.
		var in httpRequest
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		decoded := dec.Decode(&in) == nil && !dec.More()
		if decoded && e.BodySHA256 == "" {
			t.Fatalf("decoded request without a body hash: %q", lines[0])
		}
		if !decoded || err == nil {
			return
		}
		if (len(in.URL) > maxHTTPURL || !slices.Contains(httpMethods, in.Method)) && errors.Is(err, ErrRouteRefused) {
			if len(err.Error()) > maxEchoLen || len(e.Reason) > maxEchoLen || len(e.Method) > len("DELETE") || e.Host != "" {
				t.Fatalf("refusal of url=%d bytes method=%d bytes echoed it: err=%d bytes, audit=%q", len(in.URL), len(in.Method), len(err.Error()), lines[0])
			}
		}
	})
}

// sentFuzzQuery walks the sent body with its own token loop, not graphQLQuery,
// so it fails on a body that lenient decoding would read as the declared
// document: duplicate or case-variant keys, other keys, trailing data.
func sentFuzzQuery(data []byte) (string, bool) {
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return "", false
	}
	var query string
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		key, isStr := tok.(string)
		if err != nil || !isStr || seen[key] {
			return "", false
		}
		seen[key] = true
		var val json.RawMessage
		switch key {
		case "query":
			err = dec.Decode(&query)
		case "variables", "operationName":
			err = dec.Decode(&val)
		default:
			return "", false
		}
		if err != nil {
			return "", false
		}
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return "", false
	}
	if _, err := dec.Token(); err != io.EOF {
		return "", false
	}
	return query, seen["query"]
}
