package rest

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// TestDoDecodesJSONResponse covers the success path where the server returns
// a 2xx with a JSON body that Do decodes into the caller's out.
func TestDoDecodesJSONResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"name":"widget"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, nil, "testbackend", nil, nil)

	var out struct {
		Name string `json:"name"`
	}
	if err := c.Do(http.MethodGet, "/widgets/1", nil, &out); err != nil {
		t.Fatalf("Do returned unexpected error: %v", err)
	}
	if out.Name != "widget" {
		t.Fatalf("out.Name = %q, want %q", out.Name, "widget")
	}
}

// TestDoNoBody covers the case where neither the request nor the response
// carries a body (out == nil, body == nil).
func TestDoNoBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > 0 {
			t.Errorf("server received unexpected request body, ContentLength=%d", r.ContentLength)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := New(srv.URL, nil, "testbackend", nil, nil)

	if err := c.Do(http.MethodDelete, "/widgets/1", nil, nil); err != nil {
		t.Fatalf("Do returned unexpected error: %v", err)
	}
}

// errNotFoundStub is the test's stand-in sentinel, distinct from any forge
// package sentinel so the test doesn't couple rest to forge.
var errNotFoundStub = errors.New("stub: not found")

// TestDoMappedStatusReturnsSentinel covers a status code present in the
// Client's StatusMap: Do must return an error satisfying errors.Is against
// the mapped sentinel.
func TestDoMappedStatusReturnsSentinel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := New(srv.URL, nil, "testbackend", StatusMap{http.StatusNotFound: errNotFoundStub}, nil)

	err := c.Do(http.MethodGet, "/widgets/1", nil, nil)
	if err == nil {
		t.Fatal("Do returned nil error, want a mapped sentinel error")
	}
	if !errors.Is(err, errNotFoundStub) {
		t.Fatalf("Do error = %v, want errors.Is match against errNotFoundStub", err)
	}
}

// TestDoUnmappedStatusReturnsPlainError covers a status code absent from the
// Client's StatusMap: Do must still return a non-nil error, but it must not
// match the sentinel used for a different (mapped) status, and its message
// should surface the raw status code.
func TestDoUnmappedStatusReturnsPlainError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	defer srv.Close()

	c := New(srv.URL, nil, "testbackend", StatusMap{http.StatusNotFound: errNotFoundStub}, nil)

	err := c.Do(http.MethodGet, "/widgets/1", nil, nil)
	if err == nil {
		t.Fatal("Do returned nil error, want a plain error for an unmapped status")
	}
	if errors.Is(err, errNotFoundStub) {
		t.Fatalf("Do error = %v, unexpectedly matched errNotFoundStub", err)
	}
	if !strings.Contains(err.Error(), strconv.Itoa(http.StatusTeapot)) {
		t.Fatalf("Do error = %v, want it to mention status code %d", err, http.StatusTeapot)
	}
}

// TestDoMappedStatusMessageIncludesRawStatusCode covers that a mapped-status
// error's message still surfaces the raw status code, not just the sentinel
// text, so callers asserting on the numeric status (e.g. "403") in the error
// message keep working even though the status maps to a sentinel.
func TestDoMappedStatusMessageIncludesRawStatusCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := New(srv.URL, nil, "testbackend", StatusMap{http.StatusNotFound: errNotFoundStub}, nil)

	err := c.Do(http.MethodGet, "/widgets/1", nil, nil)
	if err == nil {
		t.Fatal("Do returned nil error, want a mapped sentinel error")
	}
	if !errors.Is(err, errNotFoundStub) {
		t.Fatalf("Do error = %v, want errors.Is match against errNotFoundStub", err)
	}
	if !strings.Contains(err.Error(), strconv.Itoa(http.StatusNotFound)) {
		t.Fatalf("Do error = %v, want it to mention raw status code %d", err, http.StatusNotFound)
	}
}

// TestDoAppliesAuthStrategy covers that Do invokes the configured
// AuthStrategy on the outgoing request — asserted here via TokenAuth setting
// the Authorization header the server observes.
func TestDoAppliesAuthStrategy(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	auth := TokenAuth{Scheme: "token", Token: "s3cr3t"}
	c := New(srv.URL, auth, "testbackend", nil, nil)

	if err := c.Do(http.MethodGet, "/widgets/1", nil, nil); err != nil {
		t.Fatalf("Do returned unexpected error: %v", err)
	}
	if want := "token s3cr3t"; gotAuth != want {
		t.Fatalf("server observed Authorization header %q, want %q", gotAuth, want)
	}
}

// TestDoTransportFailure covers a request that never reaches a server (a
// closed listener): Do must return a non-nil error.
func TestDoTransportFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	closedURL := srv.URL
	srv.Close() // close immediately so the listener refuses connections

	c := New(closedURL, nil, "testbackend", nil, nil)

	err := c.Do(http.MethodGet, "/widgets/1", nil, nil)
	if err == nil {
		t.Fatal("Do returned nil error, want a transport error against a closed listener")
	}
}
