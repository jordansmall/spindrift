package bindregistry

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"spindrift.dev/launcher/internal/registrymanifest"
)

// newTestForwarder stands a live Forwarder in front of upstream.
func newTestForwarder(t *testing.T, upstream *httptest.Server, secret string) *httptest.Server {
	t.Helper()

	u, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", upstream.URL, err)
	}
	host, portStr, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatalf("split upstream host/port %q: %v", u.Host, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse upstream port %q: %v", portStr, err)
	}

	handler, err := NewTCPForwarder(host, port, secret)
	if err != nil {
		t.Fatalf("NewTCPForwarder: %v", err)
	}

	return httptest.NewServer(handler)
}

// TestNewTCPForwarder_RelaysAndAttachesSecret verifies the box-local
// HTTP-aware forwarder: the secret header rides the outbound leg, the
// inbound method/path/query reach the fake upstream unchanged, and the fake
// upstream's response (status + body) comes back through the forwarder
// unchanged.
func TestNewTCPForwarder_RelaysAndAttachesSecret(t *testing.T) {
	const wantSecret = "s3cr3t-value"

	var (
		gotMethod string
		gotPath   string
		gotQuery  string
		gotSecret string
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotSecret = r.Header.Get(registrymanifest.TCPSecretHeader)
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("upstream body"))
	}))
	defer upstream.Close()

	forwarder := newTestForwarder(t, upstream, wantSecret)
	defer forwarder.Close()

	resp, err := http.Get(forwarder.URL + "/crates/foo?bar=baz")
	if err != nil {
		t.Fatalf("GET through forwarder: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("upstream saw method %q, want GET", gotMethod)
	}
	if gotPath != "/crates/foo" {
		t.Errorf("upstream saw path %q, want /crates/foo", gotPath)
	}
	if gotQuery != "bar=baz" {
		t.Errorf("upstream saw query %q, want bar=baz", gotQuery)
	}
	if gotSecret != wantSecret {
		t.Errorf("upstream saw secret %q, want %q", gotSecret, wantSecret)
	}
	if resp.StatusCode != http.StatusTeapot {
		t.Errorf("response status = %d, want %d", resp.StatusCode, http.StatusTeapot)
	}
	if string(body) != "upstream body" {
		t.Errorf("response body = %q, want %q", string(body), "upstream body")
	}
}

// TestNewTCPForwarder_InvalidUpstreamHost verifies a construction-time error
// rather than a handler that panics or silently misbehaves at request time.
func TestNewTCPForwarder_InvalidUpstreamHost(t *testing.T) {
	if _, err := NewTCPForwarder("", 0, "secret"); err == nil {
		t.Fatalf("NewTCPForwarder with empty host: err = nil, want non-nil")
	}
}

// TestNewTCPForwarder_InvalidUpstreamPort verifies a zero port is rejected at
// construction time rather than building a handler that fails only when a
// request actually arrives.
func TestNewTCPForwarder_InvalidUpstreamPort(t *testing.T) {
	if _, err := NewTCPForwarder("localhost", 0, "secret"); err == nil {
		t.Fatalf("NewTCPForwarder with zero port: err = nil, want non-nil")
	}
}

// TestNewTCPForwarder_InvalidSecret verifies an empty secret is rejected at
// construction time rather than building a handler that silently forwards
// requests with no auth header attached.
func TestNewTCPForwarder_InvalidSecret(t *testing.T) {
	if _, err := NewTCPForwarder("localhost", 8080, ""); err == nil {
		t.Fatalf("NewTCPForwarder with empty secret: err = nil, want non-nil")
	}
}

// TestNewTCPForwarder_PreservesInboundHost pins the inbound Host surviving the
// hop, which the launcher proxy needs to derive the Forwarder's own address.
func TestNewTCPForwarder_PreservesInboundHost(t *testing.T) {
	const wantSecret = "s3cr3t-value"
	const wantHost = "client.local:9999"

	var (
		gotHost   string
		gotSecret string
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		gotSecret = r.Header.Get(registrymanifest.TCPSecretHeader)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	forwarder := newTestForwarder(t, upstream, wantSecret)
	defer forwarder.Close()

	req, err := http.NewRequest(http.MethodGet, forwarder.URL+"/crates/foo", nil)
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}
	req.Host = wantHost

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET through forwarder: %v", err)
	}
	defer resp.Body.Close()

	if gotHost != wantHost {
		t.Errorf("upstream saw Host %q, want %q", gotHost, wantHost)
	}
	if gotSecret != wantSecret {
		t.Errorf("upstream saw secret %q, want %q", gotSecret, wantSecret)
	}
}
