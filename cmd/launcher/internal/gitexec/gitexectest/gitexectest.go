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
	"testing"
)

// AuthRemote starts a smart-HTTP server for a fresh bare repository seeded
// with one commit on main and returns the credential-free clone URL and the
// bare repository's directory. Every request whose basic-auth user and
// password do not match gets 401. The server stops with the test.
func AuthRemote(t testing.TB, user, pass string) (cleanURL, bareDir string) {
	t.Helper()
	root := t.TempDir()
	bareDir = filepath.Join(root, "repo.git")
	git(t, root, "init", "--bare", "-b", "main", bareDir)
	git(t, bareDir, "config", "http.receivepack", "true")

	seed := filepath.Join(root, "seed")
	git(t, root, "init", "-b", "main", seed)
	if err := os.WriteFile(filepath.Join(seed, "README"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, seed, "add", "README")
	git(t, seed, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-m", "seed")
	git(t, seed, "push", bareDir, "main")

	execPath := git(t, root, "--exec-path")
	backend := &cgi.Handler{
		Path:       filepath.Join(strings.TrimSpace(execPath), "git-http-backend"),
		Env:        []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"},
		InheritEnv: []string{"PATH"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	return srv.URL + "/repo.git", bareDir
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
