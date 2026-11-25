// Package connectorforgetest is a scripted forgejo-shaped backend, a loaded
// fixture forge Connector and a real bare git repo behind one
// forgetest.CodeForgeHarness (push-only) or forgetest.PRForgeHarness (PR
// capable), so the composed Connector forge is driven through the same seams
// production uses: the Route table, the audit log and core's own git plumbing.
package connectorforgetest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/connectorforge"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectortracker"
	"spindrift.dev/launcher/internal/fixturebuild"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
	"spindrift.dev/launcher/internal/forge/git"
	"spindrift.dev/launcher/internal/gitexec"
	"spindrift.dev/launcher/internal/gitexec/gitexectest"
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

// Scripted is one canned answer the backend serves to a request whose method
// and URL path match, ahead of its own model, for a dialect the model does not
// speak. Body may carry "{host}", answered as the request's Host, for a URL
// back to the backend.
type Scripted struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Status int    `json:"status"`
	Body   string `json:"body"`
}

// Connector is a Connector as the harness loads it: its wasm and its declared
// Route table, whatever its name.
type Connector struct {
	Wasm []byte
	// Routes is the declared table: any host, and "{repo}" for the repository.
	// The harness rebinds every host to the backend's and substitutes Repo.
	Routes []connectorhost.Route
	// Script is served ahead of the backend's model.
	Script []Scripted
}

// ServeScript answers r from the first scripted entry matching its method and
// path, and reports whether it did.
func ServeScript(w http.ResponseWriter, r *http.Request, script []Scripted) bool {
	for _, s := range script {
		if s.Method == r.Method && s.Path == r.URL.Path {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(s.Status)
			_, _ = w.Write([]byte(strings.ReplaceAll(s.Body, "{host}", r.Host)))
			return true
		}
	}
	return false
}

// BoundRoutes is c's declared Routes with Repo substituted and every host
// rebound to the backend's.
func (c Connector) BoundRoutes(host string) []connectorhost.Route {
	routes := connectortracker.Routes(c.Routes, Repo)
	for i := range routes {
		routes[i].Host = host
	}
	return routes
}

// FixtureWasm reads the named fixture Connector wasm file.
func FixtureWasm(t *testing.T, wasmFile string) []byte {
	t.Helper()
	dir, err := fixturebuild.ConnectorDir()
	if err != nil {
		t.Fatal(err)
	}
	wasm, err := os.ReadFile(filepath.Join(dir, wasmFile))
	if err != nil {
		t.Fatal(err)
	}
	return wasm
}

// fixtureConnector is the named fixture wasm file with one Route per method over
// the repository's subtree.
func fixtureConnector(t *testing.T, wasmFile string, methods []string) Connector {
	t.Helper()
	c := Connector{Wasm: FixtureWasm(t, wasmFile)}
	for _, m := range methods {
		c.Routes = append(c.Routes, connectorhost.Route{Method: m, Host: "forge.example", Path: apiPath + "/repos/{repo}"})
	}
	return c
}

// Backend is the scripted forgejo-shaped REST backend. Branch requests are
// answered from the real bare repo, so BranchExists reflects reality.
type Backend struct {
	*httptest.Server
	// Host is the backend's host:port, the one host its Route table declares.
	Host string

	bare   string
	script []Scripted

	mu       sync.Mutex
	web      string
	webPath  string
	token    string
	failures []int
	branch   int

	// gitHandler, when set, serves smart HTTP under the clone URL's owner.
	gitHandler *gitexectest.AuthHandler

	prs prStore
}

func newBackend(t *testing.T, bare string) *Backend {
	t.Helper()
	b := &Backend{bare: bare}
	b.Server = httptest.NewTLSServer(http.HandlerFunc(b.serve))
	t.Cleanup(b.Close)
	b.Host = b.Listener.Addr().String()
	b.web = "https://" + b.Host + "/" + Repo
	return b
}

// underWebPath moves the backend's web URLs under webPath.
func (b *Backend) underWebPath(webPath string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.webPath = webPath
	b.web = "https://" + b.Host + webPath + "/" + Repo
}

