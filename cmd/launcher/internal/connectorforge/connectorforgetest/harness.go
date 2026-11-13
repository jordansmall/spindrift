// Package connectorforgetest is a scripted forgejo-shaped backend, a loaded
// fixture push-only forge Connector and a real bare git repo behind one
// forgetest.CodeForgeHarness, so the composed Connector forge is driven through
// the same seams production uses: the Route table, the audit log and core's own
// git plumbing.
package connectorforgetest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/connectorforge"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/fixturebuild"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
	"spindrift.dev/launcher/internal/forge/git"
	"spindrift.dev/launcher/internal/gitexec"
	"spindrift.dev/launcher/internal/retry"
)

const (
	// Repo is the repository the scripted backend serves.
	Repo = "o/r"
	// Token is the bearer token the host attaches to every Connector request.
	Token = "s3cr3t-token"

	apiPath    = "/api/v1"
	branchPath = apiPath + "/repos/" + Repo + "/branches/"
	prefix     = "agent/issue-"
)

// Backend is the scripted forgejo-shaped REST backend. Branch requests are
// answered from the real bare repo, so BranchExists reflects reality.
type Backend struct {
	*httptest.Server
	// Host is the backend's host:port, the one host its Route table declares.
	Host string

	bare string

	mu       sync.Mutex
	clone    string
	web      string
	failures []int
	branch   int
	remotes  int
}

func newBackend(t *testing.T, bare string) *Backend {
	t.Helper()
	b := &Backend{bare: bare}
	b.Server = httptest.NewTLSServer(http.HandlerFunc(b.serve))
	t.Cleanup(b.Close)
	b.Host = b.Listener.Addr().String()
	b.clone = "https://" + b.Host + "/" + Repo + ".git"
	b.web = "https://" + b.Host + "/" + Repo
	return b
}

// SetRemote scripts the clone_url and html_url the backend serves, for the
// hostile cases.
func (b *Backend) SetRemote(clone, web string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.clone, b.web = clone, web
}

// FailBranchRequests makes the next len(statuses) branch requests answer those
// statuses, in order, before the real answer resumes.
func (b *Backend) FailBranchRequests(statuses ...int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures = append(b.failures, statuses...)
}

// BranchRequests is how many branch GET and DELETE requests the backend has seen.
func (b *Backend) BranchRequests() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.branch
}

// RemoteRequests is how many repository-identity requests the backend has seen.
func (b *Backend) RemoteRequests() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.remotes
}

func (b *Backend) serve(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == apiPath+"/repos/"+Repo && r.Method == http.MethodGet:
		b.mu.Lock()
		b.remotes++
		clone, web := b.clone, b.web
		b.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"clone_url": clone, "html_url": web})
	case strings.HasPrefix(r.URL.Path, branchPath):
		b.mu.Lock()
		b.branch++
		var scripted int
		if len(b.failures) > 0 {
			scripted, b.failures = b.failures[0], b.failures[1:]
		}
		b.mu.Unlock()
		if scripted != 0 {
			http.Error(w, "scripted failure", scripted)
			return
		}
		b.branchAnswer(w, r, strings.TrimPrefix(r.URL.Path, branchPath))
	default:
		http.NotFound(w, r)
	}
}

