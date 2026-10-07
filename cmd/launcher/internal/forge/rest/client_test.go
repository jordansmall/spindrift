package rest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	if statusErr.Message != "" {
		t.Fatalf("StatusError.Message = %q, want empty for a bodyless response", statusErr.Message)
	}
	if want := fmt.Sprintf("status %d", http.StatusConflict); statusErr.Error() != want {
		t.Fatalf("StatusError.Error() = %q, want %q", statusErr.Error(), want)
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
	if statusErr.Message != "" {
		t.Fatalf("StatusError.Message = %q, want empty for a bodyless response", statusErr.Message)
	}
	// The "unexpected" branch must not repeat the status code now that
	// StatusError.Error() also renders it.
	if got := err.Error(); strings.Count(got, strconv.Itoa(http.StatusTeapot)) != 1 {
		t.Fatalf("Do error = %q, want the status code %d to appear exactly once", got, http.StatusTeapot)
	}
}

// TestDoStatusErrorMessage covers how readErrorMessage derives
// StatusError.Message from a non-2xx response body, across the JSON,
// raw-text, and edge-case shapes a forge can send.
func TestDoStatusErrorMessage(t *testing.T) {
	jiraBody := `{"errorMessages":["Issue does not exist"],"errors":{}}`
	oversizedJSON := `{"message":"` + strings.Repeat("x", maxErrorBodyRead) + `"}`

	cases := []struct {
		name     string
		status   int
		statuses StatusMap
		body     string
		want     string
	}{
		{
			// A mapped status whose body is Forgejo/Gitea-shaped JSON
			// ({"message": ...}) must surface that message, not just the
			// raw status.
			name:     "mapped status surfaces JSON message",
			status:   http.StatusUnprocessableEntity,
			statuses: StatusMap{http.StatusUnprocessableEntity: errNotFoundStub},
			body:     `{"message":"head branch does not exist"}`,
			want:     "head branch does not exist",
		},
		{
			// An unmapped status with a non-JSON, multi-line body falls
			// back to the raw text, flattened to a single line so it
			// doesn't break the %w error chain.
			name:   "unmapped status falls back to raw text body",
			status: http.StatusTeapot,
			body:   "line one\nline two\n",
			want:   "line one line two",
		},
		{
			// A body that parses as JSON but carries no non-empty
			// "message" (Jira's shape) must still surface its detail, so
			// the raw envelope is used instead of an empty string.
			name:   "JSON body with no message field falls back to raw text",
			status: http.StatusBadRequest,
			body:   jiraBody,
			want:   jiraBody,
		},
		{
			// A JSON body larger than maxErrorBodyRead won't fully
			// decode, so it falls back to the (truncated) raw prefix.
			name:   "oversized JSON body falls back to truncated raw prefix",
			status: http.StatusBadRequest,
			body:   oversizedJSON,
			want:   oversizedJSON[:maxErrorMessageLen] + "...",
		},
		{
			// Control bytes (a terminal escape, a NUL) are blanked, not
			// dropped, so they can't reach Message or fuse the words
			// around them.
			name:   "control bytes are blanked",
			status: http.StatusTeapot,
			body:   "a\x1b[31mb\x00c",
			want:   "a [31mb c",
		},
		{
			// An oversized body must be truncated, since StatusError.Message
			// can end up quoted in an issue comment.
			name:   "oversized body truncates",
			status: http.StatusTeapot,
			body:   strings.Repeat("a", 500),
			want:   strings.Repeat("a", maxErrorMessageLen) + "...",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer srv.Close()

			c := New(srv.URL, nil, "testbackend", tc.statuses, nil)

			err := c.Do(http.MethodGet, "/widgets/1", nil, nil)
			if err == nil {
				t.Fatal("Do returned nil error, want a non-nil error for a non-2xx status")
			}
			if sentinel, ok := tc.statuses[tc.status]; ok && !errors.Is(err, sentinel) {
				t.Fatalf("Do error = %v, want errors.Is match against the mapped sentinel", err)
			}
			var statusErr StatusError
			if !errors.As(err, &statusErr) {
				t.Fatalf("Do error = %v, want errors.As match against StatusError", err)
			}
			if statusErr.Status != tc.status {
				t.Fatalf("StatusError.Status = %d, want %d", statusErr.Status, tc.status)
			}
			if statusErr.Message != tc.want {
				t.Fatalf("StatusError.Message = %q, want %q", statusErr.Message, tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Do error = %q, want it to mention the message %q", err.Error(), tc.want)
			}
		})
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

// recordSleeps swaps c's backoff clock for one that records each sleep instead
// of taking it.
func recordSleeps(c *Client) *[]time.Duration {
	var recorded []time.Duration
	c.backoff.Clock = retry.Clock{
		Now: time.Now,
		Sleep: func(d time.Duration) {
			recorded = append(recorded, d)
		},
	}
	return &recorded
}

// A 429 is retried for every method, POST included.
func TestDoRetriesTransientThenSucceeds(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
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

			recorded := recordSleeps(c)

			if err := c.Do(method, "/widgets/1", nil, nil); err != nil {
				t.Fatalf("Do returned unexpected error: %v", err)
			}
			if requests != 2 {
				t.Fatalf("server saw %d requests, want 2", requests)
			}
			if len(*recorded) != 1 {
				t.Fatalf("recorded sleeps = %v, want exactly one sleep", *recorded)
			}
		})
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

	recorded := recordSleeps(c)

	if err := c.Do(http.MethodGet, "/widgets/1", nil, nil); err != nil {
		t.Fatalf("Do returned unexpected error: %v", err)
	}
	if requests != 2 {
		t.Fatalf("server saw %d requests, want 2", requests)
	}
	if len(*recorded) != 1 {
		t.Fatalf("recorded sleeps = %v, want exactly one sleep", *recorded)
	}
}

