package connectorhost_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
)

// fetched is what the fixture network Connector reports of its http_request.
type fetched struct {
	Status int    `json:"status"`
	Body   string `json:"body"`
	// Header is nil when the header the test asked for was absent.
	Header *string `json:"header"`
}

// loadNetworkFixture loads a fixture network Connector against a scripted
// backend whose only declared route is GET /repos.
func loadNetworkFixture(t *testing.T, wasm string) (*connectorhost.Connector, *backend, *bytes.Buffer, map[string]string) {
	t.Helper()
	return loadNetworkFixtureWith(t, wasm, func(*connectorhost.Network) {})
}

// loadNetworkFixtureWith lets tweak adjust the Network before the module loads.
func loadNetworkFixtureWith(t *testing.T, wasm string, tweak func(*connectorhost.Network)) (c *connectorhost.Connector, b *backend, audit *bytes.Buffer, params map[string]string) {
	t.Helper()
	return loadNetworkFixtureVia(t, connectorhost.LoadNetwork, wasm, tweak)
}

// loadNetworkFixtureVia is loadNetworkFixtureWith over the engine ld loads with.
func loadNetworkFixtureVia(t *testing.T, ld netLoader, wasm string, tweak func(*connectorhost.Network)) (c *connectorhost.Connector, b *backend, audit *bytes.Buffer, params map[string]string) {
	t.Helper()
	b = newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			fmt.Fprint(w, strings.Repeat("x", connectorhost.MaxHTTPResponse+1))
			return
		}
		w.Header().Add("X-Scripted", "a")
		w.Header().Add("X-Scripted", "b")
		w.Header().Set("X-Empty", "")
		w.Header().Set("Set-Cookie", "session=s3cret")
		fmt.Fprint(w, "scripted "+r.URL.Path)
	})
	audit = &bytes.Buffer{}
	n := netFor(b, audit, connectorhost.Route{Method: "GET", Host: b.host(), Path: "/repos"})
	n.Name = "fixture-network"
	tweak(n)
	c = loadNetWith(t, ld, fixture(t, wasm), n)
	return c, b, audit, map[string]string{"url": b.URL + "/repos/x", "method": "GET"}
}

func TestNetworkFixtureReachesTheBackendWithTheHostCredential(t *testing.T) {
	c, b, audit, params := loadNetworkFixture(t, "network.wasm")
	var out fetched
	if err := c.Call(context.Background(), "fetch", params, &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != 200 || out.Body != "scripted /repos/x" {
		t.Errorf("fetched %+v, want the backend's answer", out)
	}
	if got := b.last.Load().Header.Get("Authorization"); b.hits.Load() != 1 || got != "Bearer s3cret" {
		t.Errorf("backend saw %d requests, Authorization %q: want one carrying the host credential", b.hits.Load(), got)
	}
	if strings.Contains(fmt.Sprintf("%+v", out), "s3cret") {
		t.Error("credential reached the Connector")
	}
	lines := auditLines(t, audit)
	if len(lines) != 1 {
		t.Fatalf("audit = %+v, want one entry", lines)
	}
	want := connectorhost.AuditEntry{
		Connector: "fixture-network", Route: "GET " + b.host() + " /repos", Method: "GET",
		Host: b.host(), Path: "/repos/x", Status: 200, Size: int64(len(out.Body)), Decision: connectorhost.DecisionSent,
	}
	if lines[0] != want {
		t.Errorf("audit = %+v, want %+v", lines[0], want)
	}
}

func TestNetworkFixtureRefusesUndeclaredRequestsUnsent(t *testing.T) {
	cases := []struct {
		wasm   string
		method string
		host   func(b *backend) string
		path   string
	}{
		{"network-host.wasm", "GET", func(*backend) string { return "undeclared.invalid" }, "/repos/x"},
		{"network-method.wasm", "DELETE", (*backend).host, "/repos/x"},
		{"network-path.wasm", "GET", (*backend).host, "/admin"},
		{"network-write-path.wasm", "POST", (*backend).host, "/admin"},
	}
	for _, tc := range cases {
		t.Run(tc.wasm, func(t *testing.T) {
			c, b, audit, params := loadNetworkFixture(t, tc.wasm)
			err := c.Call(context.Background(), "fetch", params, new(fetched))
			if !errors.Is(err, connectorhost.ErrCall) || !errors.Is(err, connectorhost.ErrRouteRefused) {
				t.Fatalf("err = %v, want ErrCall and ErrRouteRefused", err)
			}
			for _, want := range []string{"fixture-network", tc.method, tc.host(b), tc.path} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("err = %v, want it to name %q", err, want)
				}
			}
			if b.hits.Load() != 0 {
				t.Errorf("backend saw %d requests, want none", b.hits.Load())
			}
			lines := auditLines(t, audit)
			if len(lines) != 1 || lines[0].Decision != connectorhost.DecisionRefused || lines[0].Method != tc.method || lines[0].Status != 0 || lines[0].Connector != "fixture-network" {
				t.Errorf("audit = %+v, want one refused %s entry", lines, tc.method)
			}
			if err := c.Call(context.Background(), "fetch", params, new(fetched)); !errors.Is(err, connectorhost.ErrUnusable) {
				t.Errorf("second call err = %v, want ErrUnusable", err)
			}
		})
	}
}

