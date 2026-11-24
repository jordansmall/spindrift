// Package gitexectest serves a bare git repository over authenticated smart
// HTTP, so tests can run real git against a forge-shaped remote that rejects
// any request without the right basic-auth credentials.
package gitexectest

import (
	"crypto/subtle"
	"encoding/pem"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// AuthRemote starts a smart-HTTP server for a fresh bare repository seeded
// with one commit on main and returns the credential-free clone URL and the
// bare repository's directory. Every request whose basic-auth user and
// password do not match gets 401. The server stops with the test.
func AuthRemote(t testing.TB, user, pass string) (cleanURL, bareDir string) {
	t.Helper()
	r := NewAuthRemote(t, user, pass)
	return r.URL, r.BareDir
}

// Remote is one authenticated smart-HTTP server; see NewAuthRemote.
type Remote struct {
	// URL and OtherURL are credential-free clone URLs of two repositories on
	// the same host. MovedURL redirects every request to the same suffix under
	// URL's path before the auth gate; MovedAfterAuthURL does so only for a
	// request that authenticated.
	URL, OtherURL, MovedURL, MovedAfterAuthURL string
	BareDir                                    string

	handler *AuthHandler
}

// Request is one request the server received.
type Request struct {
	Path    string
	HasAuth bool // an Authorization header was present, right or wrong
}

// Requests returns every request received so far, in arrival order.
func (r *Remote) Requests() []Request { return r.handler.Requests() }

// NewAuthRemote is AuthRemote with a second repository (other.git), a
// redirecting path (moved.git -> repo.git), a second one that redirects only
// after authenticating (renamed.git -> repo.git), and a record of which
// requests carried an Authorization header. The first redirect answers before
// the auth gate, as a forge's rename redirect may.
func NewAuthRemote(t testing.TB, user, pass string) *Remote {
	t.Helper()
	root := t.TempDir()
	bareDir := filepath.Join(root, "repo.git")
	git(t, root, "init", "--bare", "-b", "main", bareDir)
	git(t, bareDir, "config", "http.receivepack", "true")
	otherDir := filepath.Join(root, "other.git")
	git(t, root, "init", "--bare", "-b", "main", otherDir)

	seed := filepath.Join(root, "seed")
	git(t, root, "init", "-b", "main", seed)
	if err := os.WriteFile(filepath.Join(seed, "README"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, seed, "add", "README")
	git(t, seed, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-m", "seed")
	git(t, seed, "push", bareDir, "main")
	git(t, seed, "push", otherDir, "main")

	h := NewAuthHandler(t, root, user, pass)
	h.Redirects = map[string]string{"/moved.git": "/repo.git"}
	h.AuthedRedirects = map[string]string{"/renamed.git": "/repo.git"}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &Remote{
		URL: srv.URL + "/repo.git", OtherURL: srv.URL + "/other.git", MovedURL: srv.URL + "/moved.git",
		MovedAfterAuthURL: srv.URL + "/renamed.git",
		BareDir:           bareDir, handler: h,
	}
}

// AuthHandler is the auth-gated smart-HTTP handler NewAuthRemote serves, for a
// test that mounts git under a server of its own. It serves the bare
// repositories under a root directory at their root-relative paths.
type AuthHandler struct {
	// Redirects maps a path prefix to the prefix it moved to. A matching
	// request is answered 301 before the auth gate, as a forge's rename
	// redirect does.
	Redirects map[string]string
	// AuthedRedirects is Redirects for a forge that authenticates the old path
	// first: a request without valid credentials gets 401, one with them gets
	// the 301.
	AuthedRedirects map[string]string

	user, pass string
	backend    *cgi.Handler

	mu   sync.Mutex
	reqs []Request
}

// NewAuthHandler returns a handler serving the bare repositories under root.
// A request whose basic-auth user and password do not match gets 401.
func NewAuthHandler(t testing.TB, root, user, pass string) *AuthHandler {
	t.Helper()
	execPath := git(t, root, "--exec-path")
	return &AuthHandler{user: user, pass: pass, backend: &cgi.Handler{
		Path:       filepath.Join(strings.TrimSpace(execPath), "git-http-backend"),
		Env:        []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"},
		InheritEnv: []string{"PATH"},
	}}
}

// ServeTLS mounts the bare repository bare at path (say "o/r.git") on srv's
// TLS server, which must route that path to the returned handler, and makes
// git trust srv's certificate for the rest of the test. Pushes are allowed.
func ServeTLS(t testing.TB, srv *httptest.Server, path, bare, user, pass string) *AuthHandler {
	t.Helper()
	git(t, bare, "config", "http.receivepack", "true")
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, filepath.Dir(path)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(bare, filepath.Join(root, path)); err != nil {
		t.Fatal(err)
	}
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_SSL_CAINFO", ca)
	return NewAuthHandler(t, root, user, pass)
}

// Requests returns every request received so far, in arrival order.
func (h *AuthHandler) Requests() []Request {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Request(nil), h.reqs...)
}

func (h *AuthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	h.reqs = append(h.reqs, Request{Path: r.URL.Path, HasAuth: r.Header.Get("Authorization") != ""})
	h.mu.Unlock()
	if redirect(w, r, h.Redirects) {
		return
	}
	u, p, ok := r.BasicAuth()
	if !ok || subtle.ConstantTimeCompare([]byte(u), []byte(h.user)) != 1 ||
		subtle.ConstantTimeCompare([]byte(p), []byte(h.pass)) != 1 {
		w.Header().Set("WWW-Authenticate", `Basic realm="gitexectest"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if redirect(w, r, h.AuthedRedirects) {
		return
	}
	h.backend.ServeHTTP(w, r)
}

// redirect answers 301 and reports true when r's path starts with a key of
// moves.
func redirect(w http.ResponseWriter, r *http.Request, moves map[string]string) bool {
	for from, to := range moves {
		if rest, ok := strings.CutPrefix(r.URL.Path, from); ok {
			target := to + rest
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, target, http.StatusMovedPermanently)
			return true
		}
	}
	return false
}

func git(t testing.TB, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// RequireAuthOnlyUnder fails unless some request authenticated and every one that did has a path under pathPrefix.
func RequireAuthOnlyUnder(t testing.TB, reqs []Request, pathPrefix string) {
	t.Helper()
	authed := 0
	for _, r := range reqs {
		if !r.HasAuth {
			continue
		}
		authed++
		if !strings.HasPrefix(r.Path, pathPrefix) {
			t.Errorf("credential sent to %s, want only paths under %s", r.Path, pathPrefix)
		}
	}
	if authed == 0 {
		t.Error("no authenticated git request reached the backend")
	}
}