// SetWeb scripts the web URL the backend serves for the repository and its PRs.
func (b *Backend) SetWeb(web string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.web = web
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

// SetToken makes the backend refuse any request that does not carry token, as
// the Connector's bearer or as git's basic-auth password, from now on. It is
// how a test rotates the credential the backend accepts mid-run.
func (b *Backend) SetToken(token string) {
	b.mu.Lock()
	b.token = token
	g := b.gitHandler
	b.mu.Unlock()
	if g != nil {
		g.SetPassword(token)
	}
}

func (b *Backend) serve(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	b.prs.requests++
	gitHandler := b.gitHandler
	webPath := b.webPath
	token := b.token
	b.mu.Unlock()
	isGit := gitHandler != nil && strings.HasPrefix(r.URL.Path, webPath+"/"+path.Dir(Repo)+"/")
	if token != "" && !isGit && r.Header.Get("Authorization") != "Bearer "+token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if isGit {
		gitHandler.ServeHTTP(w, r)
		return
	}
	if ServeScript(w, r, b.script) {
		return
	}
	if strings.HasSuffix(r.URL.Path, "/merge") {
		b.mu.Lock()
		b.prs.merges++
		b.mu.Unlock()
	}
	switch {
	case r.URL.Path == apiPath+"/repos/"+Repo && r.Method == http.MethodGet:
		b.mu.Lock()
		web, allowRebase := b.web, b.prs.allowRebase
		b.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"html_url": web, "full_name": Repo, "allow_rebase": allowRebase,
		})
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
	case strings.HasPrefix(r.URL.Path, apiPath+"/repos/"+Repo+"/"):
		b.servePR(w, r, strings.TrimPrefix(r.URL.Path, apiPath+"/repos/"+Repo))
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
		b.mu.Lock()
		protected := b.prs.protected[branch]
		b.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"protected": protected})
	}
}

// stack is one backend with a Connector loaded against it.
type stack struct {
	backend *Backend
	conn    *connectorhost.Connector
	audit   *bytes.Buffer
	target  connectorforge.Target
}

// pushOnlyMethods are all the push-only fixture's Route table needs; a PR forge
// also writes.
var (
	pushOnlyMethods = []string{http.MethodGet, http.MethodDelete}
	prMethods       = []string{http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete}
)

// load starts a backend and loads the Connector against it, its declared routes
// rebound to the backend's host.
func load(t *testing.T, eng Engine, bare string, c Connector, tokenEnv string) *stack {
	t.Helper()
	b := newBackend(t, bare)
	b.script = c.Script
	audit := &bytes.Buffer{}
	authorizer := func(r *http.Request) error {
		r.Header.Set("Authorization", "Bearer "+Token)
		return nil
	}
	if tokenEnv != "" {
		// The production authorizer, so the rotation test exercises the
		// read-at-use the launcher ships rather than a copy of it.
		authorizer = backendrows.Row{CredentialEnv: tokenEnv}.Authorizer(os.Getenv)
	}
	conn, err := eng(context.Background(), c.Wasm, connectorhost.Network{
		Name:      "fixture-forge",
		Routes:    c.BoundRoutes(b.Host),
		Authorize: authorizer,
		Client:    b.Client(),
		Audit:     connectorhost.NewAuditLog(audit),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return &stack{b, conn, audit, connectorforge.Target{API: b.URL + apiPath, Repo: Repo}}
}

// base is what the push-only and PR harnesses share: one backend, one loaded
// Connector, one bare repo and the composed forge.
type base struct {
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
	// WebBase is the Git remote declaration core hands Compose, the forge's https
	// web base; it defaults to the backend's own, so the derived clone URL is the
	// repository the backend serves.
	WebBase string
	// Note is Git.Note as Compose gets it; nil records nothing.
	Note func(method string, err error)

	connector Connector
	eng       Engine
	target    connectorforge.Target
	mu        sync.Mutex
	sleeps    []time.Duration
	plumbing  []PlumbingCall
	cf        forge.CodeForge
	caps      forge.Capabilities
}

// PlumbingCall is one call Compose made to build core's git plumbing.
type PlumbingCall struct {
	CloneURL   string
	Credential gitexec.Credential
}

// newBase starts the backend, loads the Connector and composes the forge with
// the default, valid remote.
func newBase(t *testing.T, eng Engine, c Connector, opts ...Option) *base {
	t.Helper()
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	t.Setenv("GIT_AUTHOR_NAME", "Test Bot")
	t.Setenv("GIT_AUTHOR_EMAIL", "bot@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test Bot")
	t.Setenv("GIT_COMMITTER_EMAIL", "bot@example.com")

	repo := forgetest.NewGitRepoFixture(t, "main")
	s := load(t, eng, repo.Bare, c, o.tokenEnv)
	user, pass := "bot", "git-secret"
	cred := gitexec.Credential{Username: user, Password: pass}
	if o.tokenEnv != "" {
		pass, cred = os.Getenv(o.tokenEnv), gitexec.EnvCredential(user, o.tokenEnv)
		s.backend.SetToken(pass)
	}
	s.backend.underWebPath(o.webPath)
	h := &base{
		t: t, eng: eng, connector: c, Repo: repo, Backend: s.backend, Conn: s.conn, Audit: s.audit, target: s.target,
		Credential: cred, WebBase: "https://" + s.backend.Host + o.webPath,
	}
	if o.gitOverHTTPS {
		t.Setenv("GIT_CONFIG_COUNT", "0")
		g := gitexectest.ServeTLS(t, s.backend.Server, strings.TrimPrefix(o.webPath+"/", "/")+Repo+".git", repo.Bare, user, pass)
		s.backend.mu.Lock()
		s.backend.gitHandler = g
		s.backend.mu.Unlock()
	} else {
		// Production code is https-only, so git is pointed at the bare repo through
		// the environment: no config file, and Credential.Apply extends the count.
		t.Setenv("GIT_CONFIG_COUNT", "1")
		t.Setenv("GIT_CONFIG_KEY_0", "url."+repo.Bare+".insteadOf")
		t.Setenv("GIT_CONFIG_VALUE_0", "https://"+s.backend.Host+o.webPath+"/"+Repo+".git")
	}

	cf, caps, err := h.Compose()
	if err != nil {
		t.Fatal(err)
	}
	h.cf, h.caps = cf, caps
	return h
}

// GitRequest is one git request the backend received over smart HTTP.
type GitRequest = gitexectest.Request

// GitRequests returns every git request the backend received, in arrival
// order; empty unless the harness was built WithGitOverHTTPS.
func (h *base) GitRequests() []GitRequest {
	h.Backend.mu.Lock()
	g := h.Backend.gitHandler
	h.Backend.mu.Unlock()
	if g == nil {
		return nil
	}
	return g.Requests()
}

func (h *base) git() connectorforge.Git {
	return connectorforge.Git{
		Credential: h.Credential, WebBase: h.WebBase, Note: h.Note,
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
func (h *base) Compose() (forge.CodeForge, forge.Capabilities, error) {
	return connectorforge.Compose(h.Conn, h.target, h.git())
}

// PlumbingCalls returns every call Compose made to build the git plumbing.
func (h *base) PlumbingCalls() []PlumbingCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]PlumbingCall(nil), h.plumbing...)
}

// Capabilities are the capabilities the New-time Compose resolved.
func (h *base) Capabilities() forge.Capabilities { return h.caps }

// Sleeps returns the backoff sleeps the composed forge's retries took.
func (h *base) Sleeps() []time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]time.Duration(nil), h.sleeps...)
}