// A write whose answer is over the response cap fails closed: the Connector
// gets no body and the request, which did reach the backend, is audited once.
func TestNetworkFixtureOversizedWriteResponseFailsClosed(t *testing.T) {
	c, b, audit, params := loadNetworkFixtureWith(t, "network-write-oversize.wasm", func(n *connectorhost.Network) {
		n.Routes = append(n.Routes, connectorhost.Route{Method: "PUT", Host: n.Routes[0].Host, Path: "/repos"})
	})
	var out fetched
	err := c.Call(context.Background(), "fetch", params, &out)
	if !errors.Is(err, connectorhost.ErrCall) || !errors.Is(err, connectorhost.ErrResponseTooLarge) {
		t.Fatalf("err = %v, want ErrCall and ErrResponseTooLarge", err)
	}
	if out.Body != "" || out.Status != 0 {
		t.Errorf("fetched %+v, want nothing to reach the Connector", out)
	}
	if b.hits.Load() != 1 || b.last.Load().Method != http.MethodPut {
		t.Errorf("backend saw %d requests, want the one PUT", b.hits.Load())
	}
	lines := auditLines(t, audit)
	if len(lines) != 1 || lines[0].Decision != connectorhost.DecisionFailed || lines[0].Method != "PUT" || lines[0].Path != "/repos/x" || lines[0].Connector != "fixture-network" {
		t.Fatalf("audit = %+v, want one failed PUT entry", lines)
	}
	if !strings.Contains(lines[0].Reason, connectorhost.ErrResponseTooLarge.Error()) {
		t.Errorf("audit reason %q does not name %v", lines[0].Reason, connectorhost.ErrResponseTooLarge)
	}
	if err := c.Call(context.Background(), "fetch", params, new(fetched)); !errors.Is(err, connectorhost.ErrUnusable) {
		t.Errorf("second call err = %v, want ErrUnusable", err)
	}
}

// Oversized output is hostile-oversize.wasm's case: through the SDK, encoding
// past the 8 MiB cap outruns the call budget under the interpreter on loaded CI.
func TestNetworkFixtureMalformedOutputFailsClosedAfterItsAuditedRequest(t *testing.T) {
	want := connectorhost.ErrMalformedOutput
	c, b, audit, params := loadNetworkFixture(t, "network-malformed.wasm")
	err := c.Call(context.Background(), "fetch", params, new(fetched))
	if !errors.Is(err, connectorhost.ErrCall) || !errors.Is(err, want) {
		t.Fatalf("err = %v, want ErrCall and %v", err, want)
	}
	lines := auditLines(t, audit)
	if len(lines) != 2 || lines[0].Decision != connectorhost.DecisionSent || b.hits.Load() != 1 {
		t.Fatalf("audit = %+v, hits = %d, want the sent request then a failure", lines, b.hits.Load())
	}
	if got := lines[1]; got.Decision != connectorhost.DecisionFailed || got.Connector != "fixture-network" || !strings.Contains(got.Reason, want.Error()) {
		t.Errorf("second entry = %+v, want a failed entry naming %v", got, want)
	}
	if err := c.Call(context.Background(), "fetch", params, new(fetched)); !errors.Is(err, connectorhost.ErrUnusable) {
		t.Errorf("second call err = %v, want ErrUnusable", err)
	}
}

// The guest sees a fixed reason for a failed send: an Authorize error may carry
// the credential, and the fixture hands what the guest saw back in its error.
func TestNetworkFixtureSendFailureShowsTheGuestOnlyAFixedReason(t *testing.T) {
	c, _, audit, params := loadNetworkFixtureWith(t, "network.wasm", func(n *connectorhost.Network) {
		n.Authorize = func(*http.Request) error { return errors.New("token s3cret-token expired") }
	})
	err := c.Call(context.Background(), "fetch", params, new(fetched))
	if err == nil || !strings.Contains(err.Error(), connectorhost.ErrHTTPSend.Error()) {
		t.Fatalf("err = %v, want the guest's failure to carry the fixed reason", err)
	}
	if strings.Contains(err.Error(), "s3cret-token") {
		t.Errorf("err = %v: the Authorize error reached the guest", err)
	}
	if lines := auditLines(t, audit); len(lines) != 1 || !strings.Contains(lines[0].Reason, "s3cret-token") {
		t.Errorf("audit = %+v, want the detail kept in the audit reason", lines)
	}
}

// The SDK's HTTPRequest must not grow the guest's heap per call: a Connector
// serves far more requests than the host's memory cap has room for response
// buffers.
func TestNetworkFixtureServesManyRequestsUnderTheMemoryCap(t *testing.T) {
	b := newBackend(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "scripted "+r.URL.Path) })
	n := netFor(b, &bytes.Buffer{}, connectorhost.Route{Method: "GET", Host: b.host(), Path: "/repos"})
	c := loadNetWith(t, netLoaders["compiled"], fixture(t, "network.wasm"), n)
	params := map[string]string{"url": b.URL + "/repos/x", "method": "GET"}
	for i := range 400 {
		var out fetched
		if err := c.Call(context.Background(), "fetch", params, &out); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		if out.Body != "scripted /repos/x" {
			t.Fatalf("request %d: body %q", i, out.Body)
		}
	}
}

func TestNetworkFixtureReadsResponseHeadersThroughTheHostCall(t *testing.T) {
	c, _, _, params := loadNetworkFixture(t, "network.wasm")
	for _, tc := range []struct {
		name, header string
		want         *string
	}{
		{"canonicalised and joined", "x-scripted", ptr("a, b")},
		{"present but empty", "X-Empty", ptr("")},
		{"absent", "X-Missing", nil},
		{"set-cookie is withheld", "Set-Cookie", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := map[string]string{"url": params["url"], "method": "GET", "header": tc.header}
			var out fetched
			if err := c.Call(context.Background(), "fetch", p, &out); err != nil {
				t.Fatal(err)
			}
			if (out.Header == nil) != (tc.want == nil) || (tc.want != nil && *out.Header != *tc.want) {
				t.Errorf("header %q = %v, want %v", tc.header, out.Header, tc.want)
			}
		})
	}
}

func ptr(s string) *string { return &s }
