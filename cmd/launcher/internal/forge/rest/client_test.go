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

// errors.As(err, &DecodeError{}) zero-initializes the struct before As
// populates it, so Error must not dereference a nil Err during that window.
func TestDecodeErrorZeroValueDoesNotPanic(t *testing.T) {
	var de DecodeError
	if got := de.Error(); got == "" {
		t.Fatalf("DecodeError{}.Error() = %q, want a non-empty message", got)
	}
}

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

func TestDoChainsDecodeErrorOnMalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("not json"))
	}))
	defer srv.Close()

	c := New(srv.URL, nil, "testbackend", nil, nil)

	var out struct {
		Name string `json:"name"`
	}
	err := c.Do(http.MethodGet, "/widgets/1", nil, &out)
	if err == nil {
		t.Fatal("Do returned nil error, want a decode error for a malformed JSON body")
	}
	var decodeErr DecodeError
	if !errors.As(err, &decodeErr) {
		t.Fatalf("Do error = %v, want errors.As match against DecodeError", err)
	}
}

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

// These tests use their own sentinel instead of a forge package one, so rest
// stays uncoupled from forge.
var errNotFoundStub = errors.New("stub: not found")

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

// Callers assert on the numeric status in the error message, so a mapped
// status must still print the raw code and not only the sentinel text.
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

// A caller that disambiguates by endpoint rather than by the shared sentinel
// needs the raw code, so a mapped status must still chain a StatusError.
func TestDoChainsStatusErrorMappedStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	defer srv.Close()

	c := New(srv.URL, nil, "testbackend", StatusMap{http.StatusConflict: errNotFoundStub}, nil)

	err := c.Do(http.MethodGet, "/widgets/1", nil, nil)
	if err == nil {
		t.Fatal("Do returned nil error, want a mapped sentinel error")
	}
	var statusErr StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("Do error = %v, want errors.As match against StatusError", err)
	}
	if statusErr.Status != http.StatusConflict {
		t.Fatalf("StatusError.Status = %d, want %d", statusErr.Status, http.StatusConflict)
	}
}

func TestDoChainsStatusErrorUnmappedStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	defer srv.Close()

	c := New(srv.URL, nil, "testbackend", StatusMap{http.StatusNotFound: errNotFoundStub}, nil)

	err := c.Do(http.MethodGet, "/widgets/1", nil, nil)
	if err == nil {
		t.Fatal("Do returned nil error, want a plain error for an unmapped status")
	}
	var statusErr StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("Do error = %v, want errors.As match against StatusError", err)
	}
	if statusErr.Status != http.StatusTeapot {
		t.Fatalf("StatusError.Status = %d, want %d", statusErr.Status, http.StatusTeapot)
	}
}

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

// The StatusMap here maps 404, not the 429 the server returns, so the
// exhausted retry falls through to the generic unmapped-status path.
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

// The last fixture page is deliberately short: that is what tells the fetch
// closure to stop, and the server errors the test if Paginate asks for a page
// past it.
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
