package github

import (
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

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

// fakeGHHTTPSRelay stands in for the gh CLI on a host whose gh config prefers
// SSH. `auth git-credential` is the helper GitRemote wires into git, and reads
// the live GH_TOKEN on every call, as the launcher's token refresh requires.
// `repo clone` fails the way an unreachable personal SSH key does, so any code
// path still cloning through gh fails loudly. Every other subcommand falls
// through to the next gh on PATH.
const fakeGHHTTPSRelay = `#!/bin/sh
case "$1 $2" in
"auth git-credential")
	if [ "$3" = get ]; then
		printf 'username=x-access-token\npassword=%s\n' "$GH_TOKEN"
	fi
	exit 0
	;;
"repo clone")
	printf 'git@github.com: Permission denied (publickey).\nfatal: Could not read from remote repository.\n' >&2
	exit 1
	;;
esac
PATH=${PATH#"$FAKE_GH_DIR:"}
exec gh "$@"
`

type httpsRelayRequest struct {
	path       string
	authorised bool
}

// httpsRelayServer serves a bare repo over TLS through git-http-backend,
// demanding basic auth whose password is the token the test currently expects.
type httpsRelayServer struct {
	mu     sync.Mutex
	token  string
	served []httpsRelayRequest
}

func (s *httpsRelayServer) expectToken(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = token
}

func (s *httpsRelayServer) requests() []httpsRelayRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]httpsRelayRequest(nil), s.served...)
}

func (s *httpsRelayServer) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.served = nil
}

// sawAuthorised reports whether an authorised request hit a path ending in
// suffix (e.g. "/git-receive-pack").
func (s *httpsRelayServer) sawAuthorised(suffix string) bool {
	for _, r := range s.requests() {
		if r.authorised && strings.HasSuffix(r.path, suffix) {
			return true
		}
	}
	return false
}

func (s *httpsRelayServer) authorise(r *http.Request) bool {
	_, pass, ok := r.BasicAuth()
	s.mu.Lock()
	defer s.mu.Unlock()
	good := ok && pass == s.token
	s.served = append(s.served, httpsRelayRequest{path: r.URL.Path, authorised: good})
	return good
}

