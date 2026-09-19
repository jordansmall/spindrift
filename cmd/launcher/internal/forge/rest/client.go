// Package rest is the shared JSON-over-HTTP client for forge adapters that
// speak plain REST (forgejo today). It turns a non-2xx status into a sentinel
// error an adapter configures once via StatusMap, so no call site branches on
// raw status codes.
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

// Retry knobs New applies to transient (429/5xx) responses.
const (
	defaultBackoffUnit = 200 * time.Millisecond
	defaultBackoffCap  = 2 * time.Second
	defaultMaxAttempts = 3
)

// AuthStrategy adds a backend's own authentication scheme to an outgoing
// request, so Client never knows the details.
type AuthStrategy interface {
	Apply(req *http.Request)
}

// TokenAuth is an AuthStrategy that sets "Authorization: <Scheme> <Token>",
// e.g. "Authorization: token abc" for Forgejo.
type TokenAuth struct {
	Scheme string
	Token  string
}

// Apply sets the Authorization header per TokenAuth's Scheme and Token.
func (a TokenAuth) Apply(req *http.Request) {
	req.Header.Set("Authorization", a.Scheme+" "+a.Token)
}

// StatusMap maps one backend's HTTP status codes to sentinel errors, so a
// caller checks a failure with errors.Is rather than a raw status code.
type StatusMap map[int]error

// StatusError carries the raw HTTP status of a non-2xx response, chained by
// Do into every failed-request error via %w. One status can mean different
// things on different endpoints of the same backend (Forgejo's 409 is "not
// mergeable" on merge but "already exists" on pulls-create) while StatusMap's
// sentinel is shared Client-wide, so a caller disambiguates with errors.As.
type StatusError struct {
	Status int
}

// Error renders the status code, e.g. "status 409".
func (e StatusError) Error() string {
	return fmt.Sprintf("status %d", e.Status)
}

// DecodeError marks a 2xx body that did not decode as the expected JSON,
// distinct from a network failure or a non-2xx status (StatusError). Do
// chains one in via %w so a caller recovers it with errors.As.
type DecodeError struct {
	Err error
}

// Error delegates to the wrapped error, so wrapping changes no message. A
// zero-value DecodeError, such as an errors.As target before As fills it, has
// a nil Err and reports a placeholder rather than dereferencing it.
func (e DecodeError) Error() string {
	if e.Err == nil {
		return "decode error"
	}
	return e.Err.Error()
}

// Unwrap exposes the wrapped error, so errors.Is and errors.As see through
// DecodeError to the original (e.g. a *json.SyntaxError).
func (e DecodeError) Unwrap() error {
	return e.Err
}

// Client is a generic REST client for a single forge backend.
type Client struct {
	baseURL     string
	auth        AuthStrategy
	backend     string
	statuses    StatusMap
	hc          *http.Client
	backoff     retry.LinearBackoff
	maxAttempts int
}

// New returns a Client for baseURL, trimming a trailing "/". A nil auth
// applies no authentication and a nil hc uses http.DefaultClient; backend
// prefixes every error message.
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

func isTransientStatus(status int) bool {
	return status == http.StatusTooManyRequests || (status >= 500 && status < 600)
}

// HTTPClientForTest returns the underlying *http.Client, so an adapter's own
// tests can assert construction defaults such as Timeout without driving a
// real request. Test-only.
func (c *Client) HTTPClientForTest() *http.Client {
	return c.hc
}

// Do issues method against path relative to the base URL, marshaling body as
// the JSON request body (nil for none) and decoding the response into out
// (nil discards it). A non-2xx status returns an error wrapping StatusMap's
// sentinel when the map has one, and a generic status error otherwise.
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
					return fmt.Errorf("%s: decode response from %s %s: %w", c.backend, method, path, DecodeError{Err: err})
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
			return fmt.Errorf("%s: %s %s: %w: %w", c.backend, method, path, sentinel, StatusError{Status: resp.StatusCode})
		}
		resp.Body.Close()
		return fmt.Errorf("%s: %s %s: unexpected status %d: %w", c.backend, method, path, resp.StatusCode, StatusError{Status: resp.StatusCode})
	}
	return fmt.Errorf("%s: %s %s: maxAttempts must be >= 1 (got %d)", c.backend, method, path, c.maxAttempts)
}

// Paginate calls fetch for page 1, 2, 3, ... until fetch reports done, so each
// backend keeps its own last-page signal. It returns fetch's first error.
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
