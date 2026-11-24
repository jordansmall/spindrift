// Package gitexectest serves a bare git repository over authenticated smart
// HTTP, so tests can run real git against a forge-shaped remote that rejects
// any request without the right basic-auth credentials.
package gitexectest

import (
	"crypto/subtle"
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
	// URL's path.
	URL, OtherURL, MovedURL string
	BareDir                 string

	mu   sync.Mutex
	reqs []Request
}

// Request is one request the server received.
type Request struct {
	Path    string
	HasAuth bool // an Authorization header was present, right or wrong
}

// Requests returns every request received so far, in arrival order.
func (r *Remote) Requests() []Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Request(nil), r.reqs...)
}

// NewAuthRemote is AuthRemote with a second repository (other.git), a
// redirecting path (moved.git -> repo.git), and a record of which requests
// carried an Authorization header. The redirect answers before the auth gate,
// as a forge's rename redirect does.
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

	execPath := git(t, root, "--exec-path")
	backend := &cgi.Handler{
		Path:       filepath.Join(strings.TrimSpace(execPath), "git-http-backend"),
		Env:        []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"},
		InheritEnv: []string{"PATH"},
	}
	rem := &Remote{BareDir: bareDir}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rem.mu.Lock()
		rem.reqs = append(rem.reqs, Request{Path: r.URL.Path, HasAuth: r.Header.Get("Authorization") != ""})
		rem.mu.Unlock()
		if rest, ok := strings.CutPrefix(r.URL.Path, "/moved.git"); ok {
			target := "/repo.git" + rest
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, target, http.StatusMovedPermanently)
			return
		}
		u, p, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(u), []byte(user)) != 1 ||
			subtle.ConstantTimeCompare([]byte(p), []byte(pass)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="gitexectest"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		backend.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	rem.URL = srv.URL + "/repo.git"
	rem.OtherURL = srv.URL + "/other.git"
	rem.MovedURL = srv.URL + "/moved.git"
	return rem
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
