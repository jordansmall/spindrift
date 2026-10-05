// Package rest is the shared JSON-over-HTTP client for forge adapters that
// speak plain REST. It turns a non-2xx status into a sentinel error an adapter
// configures once via StatusMap, so no call site branches on raw status codes.
package rest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"

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
// Message is the forge's own explanation from the response body, if any.
type StatusError struct {
	Status  int
	Message string
}

// Error renders the status code, e.g. "status 409", with the forge's message
// appended when present, e.g. "status 422: head branch does not exist".
func (e StatusError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("status %d", e.Status)
	}
	return fmt.Sprintf("status %d: %s", e.Status, e.Message)
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

// maxErrorMessageLen bounds StatusError.Message to this many runes (plus a
// trailing "..." ellipsis when truncated): these strings can end up quoted
// verbatim in an issue comment, so an oversized or binary body must not flow
// through unbounded.
const maxErrorMessageLen = 200

// maxErrorBodyRead caps how much of a non-2xx body readErrorMessage reads. A
// JSON body past this limit won't decode, so it falls back to the raw prefix
// like any non-JSON body.
const maxErrorBodyRead = 4 * 1024

// readErrorMessage extracts a short, single-line explanation from a non-2xx
// response body: the "message" field for a Forgejo/Gitea-shaped JSON error,
// or the raw body text otherwise — including JSON with no "message", such as
// Jira's {"errorMessages":[...]}, whose detail lives only in the envelope.
func readErrorMessage(r io.Reader) string {
	raw, err := io.ReadAll(io.LimitReader(r, maxErrorBodyRead))
	if err != nil || len(raw) == 0 {
		return ""
	}

	text := string(raw)
	var decoded struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &decoded) == nil && decoded.Message != "" {
		text = decoded.Message
	}

	// Blank every non-printable rune (control bytes, format and bidi runes,
	// non-ASCII spaces) so a forge body can't smuggle terminal escapes into
	// logs.
	printable := strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return ' '
	}, text)

	flat := strings.Join(strings.Fields(printable), " ")
	return truncateRunes(flat, maxErrorMessageLen)
}

// truncateRunes returns s unchanged if it has at most n runes, otherwise its
// first n runes plus a trailing "...", without splitting a multi-byte rune.
func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "..."
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

		statusErr := StatusError{Status: resp.StatusCode, Message: readErrorMessage(resp.Body)}
		resp.Body.Close()
		if sentinel, ok := c.statuses[resp.StatusCode]; ok {
			return fmt.Errorf("%s: %s %s: %w: %w", c.backend, method, path, sentinel, statusErr)
		}
		return fmt.Errorf("%s: %s %s: unexpected %w", c.backend, method, path, statusErr)
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
