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

// Client is a generic REST client for a single forge backend. Construct one
// with New; issue requests with Do.
type Client struct {
	baseURL  string
	auth     AuthStrategy
	backend  string
	statuses StatusMap
	hc       *http.Client
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
		baseURL:  strings.TrimSuffix(baseURL, "/"),
		auth:     auth,
		backend:  backend,
		statuses: statuses,
		hc:       hc,
	}
}

// Do issues an HTTP request with the given method and path (relative to the
// Client's base URL), marshaling body as the JSON request body (nil for
// none) and decoding a JSON response into out (nil to discard the body). A
// non-2xx response status is translated to an error: a status present in
// the Client's StatusMap wraps the mapped sentinel via %w; a status absent
// from the map returns a generic error naming the backend, method, path,
// and raw status code. Do returns nil on success.
func (c *Client) Do(method, path string, body, out any) error {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("%s: marshal request: %w", c.backend, err)
		}
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
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if sentinel, ok := c.statuses[resp.StatusCode]; ok {
			return fmt.Errorf("%s: %s %s: %w (status %d)", c.backend, method, path, sentinel, resp.StatusCode)
		}
		return fmt.Errorf("%s: %s %s: unexpected status %d", c.backend, method, path, resp.StatusCode)
	}

	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil && err != io.EOF {
			return fmt.Errorf("%s: decode response from %s %s: %w", c.backend, method, path, err)
		}
	}
	return nil
}
