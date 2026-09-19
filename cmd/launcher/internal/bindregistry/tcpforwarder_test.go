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

// A bad host must fail at construction, not from a handler that panics or
// misbehaves once a request arrives.
func TestNewTCPForwarder_InvalidUpstreamHost(t *testing.T) {
	if _, err := NewTCPForwarder("", 0, "secret"); err == nil {
		t.Fatalf("NewTCPForwarder with empty host: err = nil, want non-nil")
	}
}

// A zero port must fail at construction, not from a handler that fails only
// once a request arrives.
func TestNewTCPForwarder_InvalidUpstreamPort(t *testing.T) {
	if _, err := NewTCPForwarder("localhost", 0, "secret"); err == nil {
		t.Fatalf("NewTCPForwarder with zero port: err = nil, want non-nil")
	}
}

// An empty secret must fail at construction. Otherwise the handler forwards
// every request with no auth header attached.
func TestNewTCPForwarder_InvalidSecret(t *testing.T) {
	if _, err := NewTCPForwarder("localhost", 8080, ""); err == nil {
		t.Fatalf("NewTCPForwarder with empty secret: err = nil, want non-nil")
	}
}

// Go's SetURL rewrites the outbound Host, so the launcher proxy derived the
// wrong Forwarder address for the cargo dl rewrite and every crate download
// got a 401 (#3314). The inbound Host must survive the hop.
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