func TestDoDoesNotRetry5xxForNonIdempotentMethod(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			var requests int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.WriteHeader(http.StatusBadGateway)
			}))
			defer srv.Close()

			c := New(srv.URL, nil, "testbackend", nil, nil)

			recorded := recordSleeps(c)

			err := c.Do(method, "/widgets", map[string]string{"k": "v"}, nil)
			var statusErr StatusError
			if !errors.As(err, &statusErr) || statusErr.Status != http.StatusBadGateway {
				t.Fatalf("Do error = %v, want StatusError{Status: %d}", err, http.StatusBadGateway)
			}
			if requests != 1 {
				t.Fatalf("server saw %d requests, want 1", requests)
			}
			if len(*recorded) != 0 {
				t.Fatalf("recorded sleeps = %v, want none", *recorded)
			}
		})
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

	recorded := recordSleeps(c)

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
	if len(*recorded) != 0 {
		t.Fatalf("recorded sleeps = %v, want no sleeps for non-transient status", *recorded)
	}
}

// The StatusMap here maps 404, not the 429 the server returns, so the
// exhausted retry falls through to the generic unmapped-status path.
func TestDoExhaustsRetriesOnPersistentTransient(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"message":"rate limit exceeded"}`)
	}))
	defer srv.Close()

	c := New(srv.URL, nil, "testbackend", StatusMap{http.StatusNotFound: errNotFoundStub}, nil)

	recorded := recordSleeps(c)

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
	var statusErr StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("Do error = %v, want errors.As match against StatusError", err)
	}
	if want := "rate limit exceeded"; statusErr.Message != want {
		t.Fatalf("StatusError.Message = %q, want %q", statusErr.Message, want)
	}
	if requests != defaultMaxAttempts {
		t.Fatalf("server saw %d requests, want %d (defaultMaxAttempts)", requests, defaultMaxAttempts)
	}
	if len(*recorded) != defaultMaxAttempts-1 {
		t.Fatalf("recorded sleeps = %v, want exactly %d sleeps", *recorded, defaultMaxAttempts-1)
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

func TestDoWithHeaderReturnsResponseHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Total-Count", "7")
		w.Write([]byte(`{"name":"widget"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, nil, "testbackend", nil, nil)

	var out struct {
		Name string `json:"name"`
	}
	h, err := c.DoWithHeader(http.MethodGet, "/widgets", nil, &out)
	if err != nil {
		t.Fatalf("DoWithHeader returned unexpected error: %v", err)
	}
	if got := h.Get("x-total-count"); got != "7" {
		t.Fatalf("x-total-count = %q, want 7", got)
	}
	if out.Name != "widget" {
		t.Fatalf("out.Name = %q, want widget", out.Name)
	}
}

func TestDoWithHeaderErrorReturnsNoHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Total-Count", "7")
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := New(srv.URL, nil, "testbackend", nil, nil)

	h, err := c.DoWithHeader(http.MethodGet, "/widgets", nil, nil)
	if err == nil {
		t.Fatal("DoWithHeader on a 404 returned nil error")
	}
	if h != nil {
		t.Fatalf("headers = %v, want nil on error", h)
	}
}
