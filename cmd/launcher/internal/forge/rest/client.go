// Package rest is the generic, error-only native-HTTP substrate shared by
// forge adapters that speak plain JSON-over-HTTP REST (forgejo today; a
// future jira or other backend tomorrow). It factors out the marshal,
// build-request, auth, execute, status-check, decode sequence every such
// adapter otherwise reimplements, and replaces raw-status branching at the
// call site with sentinel errors an adapter configures once via StatusMap.
package rest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/retry"
)

// Default retry knobs applied by New: a bounded linear backoff (behind the
// injectable Clock seam) and a fixed attempt ceiling for transient (429/5xx)
// responses. defaultMaxAttempts is referenced directly by client_test.go
// instead of a magic number.
const (
	defaultBackoffUnit = 200 * time.Millisecond
	defaultBackoffCap  = 2 * time.Second
	defaultMaxAttempts = 3
)

// AuthStrategy mutates an outgoing request to add whatever authentication
// scheme a backend requires, letting each backend supply its own (forgejo's
// "token <T>", a future jira's "Bearer <T>" or HTTP Basic) without Client
// itself knowing the details.
type AuthStrategy interface {
	Apply(req *http.Request)
}

// TokenAuth is an AuthStrategy that sets the Authorization header to
// "Scheme Token", e.g. TokenAuth{Scheme: "token", Token: "abc"} produces
// "Authorization: token abc" (Forgejo's scheme).
type TokenAuth struct {
	Scheme string
	Token  string
}

// Apply sets the Authorization header per TokenAuth's Scheme and Token.
func (a TokenAuth) Apply(req *http.Request) {
	req.Header.Set("Authorization", a.Scheme+" "+a.Token)
}

// StatusMap is a per-backend HTTP-status-code -> sentinel-error table,
// supplied at Client construction. Do consults it when a request fails with
// a non-2xx status, so callers get a stable sentinel (errors.Is-checkable)
// for statuses the backend has mapped, instead of branching on raw status
// codes at each call site.
type StatusMap map[int]error

// StatusError carries the raw HTTP status code of a non-2xx response. Do
// chains one into every error it returns for a failed request (both a
// status mapped by StatusMap and an unmapped one) via %w, alongside any
// mapped sentinel. A single status code can carry different meanings across
// different endpoints of the same backend (e.g. Forgejo's 409 means "not
// mergeable" on the merge endpoint but "already exists" on the pulls-create
// endpoint) -- StatusMap's sentinel is necessarily shared across every call
// through a Client, so a caller that needs to disambiguate by endpoint
// recovers the exact wire status with errors.As(err, &StatusError{})
// instead of parsing it back out of the error string or overloading the
// shared sentinel.
type StatusError struct {
	Status int
}

// Error renders the status code, e.g. "status 409".
func (e StatusError) Error() string {
	return fmt.Sprintf("status %d", e.Status)
}

// Client is a generic REST client for a single forge backend. Construct one
// with New; issue requests with Do.
type Client struct {
	baseURL     string
	auth        AuthStrategy
	backend     string
	statuses    StatusMap
	hc          *http.Client
	backoff     retry.LinearBackoff
	maxAttempts int
}

// New returns a Client for baseURL (a trailing "/" is trimmed), using auth
// to authenticate each request (nil means no auth is applied), backend as
// the error-message prefix identifying which adapter issued the request,
// statuses as the per-status-code sentinel-error table, and hc as the
// underlying HTTP client (nil uses http.DefaultClient).
func New(baseURL string, auth AuthStrategy, backend string, statuses StatusMap, hc *http.Client) *Client {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Client{
		baseURL:     strings.TrimSuffix(baseURL, "/"),
		auth:        auth,
		backend:     backend,
		statuses:    statuses,
		hc:          hc,
		backoff:     retry.LinearBackoff{Unit: defaultBackoffUnit, Cap: defaultBackoffCap, Clock: retry.RealClock()},
		maxAttempts: defaultMaxAttempts,
	}
}

// isTransientStatus reports whether status is a transient failure worth
// retrying: 429 (Too Many Requests) or any 5xx server error.
func isTransientStatus(status int) bool {
	return status == http.StatusTooManyRequests || (status >= 500 && status < 600)
}

// HTTPClientForTest returns the underlying *http.Client Do issues requests
// through -- test-only, letting callers outside this package (e.g. an
// adapter's own tests) assert construction defaults, such as a bounded
// Timeout, without driving a real slow request through Do.
func (c *Client) HTTPClientForTest() *http.Client {
	return c.hc
}

// Do issues an HTTP request with the given method and path (relative to the
// Client's base URL), marshaling body as the JSON request body (nil for
// none) and decoding a JSON response into out (nil to discard the body). A
// non-2xx response status is translated to an error: a status present in
// the Client's StatusMap wraps the mapped sentinel via %w; a status absent
// from the map returns a generic error naming the backend, method, path,
// and raw status code. Do returns nil on success.
func (c *Client) Do(method, path string, body, out any) error {
	var b []byte
	if body != nil {
		var err error
		b, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("%s: marshal request: %w", c.backend, err)
		}
	}

	for attempt := 1; attempt <= c.maxAttempts; attempt++ {
		var reqBody io.Reader
		if body != nil {
			reqBody = bytes.NewReader(b)
		}

		req, err := http.NewRequest(method, c.baseURL+path, reqBody)
		if err != nil {
			return fmt.Errorf("%s: build request: %w", c.backend, err)
		}
		if c.auth != nil {
			c.auth.Apply(req)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := c.hc.Do(req)
		if err != nil {
			return fmt.Errorf("%s: %s %s: %w", c.backend, method, path, err)
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if out != nil {
				if err := json.NewDecoder(resp.Body).Decode(out); err != nil && err != io.EOF {
					resp.Body.Close()
					return fmt.Errorf("%s: decode response from %s %s: %w", c.backend, method, path, err)
				}
			}
			resp.Body.Close()
			return nil
		}

		if isTransientStatus(resp.StatusCode) && attempt < c.maxAttempts {
			io.Copy(io.Discard, resp.Body) //nolint:errcheck // draining to allow keep-alive reuse; a drain error is not actionable here
			resp.Body.Close()
			c.backoff.Do(attempt)
			continue
		}

		if sentinel, ok := c.statuses[resp.StatusCode]; ok {
			resp.Body.Close()
			return fmt.Errorf("%s: %s %s: %w (status %d): %w", c.backend, method, path, sentinel, resp.StatusCode, StatusError{Status: resp.StatusCode})
		}
		resp.Body.Close()
		return fmt.Errorf("%s: %s %s: unexpected status %d: %w", c.backend, method, path, resp.StatusCode, StatusError{Status: resp.StatusCode})
	}
	return fmt.Errorf("%s: %s %s: maxAttempts must be >= 1 (got %d)", c.backend, method, path, c.maxAttempts)
}

// Paginate repeatedly invokes fetch for page 1, 2, 3, ... until fetch
// reports the walk is done, so a caller performs one Do call per page and
// decides its own last-page signal (array length, startAt/total, a
// next-page header, whatever its backend uses) without writing the loop
// itself. Paginate returns the first error fetch returns, if any.
func (c *Client) Paginate(fetch func(page int) (done bool, err error)) error {
	for page := 1; ; page++ {
		done, err := fetch(page)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
	}
}
