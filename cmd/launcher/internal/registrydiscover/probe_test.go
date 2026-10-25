package registrydiscover

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func probeServer(t *testing.T, h http.HandlerFunc) ProbeResult {
	t.Helper()
	srv := httptest.NewServer(h)
	defer srv.Close()
	return HTTPProbe(&http.Client{Timeout: time.Second}, srv.URL)
}

func TestHTTPProbe_BearerHeaderAnswersBearer(t *testing.T) {
	got := probeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="registry"`)
		w.WriteHeader(http.StatusUnauthorized)
	})
	if got.Scheme != "bearer" {
		t.Errorf("Scheme = %q, want %q", got.Scheme, "bearer")
	}
}

func TestHTTPProbe_BasicHeaderAnswersBasic(t *testing.T) {
	got := probeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="x"`)
		w.WriteHeader(http.StatusUnauthorized)
	})
	if got.Scheme != "basic" {
		t.Errorf("Scheme = %q, want %q", got.Scheme, "basic")
	}
}

// No challenge is distinct from a real bearer one: a later host-profile match
// may fill the scheme only when the probe found nothing usable.
func TestHTTPProbe_NoHeaderAnswersNoChallenge(t *testing.T) {
	got := probeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	if got.Scheme != "" {
		t.Errorf("Scheme = %q, want empty", got.Scheme)
	}
}

// HTTPProbe has no error return by design (see Probe's doc), so a transport
// error, here a server closed before the probe runs, reports no challenge and
// no headers.
func TestHTTPProbe_UnreachableAnswersNoChallenge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()

	got := HTTPProbe(&http.Client{Timeout: time.Second}, url)
	if got.Scheme != "" {
		t.Errorf("Scheme = %q, want empty", got.Scheme)
	}
	if got.Header != nil {
		t.Errorf("Header = %v, want nil", got.Header)
	}
}

// HTTPProbe only ever proposes "bearer" or "basic". A "header:<Name>" scheme is
// operator-authored in the routes file, never probed.
func TestHTTPProbe_UnmodeledSchemeAnswersNoChallenge(t *testing.T) {
	cases := []string{`Digest realm="x"`, `Negotiate`, "garbage-scheme"}
	for _, header := range cases {
		t.Run(header, func(t *testing.T) {
			got := probeServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("WWW-Authenticate", header)
				w.WriteHeader(http.StatusUnauthorized)
			})
			if got.Scheme != "" {
				t.Errorf("Scheme = %q, want empty", got.Scheme)
			}
		})
	}
}

// WWW-Authenticate scheme tokens are case-insensitive per RFC 7235.
func TestHTTPProbe_SchemeMatchIsCaseInsensitive(t *testing.T) {
	got := probeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `bEaReR realm="registry"`)
		w.WriteHeader(http.StatusUnauthorized)
	})
	if got.Scheme != "bearer" {
		t.Errorf("Scheme = %q, want %q", got.Scheme, "bearer")
	}
}

func TestHTTPProbe_CarriesResponseHeaders(t *testing.T) {
	got := probeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Artifactory-Id", "abc")
		w.WriteHeader(http.StatusOK)
	})
	if v := got.Header.Get("X-Artifactory-Id"); v != "abc" {
		t.Errorf("Header X-Artifactory-Id = %q, want %q", v, "abc")
	}
}

// ReadProbeResponse is the parsing a corpus fixture shares with the live probe.
func TestReadProbeResponse(t *testing.T) {
	h := http.Header{}
	h.Set("WWW-Authenticate", `Basic realm="x"`)
	h.Set("X-Jfrog-Version", "7")
	got := ReadProbeResponse(h)
	if got.Scheme != "basic" {
		t.Errorf("Scheme = %q, want %q", got.Scheme, "basic")
	}
	if got.Header.Get("X-Jfrog-Version") != "7" {
		t.Errorf("Header not carried: %v", got.Header)
	}
	if empty := ReadProbeResponse(http.Header{}); empty.Scheme != "" {
		t.Errorf("empty header Scheme = %q, want empty", empty.Scheme)
	}
}