// serveRelayOverHTTPS makes the launcher's httpsClone (relay and Rebase alike)
// reach repo only through https://$GH_HOST/owner/repo.git authenticated by the
// gh credential helper. It models a host whose gh prefers SSH: an empty HOME,
// no SSH agent, an ssh binary that always fails, and a gh config with
// git_protocol: ssh, so a clone that follows gh's protocol preference (or any
// ambient SSH key)
// fails. The initial GH_TOKEN, and the one the server expects, is token.
func serveRelayOverHTTPS(t *testing.T, repo *forgetest.GitRepoFixture, token string) *httpsRelayServer {
	t.Helper()
	backend := filepath.Join(gitExecPath(t), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Fatalf("git-http-backend not available: %v", err)
	}

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "owner"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(repo.Bare, filepath.Join(root, "owner", "repo.git")); err != nil {
		t.Fatal(err)
	}

	s := &httpsRelayServer{token: token}
	cgiHandler := &cgi.Handler{
		Path: backend,
		Env: []string{
			"GIT_PROJECT_ROOT=" + root,
			"GIT_HTTP_EXPORT_ALL=1",
			"REMOTE_USER=x-access-token",
		},
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authorise(r) {
			w.Header().Set("WWW-Authenticate", `Basic realm="relay"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		cgiHandler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	caFile := filepath.Join(t.TempDir(), "ca.pem")
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(caFile, caPEM, 0o644); err != nil {
		t.Fatal(err)
	}

	ghConfig := t.TempDir()
	if err := os.WriteFile(filepath.Join(ghConfig, "config.yml"), []byte("git_protocol: ssh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ghDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(ghDir, "gh"), []byte(fakeGHHTTPSRelay), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", ghDir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_GH_DIR", ghDir)
	t.Setenv("GH_HOST", strings.TrimPrefix(srv.URL, "https://"))
	t.Setenv("GH_TOKEN", token)
	t.Setenv("GH_CONFIG_DIR", ghConfig)
	t.Setenv("GIT_SSL_CAINFO", caFile)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_SSH_COMMAND", "false")
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	// t.Setenv registers the restore; Unsetenv then truly unsets the variable.
	t.Setenv("SSH_AUTH_SOCK", "")
	os.Unsetenv("SSH_AUTH_SOCK")
	return s
}

func gitExecPath(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Fatalf("git --exec-path: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// A relay against a host whose gh prefers SSH must still clone and push over
// https://<GH_HOST>/<repo>.git, authenticated by the gh credential helper
// (issue #4647: relay bundle failed with `Permission denied (publickey)`).
func TestReadOnlyCodeForge_RelayBundle_AuthenticatesOverHTTPSNotSSH(t *testing.T) {
	repo, srv := newRelayHTTPSHarness(t)
	outbox := t.TempDir()
	branch := "agent/issue-4647"
	wantSHA := forgetest.SeedRelayBundle(t, repo.Bare, "main", outbox, branch)

	cf := NewReadOnlyCodeForge("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	if err := cf.(forge.BundleRelay).RelayBundle(outbox, branch); err != nil {
		t.Fatalf("RelayBundle: %v", err)
	}
	if got := forgetest.RevParse(t, repo.Bare, "refs/heads/"+branch); got != wantSHA {
		t.Errorf("refs/heads/%s = %s, want %s", branch, got, wantSHA)
	}
	for _, suffix := range []string{"/git-upload-pack", "/git-receive-pack"} {
		if !srv.sawAuthorised(suffix) {
			t.Errorf("server saw no authorised %s request, want the clone and push authenticated through the gh credential", suffix)
		}
	}
	for _, r := range srv.requests() {
		if !strings.HasPrefix(r.path, "/owner/repo.git/") {
			t.Errorf("request path %q, want it under /owner/repo.git/", r.path)
		}
	}
}

func TestReadOnlyCodeForge_CommitSubjects_AuthenticatesOverHTTPSNotSSH(t *testing.T) {
	repo, srv := newRelayHTTPSHarness(t)
	outbox := t.TempDir()
	branch := "agent/issue-4647"
	forgetest.SeedRelayBundle(t, repo.Bare, "main", outbox, branch)

	cf := NewReadOnlyCodeForge("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	subjects, err := cf.(forge.BundleCommitSubjects).CommitSubjects(outbox, "main", branch)
	if err != nil {
		t.Fatalf("CommitSubjects: %v", err)
	}
	if len(subjects) != 1 || subjects[0] != "feature" {
		t.Errorf("CommitSubjects = %v, want [feature]", subjects)
	}
	if !srv.sawAuthorised("/git-upload-pack") {
		t.Error("server saw no authorised git-upload-pack request")
	}
	if srv.sawAuthorised("/git-receive-pack") {
		t.Error("CommitSubjects pushed: server saw a git-receive-pack request, want it read-only")
	}
}

func TestExecClient_PushBranch_AuthenticatesOverHTTPSNotSSH(t *testing.T) {
	repo, srv := newRelayHTTPSHarness(t)
	src := t.TempDir()
	forgetest.Run(t, "", "clone", repo.Bare, src)
	forgetest.Run(t, src, "checkout", "-b", "work")
	forgetest.WriteFile(t, filepath.Join(src, "feature.txt"), "feature\n")
	forgetest.Run(t, src, "add", "feature.txt")
	forgetest.Run(t, src, "commit", "-m", "feature")
	wantSHA := forgetest.RevParse(t, src, "work")

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
	branch := "agent/issue-4647"
	if err := c.PushBranch(src, "work", branch, "main"); err != nil {
		t.Fatalf("PushBranch: %v", err)
	}
	if got := forgetest.RevParse(t, repo.Bare, "refs/heads/"+branch); got != wantSHA {
		t.Errorf("refs/heads/%s = %s, want %s", branch, got, wantSHA)
	}
	for _, suffix := range []string{"/git-upload-pack", "/git-receive-pack"} {
		if !srv.sawAuthorised(suffix) {
			t.Errorf("server saw no authorised %s request", suffix)
		}
	}
}

// The merge gate's Rebase must clone and force-push over the token-authenticated
// HTTPS remote too, not through `gh repo clone` (issue #4650).
func TestExecClient_Rebase_AuthenticatesOverHTTPSNotSSH(t *testing.T) {
	h := newCodeForgeHarness(t)
	url := h.SeedLandable("4650")
	h.AdvanceBase()

	if err := h.cf.Rebase(url); err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	if !h.Rebased("4650") {
		t.Errorf("%s does not contain main's tip after Rebase", h.branchName("4650"))
	}
	for _, suffix := range []string{"/git-upload-pack", "/git-receive-pack"} {
		if !h.srv.sawAuthorised(suffix) {
			t.Errorf("server saw no authorised %s request", suffix)
		}
	}
}

// The launcher's token refresh does os.Setenv("GH_TOKEN", v) mid-run
// (bootstrap.go); the credential helper must read the live value on each git
// invocation, not a token captured when the relay was constructed.
func TestReadOnlyCodeForge_RelayBundle_HonoursRefreshedGHToken(t *testing.T) {
	repo, srv := newRelayHTTPSHarness(t)
	cf := NewReadOnlyCodeForge("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	relay := func(branch string) error {
		outbox := t.TempDir()
		forgetest.SeedRelayBundle(t, repo.Bare, "main", outbox, branch)
		return cf.(forge.BundleRelay).RelayBundle(outbox, branch)
	}

	if err := relay("agent/issue-1"); err != nil {
		t.Fatalf("RelayBundle with the initial token: %v", err)
	}

	srv.expectToken("refreshed-token")
	if err := relay("agent/issue-2"); err == nil {
		t.Fatal("RelayBundle with a stale GH_TOKEN: got nil error, want the server to reject it")
	}

	t.Setenv("GH_TOKEN", "refreshed-token")
	srv.reset()
	if err := relay("agent/issue-3"); err != nil {
		t.Fatalf("RelayBundle after the GH_TOKEN refresh: %v", err)
	}
	if !srv.sawAuthorised("/git-receive-pack") {
		t.Error("server saw no authorised git-receive-pack after the refresh")
	}
}
