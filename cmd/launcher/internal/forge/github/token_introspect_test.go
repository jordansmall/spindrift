package github

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Pins issue #1950's read-only token gate. A scripted fake `gh` stands in for
// a live GitHub call so the test still runs the real gh-shelling code path.
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

// A token with no classic scopes at all must yield no scopes, not a slice
// holding one empty string.
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

// Pins issue #2864: gh's stderr diagnostic must reach the returned error
// through ghCommandErr.
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

// Pins issue #2864: gh's stderr diagnostic must reach the returned error
// through ghCommandErr.
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

// A missing permissions object is an ambiguous signal, not a "no push access"
// signal. checkReadOnlyTokenGate aborts startup on an introspection error, so
// returning an error here keeps an unreadable signal fail-closed.
func TestTokenRepoPushPermission_MissingPermissionsFieldFailsClosed(t *testing.T) {
	prependFakeGH(t, `printf '{"full_name":"owner/repo"}'`)

	_, err := TokenRepoPushPermission("ghs_test", "owner/repo")
	if err == nil {
		t.Fatal("TokenRepoPushPermission() = nil error, want an error for a missing permissions field")
	}
}
