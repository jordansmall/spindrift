package connectorhost_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
)

func TestPublicAddress(t *testing.T) {
	for addr, want := range map[string]bool{
		"8.8.8.8":                true,
		"1.1.1.1":                true,
		"140.82.112.3":           true,
		"2606:4700:4700::1111":   true,
		"::ffff:8.8.8.8":         true,
		"64:ff9b::808:808":       true,
		"127.0.0.1":              false,
		"::1":                    false,
		"::ffff:127.0.0.1":       false,
		"10.0.0.1":               false,
		"172.16.0.1":             false,
		"172.31.255.255":         false,
		"192.168.1.1":            false,
		"fc00::1":                false,
		"fd00:ec2::254":          false,
		"169.254.169.254":        false,
		"169.254.0.1":            false,
		"fe80::1":                false,
		"fe80::1%eth0":           false,
		"100.64.0.1":             false,
		"100.100.100.200":        false,
		"0.0.0.0":                false,
		"0.1.2.3":                false,
		"::":                     false,
		"224.0.0.1":              false,
		"ff02::1":                false,
		"255.255.255.255":        false,
		"64:ff9b::7f00:1":        false,
		"64:ff9b::a00:1":         false,
		"64:ff9b::a9fe:a9fe":     false,
		"::ffff:169.254.169.254": false,
		"::ffff:100.100.100.200": false,
	} {
		if got := connectorhost.PublicAddress(netip.MustParseAddr(addr)); got != want {
			t.Errorf("publicAddress(%s) = %v, want %v", addr, got, want)
		}
	}
}

func TestCheckSeamsRefusesATestSeamOutsideATestBinary(t *testing.T) {
	seams := map[string]connectorhost.Network{
		"PublicForTest": {PublicForTest: func(netip.Addr) bool { return true }},
		"LookupForTest": {LookupForTest: func(context.Context, string) ([]netip.Addr, error) { return nil, nil }},
	}
	for name, n := range seams {
		if err := connectorhost.CheckSeams(n, false); err == nil {
			t.Errorf("%s accepted outside a test binary", name)
		}
		if err := connectorhost.CheckSeams(n, true); err != nil {
			t.Errorf("%s refused in a test binary: %v", name, err)
		}
	}
	if err := connectorhost.CheckSeams(connectorhost.Network{}, false); err != nil {
		t.Errorf("a Network with no seam refused: %v", err)
	}
}

func TestRouteJSONCannotSetNamedByKnob(t *testing.T) {
	var r connectorhost.Route
	if err := json.Unmarshal([]byte(`{"method":"GET","host":"h.io","path":"/a","NamedByKnob":true,"namedByKnob":true}`), &r); err != nil {
		t.Fatal(err)
	}
	if r.NamedByKnob {
		t.Fatal("a route table set NamedByKnob")
	}
}

// resolvingTo is a LookupForTest answering every name with addrs.
func resolvingTo(addrs ...string) func(context.Context, string) ([]netip.Addr, error) {
	var out []netip.Addr
	for _, a := range addrs {
		out = append(out, netip.MustParseAddr(a))
	}
	return func(context.Context, string) ([]netip.Addr, error) { return out, nil }
}

// named is a Route and request URL for example.com, a name the httptest
// certificate covers, on b's port.
func named(b *backend, knob bool) (connectorhost.Route, []byte) {
	host := fmt.Sprintf("example.com:%d", b.Listener.Addr().(*net.TCPAddr).Port)
	r := connectorhost.Route{Method: "GET", Host: host, Path: "/repos", NamedByKnob: knob}
	return r, fmt.Appendf(nil, `{"method":"GET","url":"https://%s/repos"}`, host)
}

func okBackend(t *testing.T) *backend {
	return newBackend(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") })
}

func TestLiteralRouteRefusesANonPublicAddress(t *testing.T) {
	for _, addr := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "fe80::1", "100.100.100.200"} {
		t.Run(addr, func(t *testing.T) {
			b := okBackend(t)
			var audit bytes.Buffer
			route, raw := named(b, false)
			n := netFor(b, &audit, route)
			n.PublicForTest = nil
			n.LookupForTest = resolvingTo(addr)
			_, _, err := n.Do(context.Background(), raw, 1<<10)
			if !errors.Is(err, connectorhost.ErrRouteRefused) {
				t.Fatalf("err = %v, want ErrRouteRefused", err)
			}
			if b.hits.Load() != 0 {
				t.Fatal("the request reached the server")
			}
			e := auditLines(t, &audit)
			if len(e) != 1 || e[0].Decision != connectorhost.DecisionRefused || !strings.Contains(e[0].Reason, addr) ||
				e[0].AddressCheck != connectorhost.AddressCheckRefused {
				t.Fatalf("audit = %+v, want a refusal naming %s", e, addr)
			}
		})
	}
}

func TestLiteralRouteWithNoClientIsChecked(t *testing.T) {
	var audit bytes.Buffer
	n := &connectorhost.Network{
		Name:          "demo",
		Routes:        []connectorhost.Route{{Method: "GET", Host: "example.com", Path: "/repos"}},
		LookupForTest: resolvingTo("10.0.0.1"),
		Audit:         connectorhost.NewAuditLog(&audit),
	}
	_, _, err := n.Do(context.Background(), []byte(`{"method":"GET","url":"https://example.com/repos"}`), 1<<10)
	if !errors.Is(err, connectorhost.ErrRouteRefused) {
		t.Fatalf("err = %v, want ErrRouteRefused", err)
	}
}

