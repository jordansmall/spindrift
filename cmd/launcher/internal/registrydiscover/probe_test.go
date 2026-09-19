package registrydiscover

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHTTPProbe_BearerHeaderAnswersBearer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="registry"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	got := HTTPProbe(&http.Client{Timeout: time.Second}, srv.URL)
	if got != "bearer" {
		t.Errorf("got %q, want %q", got, "bearer")
	}
}

func TestHTTPProbe_BasicHeaderAnswersBasic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="x"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	got := HTTPProbe(&http.Client{Timeout: time.Second}, srv.URL)
	if got != "basic" {
		t.Errorf("got %q, want %q", got, "basic")
	}
}

func TestHTTPProbe_NoHeaderAnswersBearer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	got := HTTPProbe(&http.Client{Timeout: time.Second}, srv.URL)
	if got != "bearer" {
		t.Errorf("got %q, want %q", got, "bearer")
	}
}

// HTTPProbe has no error return by design (see Probe's doc), so a transport
// error, here a server closed before the probe runs, falls back to "bearer".
func TestHTTPProbe_UnreachableAnswersBearer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()

	got := HTTPProbe(&http.Client{Timeout: time.Second}, url)
	if got != "bearer" {
		t.Errorf("got %q, want %q", got, "bearer")
	}
}

// HTTPProbe only ever proposes "bearer" or "basic". A "header:<Name>" scheme is
// operator-authored in the routes file, never probed.
func TestHTTPProbe_UnmodeledSchemeAnswersBearer(t *testing.T) {
	cases := []string{`Digest realm="x"`, "garbage-scheme"}
	for _, header := range cases {
		t.Run(header, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("WWW-Authenticate", header)
				w.WriteHeader(http.StatusUnauthorized)
			}))
			defer srv.Close()

			got := HTTPProbe(&http.Client{Timeout: time.Second}, srv.URL)
			if got != "bearer" {
				t.Errorf("got %q, want %q", got, "bearer")
			}
		})
	}
}

// WWW-Authenticate scheme tokens are case-insensitive per RFC 7235.
func TestHTTPProbe_SchemeMatchIsCaseInsensitive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `bEaReR realm="registry"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	got := HTTPProbe(&http.Client{Timeout: time.Second}, srv.URL)
	if got != "bearer" {
		t.Errorf("got %q, want %q", got, "bearer")
	}
}