// Harness implements forgetest.CodeForgeHarness and forgetest.PushOnly over the
// composed push-only Connector forge.
type Harness struct{ *base }

// New starts the backend, loads the fixture push-only Connector and composes the
// forge with the default, valid remote.
func New(t *testing.T, opts ...Option) *Harness {
	t.Helper()
	return NewFor(t, fixtureConnector(t, "forge.wasm", pushOnlyMethods), opts...)
}

// NewFor is New over a Connector's own wasm and declared Route table.
func NewFor(t *testing.T, c Connector, opts ...Option) *Harness {
	t.Helper()
	return &Harness{newBase(t, inProcess, c, opts...)}
}

// Forge returns the composed forge.
func (h *Harness) Forge() forge.CodeForge { return h.cf }

// Unreachable composes against a second backend, then closes it so Probe fails.
func (h *Harness) Unreachable() forge.CodeForge {
	h.t.Helper()
	s := load(h.t, h.eng, h.Repo.Bare, h.connector, "")
	cf, _, err := connectorforge.Compose(s.conn, s.target, h.git())
	if err != nil {
		h.t.Fatal(err)
	}
	s.backend.Close()
	return cf
}

func (h *base) BranchPrefix() string { return prefix }

// BranchForge returns the composed forge, whichever harness embeds base.
func (h *base) BranchForge() forge.CodeForge { return h.cf }

func (h *Harness) IsPushOnly() {}

func (h *base) SeedLandable(num string) string {
	h.Repo.SeedBranch(forge.AgentBranchName(prefix, num), num)
	return forge.AgentBranchName(prefix, num)
}

func (h *base) AdvanceBase() { h.Repo.AdvanceBase() }

func (h *base) Landed(num string) bool { return h.Repo.Landed(num) }

func (h *base) Rebased(num string) bool { return h.Repo.Rebased(forge.AgentBranchName(prefix, num)) }

func (h *Harness) FailNextMerge(ref string) { h.Repo.ConflictBase(strings.TrimPrefix(ref, prefix)) }

func (h *Harness) FailNextRebase(ref string) { h.Repo.ConflictBase(strings.TrimPrefix(ref, prefix)) }
