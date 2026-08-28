package github

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTokenOAuthScopes_ParsesXOAuthScopesHeader verifies TokenOAuthScopes
// extracts the comma-separated scope list from `gh api -i`'s X-OAuth-Scopes
// response header (issue #1950's read-only token gate), exercising the real
// gh-shelling code path against a scripted fake `gh` rather than a live
// GitHub call.
func TestTokenOAuthScopes_ParsesXOAuthScopesHeader(t *testing.T) {
	dir := prependFakeGH(t, `printf 'HTTP/2.0 200 OK\nX-OAuth-Scopes: repo, read:org\n\n{}'`)

	scopes, err := TokenOAuthScopes("ghp_test")
	if err != nil {
		t.Fatalf("TokenOAuthScopes: %v", err)
	}
	want := []string{"repo", "read:org"}
	if len(scopes) != len(want) || scopes[0] != want[0] || scopes[1] != want[1] {
		t.Errorf("TokenOAuthScopes() = %v, want %v", scopes, want)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "call-00.txt"))
	if err != nil {
		t.Fatalf("call-00.txt not written: %v", err)
	}
	if !strings.Contains(string(raw), "api\n-i\nuser") {
		t.Errorf("call = %q, want it to invoke `gh api -i user`", raw)
	}
}

// TestTokenOAuthScopes_EmptyHeaderReturnsNil verifies an empty
// X-OAuth-Scopes header (a token with no classic scopes at all) yields a nil
// slice rather than a slice containing one empty string.
func TestTokenOAuthScopes_EmptyHeaderReturnsNil(t *testing.T) {
	prependFakeGH(t, `printf 'HTTP/2.0 200 OK\nX-OAuth-Scopes: \n\n{}'`)

	scopes, err := TokenOAuthScopes("ghp_test")
	if err != nil {
		t.Fatalf("TokenOAuthScopes: %v", err)
	}
	if len(scopes) != 0 {
		t.Errorf("TokenOAuthScopes() = %v, want empty", scopes)
	}
}

// TestTokenOAuthScopes_ErrorSurfacesStderr verifies that when `gh api -i
// user` exits non-zero with a diagnostic on stderr, TokenOAuthScopes's
// returned error includes that stderr text, routed through ghCommandErr
// (issue #2864).
func TestTokenOAuthScopes_ErrorSurfacesStderr(t *testing.T) {
	prependFakeGH(t, `printf 'HTTP 401: Bad credentials\n' >&2
exit 1
`)

	_, err := TokenOAuthScopes("ghp_test")
	if err == nil {
		t.Fatal("TokenOAuthScopes: want error, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP 401: Bad credentials") {
		t.Fatalf("TokenOAuthScopes error must surface gh's stderr; got: %v", err)
	}
}

// TestTokenRepoPushPermission_ParsesPushTrue verifies TokenRepoPushPermission
// reads `permissions.push` out of the repo endpoint's JSON body.
func TestTokenRepoPushPermission_ParsesPushTrue(t *testing.T) {
	prependFakeGH(t, `printf '{"permissions":{"admin":false,"push":true,"pull":true}}'`)

	push, err := TokenRepoPushPermission("ghs_test", "owner/repo")
	if err != nil {
		t.Fatalf("TokenRepoPushPermission: %v", err)
	}
	if !push {
		t.Error("TokenRepoPushPermission() = false, want true")
	}
}

// TestTokenRepoPushPermission_ParsesPushFalse verifies a token with no push
// access is reported as such.
func TestTokenRepoPushPermission_ParsesPushFalse(t *testing.T) {
	prependFakeGH(t, `printf '{"permissions":{"admin":false,"push":false,"pull":true}}'`)

	push, err := TokenRepoPushPermission("ghs_test", "owner/repo")
	if err != nil {
		t.Fatalf("TokenRepoPushPermission: %v", err)
	}
	if push {
		t.Error("TokenRepoPushPermission() = true, want false")
	}
}

// TestTokenRepoPushPermission_ErrorSurfacesStderr verifies that when `gh api
// repos/<slug>` exits non-zero with a diagnostic on stderr,
// TokenRepoPushPermission's returned error includes that stderr text, routed
// through ghCommandErr (issue #2864).
func TestTokenRepoPushPermission_ErrorSurfacesStderr(t *testing.T) {
	prependFakeGH(t, `printf 'HTTP 404: Not Found\n' >&2
exit 1
`)

	_, err := TokenRepoPushPermission("ghs_test", "owner/repo")
	if err == nil {
		t.Fatal("TokenRepoPushPermission: want error, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP 404: Not Found") {
		t.Fatalf("TokenRepoPushPermission error must surface gh's stderr; got: %v", err)
	}
}

// TestTokenRepoPushPermission_MissingPermissionsFieldFailsClosed verifies
// that a response with no `permissions` object at all (an ambiguous signal,
// not a "no push access" signal) returns an error rather than silently
// reporting push=false -- checkReadOnlyTokenGate treats an introspection
// error as a startup abort, so this keeps an unreadable signal fail-closed
// instead of fail-open.
func TestTokenRepoPushPermission_MissingPermissionsFieldFailsClosed(t *testing.T) {
	prependFakeGH(t, `printf '{"full_name":"owner/repo"}'`)

	_, err := TokenRepoPushPermission("ghs_test", "owner/repo")
	if err == nil {
		t.Fatal("TokenRepoPushPermission() = nil error, want an error for a missing permissions field")
	}
}