func (b *Backend) branchAnswer(w http.ResponseWriter, r *http.Request, branch string) {
	ref := "refs/heads/" + branch
	exists := exec.Command("git", "-C", b.bare, "rev-parse", "--verify", "--quiet", ref).Run() == nil
	switch {
	case !exists:
		http.NotFound(w, r)
	case r.Method == http.MethodDelete:
		if out, err := exec.Command("git", "-C", b.bare, "branch", "-D", branch).CombinedOutput(); err != nil {
			http.Error(w, string(out), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusOK)
	}
}

// stack is one backend with a Connector loaded against it.
type stack struct {
	backend *Backend
	conn    *connectorhost.Connector
	audit   *bytes.Buffer
	target  connectorforge.Target
}

func load(t *testing.T, bare string) *stack {
	t.Helper()
	b := newBackend(t, bare)
	dir, err := fixturebuild.ConnectorDir()
	if err != nil {
		t.Fatal(err)
	}
	wasm, err := os.ReadFile(filepath.Join(dir, "forge.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	audit := &bytes.Buffer{}
	route := func(method string) connectorhost.Route {
		return connectorhost.Route{Method: method, Host: b.Host, Path: apiPath + "/repos/" + Repo}
	}
	c, err := connectorhost.LoadNetwork(context.Background(), wasm, connectorhost.Network{
		Name:   "fixture-forge",
		Routes: []connectorhost.Route{route(http.MethodGet), route(http.MethodDelete)},
		Authorize: func(r *http.Request) error {
			r.Header.Set("Authorization", "Bearer "+Token)
			return nil
		},
		Client: b.Client(),
		Audit:  connectorhost.NewAuditLog(audit),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return &stack{b, c, audit, connectorforge.Target{API: b.URL + apiPath, Repo: Repo}}
}

// Harness implements forgetest.CodeForgeHarness and forgetest.PushOnly over the
// composed Connector forge.
type Harness struct {
	t *testing.T
	// Repo is the real bare repo behind the backend.
	Repo *forgetest.GitRepoFixture
	// Backend is the scripted forge the Connector talks to.
	Backend *Backend
	// Conn is the loaded fixture Connector.
	Conn *connectorhost.Connector
	// Audit receives the Connector's audit log.
	Audit *bytes.Buffer
	// Credential is core's git credential handed to Compose.
	Credential gitexec.Credential

	target   connectorforge.Target
	mu       sync.Mutex
	sleeps   []time.Duration
	plumbing []PlumbingCall
	cf       forge.CodeForge
	caps     forge.Capabilities
}

// PlumbingCall is one call Compose made to build core's git plumbing.
type PlumbingCall struct {
	CloneURL   string
	Credential gitexec.Credential
}

// New starts the backend, loads the fixture Connector and composes the forge
// with the default, valid remote.
func New(t *testing.T) *Harness {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "Test Bot")
	t.Setenv("GIT_AUTHOR_EMAIL", "bot@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test Bot")
	t.Setenv("GIT_COMMITTER_EMAIL", "bot@example.com")

	repo := forgetest.NewGitRepoFixture(t, "main")
	s := load(t, repo.Bare)
	h := &Harness{
		t: t, Repo: repo, Backend: s.backend, Conn: s.conn, Audit: s.audit, target: s.target,
		Credential: gitexec.Credential{Username: "bot", Password: "git-secret"},
	}
	// Production code is https-only, so git is pointed at the bare repo through
	// the environment: no config file, and Credential.Apply extends the count.
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url."+repo.Bare+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", s.backend.clone)

	cf, caps, err := h.Compose()
	if err != nil {
		t.Fatal(err)
	}
	h.cf, h.caps = cf, caps
	return h
}

func (h *Harness) git() connectorforge.Git {
	return connectorforge.Git{
		Credential: h.Credential,
		Plumbing: func(u string, cred gitexec.Credential) forge.CodeForge {
			h.mu.Lock()
			h.plumbing = append(h.plumbing, PlumbingCall{u, cred})
			h.mu.Unlock()
			return git.NewGitClient(u, "main", "Test Bot", "bot@example.com", git.WithCredential(cred))
		},
		Clock: retry.Clock{Now: time.Now, Sleep: func(d time.Duration) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.sleeps = append(h.sleeps, d)
		}},
	}
}

// Compose composes a forge from the harness's Connector as it stands, so a test
// can script the backend first.
func (h *Harness) Compose() (forge.CodeForge, forge.Capabilities, error) {
	return connectorforge.Compose(h.Conn, h.target, h.git())
}

// PlumbingCalls returns every call Compose made to build the git plumbing.
func (h *Harness) PlumbingCalls() []PlumbingCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]PlumbingCall(nil), h.plumbing...)
}

// Capabilities are the capabilities the New-time Compose resolved.
func (h *Harness) Capabilities() forge.Capabilities { return h.caps }

// Sleeps returns the backoff sleeps the composed forge's retries took.
func (h *Harness) Sleeps() []time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]time.Duration(nil), h.sleeps...)
}

// Forge returns the composed forge.
func (h *Harness) Forge() forge.CodeForge { return h.cf }

// Unreachable composes against a second backend, then closes it so Probe fails.
func (h *Harness) Unreachable() forge.CodeForge {
	h.t.Helper()
	s := load(h.t, h.Repo.Bare)
	cf, _, err := connectorforge.Compose(s.conn, s.target, h.git())
	if err != nil {
		h.t.Fatal(err)
	}
	s.backend.Close()
	return cf
}

func (h *Harness) BranchPrefix() string { return prefix }

func (h *Harness) IsPushOnly() {}

func (h *Harness) SeedLandable(num string) string {
	h.Repo.SeedBranch(forge.AgentBranchName(prefix, num), num)
	return forge.AgentBranchName(prefix, num)
}

func (h *Harness) AdvanceBase() { h.Repo.AdvanceBase() }

func (h *Harness) Landed(num string) bool { return h.Repo.Landed(num) }

func (h *Harness) Rebased(num string) bool { return h.Repo.Rebased(forge.AgentBranchName(prefix, num)) }

func (h *Harness) FailNextMerge(ref string) { h.Repo.ConflictBase(strings.TrimPrefix(ref, prefix)) }

func (h *Harness) FailNextRebase(ref string) { h.Repo.ConflictBase(strings.TrimPrefix(ref, prefix)) }