func TestKnobNamedRouteMayResolveAnywhere(t *testing.T) {
	b := okBackend(t)
	var audit bytes.Buffer
	// A loopback host, which the production classifier refuses for a literal.
	n := netFor(b, &audit, connectorhost.Route{Method: "GET", Host: b.host(), Path: "/repos", NamedByKnob: true})
	n.PublicForTest = nil
	raw := []byte(fmt.Sprintf(`{"method":"GET","url":"%s/repos"}`, b.URL))
	if _, body, err := n.Do(context.Background(), raw, 1<<10); err != nil || string(body) != "ok" {
		t.Fatalf("body = %q, err = %v, want the knob-named host admitted", body, err)
	}
	if e := auditLines(t, &audit); len(e) != 1 || e[0].AddressCheck != "" {
		t.Fatalf("audit = %+v, want no address check", e)
	}
}

func TestLiteralRouteDialsAPublicAddress(t *testing.T) {
	b := okBackend(t)
	var audit bytes.Buffer
	route, raw := named(b, false)
	n := netFor(b, &audit, route)
	n.LookupForTest = resolvingTo("127.0.0.1")
	n.PublicForTest = func(a netip.Addr) bool { return a == netip.MustParseAddr("127.0.0.1") }
	if _, body, err := n.Do(context.Background(), raw, 1<<10); err != nil || string(body) != "ok" {
		t.Fatalf("body = %q, err = %v, want the request sent", body, err)
	}
	if e := auditLines(t, &audit); len(e) != 1 || e[0].AddressCheck != connectorhost.AddressCheckPublic {
		t.Fatalf("audit = %+v, want address_check %q", e, connectorhost.AddressCheckPublic)
	}
}

func TestLiteralRouteDialsOnlyThePublicAddressOfAMixedAnswer(t *testing.T) {
	b := okBackend(t)
	route, raw := named(b, false)
	n := netFor(b, &bytes.Buffer{}, route)
	// 10.0.0.1 first: dialling it would stall to the dial timeout.
	n.LookupForTest = resolvingTo("10.0.0.1", "127.0.0.1")
	n.PublicForTest = func(a netip.Addr) bool { return a != netip.MustParseAddr("10.0.0.1") }
	if _, _, err := n.Do(context.Background(), raw, 1<<10); err != nil {
		t.Fatal(err)
	}
	if b.hits.Load() != 1 {
		t.Fatalf("server hits = %d, want 1", b.hits.Load())
	}
}

func TestLiteralRouteBehindAProxyIsAuditedUnchecked(t *testing.T) {
	b := okBackend(t)
	var tunnels atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "connect only", http.StatusMethodNotAllowed)
			return
		}
		tunnels.Add(1)
		up, err := net.Dial("tcp", b.host())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		down, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			up.Close()
			return
		}
		io.WriteString(down, "HTTP/1.1 200 Connection established\r\n\r\n")
		go func() { io.Copy(up, down); up.Close() }()
		go func() { io.Copy(down, up); down.Close() }()
	}))
	t.Cleanup(proxy.Close)
	proxyURL, _ := url.Parse(proxy.URL)

	var audit bytes.Buffer
	route, raw := named(b, false)
	n := netFor(b, &audit, route)
	n.PublicForTest = nil
	tr := b.Client().Transport.(*http.Transport).Clone()
	tr.Proxy = http.ProxyURL(proxyURL)
	n.Client = &http.Client{Transport: tr}
	if _, body, err := n.Do(context.Background(), raw, 1<<10); err != nil || string(body) != "ok" {
		t.Fatalf("body = %q, err = %v, want the request proxied", body, err)
	}
	if tunnels.Load() != 1 {
		t.Fatalf("proxy tunnels = %d, want 1", tunnels.Load())
	}
	if e := auditLines(t, &audit); len(e) != 1 || e[0].AddressCheck != connectorhost.AddressCheckProxy {
		t.Fatalf("audit = %+v, want address_check %q", e, connectorhost.AddressCheckProxy)
	}
}

func TestLiteralRouteOverACustomTransportIsAuditedUnchecked(t *testing.T) {
	var audit bytes.Buffer
	stub := &admitCaseTransport{}
	n := &connectorhost.Network{
		Name:   "demo",
		Routes: []connectorhost.Route{{Method: "GET", Host: "example.com", Path: "/repos"}},
		Client: &http.Client{Transport: stub},
		Audit:  connectorhost.NewAuditLog(&audit),
	}
	if _, _, err := n.Do(context.Background(), []byte(`{"method":"GET","url":"https://example.com/repos"}`), 1<<10); err != nil {
		t.Fatal(err)
	}
	if e := auditLines(t, &audit); stub.sent != 1 || len(e) != 1 || e[0].AddressCheck != connectorhost.AddressCheckCustomTransport {
		t.Fatalf("sent = %d, audit = %+v, want address_check %q", stub.sent, e, connectorhost.AddressCheckCustomTransport)
	}
}
