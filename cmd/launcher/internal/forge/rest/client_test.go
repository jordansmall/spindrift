package rest

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/retry"
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

// TestDoRetriesTransientThenSucceeds covers that Do retries a transient
// (429) response, sleeping a single backoff via the injected Clock, and
// succeeds once the server returns 200 on the second attempt.
func TestDoRetriesTransientThenSucceeds(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(srv.URL, nil, "testbackend", nil, nil)

	var recorded []time.Duration
	c.backoff.Clock = retry.Clock{
		Now: time.Now,
		Sleep: func(d time.Duration) {
			recorded = append(recorded, d)
		},
	}

	if err := c.Do(http.MethodGet, "/widgets/1", nil, nil); err != nil {
		t.Fatalf("Do returned unexpected error: %v", err)
	}
	if requests != 2 {
		t.Fatalf("server saw %d requests, want 2", requests)
	}
	if len(recorded) != 1 {
		t.Fatalf("recorded sleeps = %v, want exactly one sleep", recorded)
	}
}

// TestDoRetries5xxThenSucceeds covers that Do retries a transient 5xx
// (503) response the same way it retries 429, sleeping a single backoff via
// the injected Clock, and succeeds once the server returns 200 on the
// second attempt.
func TestDoRetries5xxThenSucceeds(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(srv.URL, nil, "testbackend", nil, nil)

	var recorded []time.Duration
	c.backoff.Clock = retry.Clock{
		Now: time.Now,
		Sleep: func(d time.Duration) {
			recorded = append(recorded, d)
		},
	}

	if err := c.Do(http.MethodGet, "/widgets/1", nil, nil); err != nil {
		t.Fatalf("Do returned unexpected error: %v", err)
	}
	if requests != 2 {
		t.Fatalf("server saw %d requests, want 2", requests)
	}
	if len(recorded) != 1 {
		t.Fatalf("recorded sleeps = %v, want exactly one sleep", recorded)
	}
}

// TestDoDoesNotRetryNonTransient4xx covers that Do does not retry a
// non-transient status such as 404: the server should see exactly one
// request, no sleep should be recorded, and the existing mapped-status
// behavior (errors.Is against the configured sentinel) still applies.
func TestDoDoesNotRetryNonTransient4xx(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := New(srv.URL, nil, "testbackend", StatusMap{http.StatusNotFound: errNotFoundStub}, nil)

	var recorded []time.Duration
	c.backoff.Clock = retry.Clock{
		Now: time.Now,
		Sleep: func(d time.Duration) {
			recorded = append(recorded, d)
		},
	}

	err := c.Do(http.MethodGet, "/widgets/1", nil, nil)
	if err == nil {
		t.Fatal("Do returned nil error, want a mapped sentinel error")
	}
	if !errors.Is(err, errNotFoundStub) {
		t.Fatalf("Do error = %v, want errors.Is match against errNotFoundStub", err)
	}
	if requests != 1 {
		t.Fatalf("server saw %d requests, want 1 (no retry for non-transient status)", requests)
	}
	if len(recorded) != 0 {
		t.Fatalf("recorded sleeps = %v, want no sleeps for non-transient status", recorded)
	}
}

// TestDoExhaustsRetriesOnPersistentTransient covers that Do gives up after
// maxAttempts when a transient status (429) never clears: the server should
// see exactly defaultMaxAttempts requests, sleep exactly
// defaultMaxAttempts-1 times between them, and return a non-nil error that
// falls through to the generic unmapped-status path (this Client's
// StatusMap doesn't map 429).
func TestDoExhaustsRetriesOnPersistentTransient(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := New(srv.URL, nil, "testbackend", StatusMap{http.StatusNotFound: errNotFoundStub}, nil)

	var recorded []time.Duration
	c.backoff.Clock = retry.Clock{
		Now: time.Now,
		Sleep: func(d time.Duration) {
			recorded = append(recorded, d)
		},
	}

	err := c.Do(http.MethodGet, "/widgets/1", nil, nil)
	if err == nil {
		t.Fatal("Do returned nil error, want a non-nil error after exhausting retries")
	}
	if errors.Is(err, errNotFoundStub) {
		t.Fatalf("Do error = %v, unexpectedly matched errNotFoundStub", err)
	}
	if !strings.Contains(err.Error(), strconv.Itoa(http.StatusTooManyRequests)) {
		t.Fatalf("Do error = %v, want it to mention status code %d", err, http.StatusTooManyRequests)
	}
	if requests != defaultMaxAttempts {
		t.Fatalf("server saw %d requests, want %d (defaultMaxAttempts)", requests, defaultMaxAttempts)
	}
	if len(recorded) != defaultMaxAttempts-1 {
		t.Fatalf("recorded sleeps = %v, want exactly %d sleeps", recorded, defaultMaxAttempts-1)
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

// TestPaginateWalksAllPages covers the substrate-level page-walking
// behavior: a 3-page fixture (two full pages of 3 items, a shorter final
// page of 1 item) must yield every item across all pages, in order, via a
// fetch closure that does its own c.Do call per page and signals "done"
// once it observes a short page -- and the server must never see a request
// for a page beyond that last, short page.
func TestPaginateWalksAllPages(t *testing.T) {
	const pageSize = 3
	fixture := map[int][]string{
		1: {"a", "b", "c"},
		2: {"d", "e", "f"},
		3: {"g"},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, err := strconv.Atoi(r.URL.Query().Get("page"))
		if err != nil {
			t.Errorf("server received request with invalid page query param: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		items, ok := fixture[page]
		if !ok {
			t.Errorf("server received request for page %d, want no request beyond the last (short) page", page)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(items); err != nil {
			t.Fatalf("server failed to encode fixture page %d: %v", page, err)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, nil, "testbackend", nil, nil)

	var got []string
	err := c.Paginate(func(page int) (bool, error) {
		var items []string
		if err := c.Do(http.MethodGet, fmt.Sprintf("/items?page=%d", page), nil, &items); err != nil {
			return false, err
		}
		got = append(got, items...)
		return len(items) < pageSize, nil
	})
	if err != nil {
		t.Fatalf("Paginate returned unexpected error: %v", err)
	}

	want := []string{"a", "b", "c", "d", "e", "f", "g"}
	if len(got) != len(want) {
		t.Fatalf("Paginate accumulated %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Paginate accumulated %v, want %v", got, want)
		}
	}
}
