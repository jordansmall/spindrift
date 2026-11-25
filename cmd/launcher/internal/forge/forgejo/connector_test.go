package forgejo_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/connectorforge"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectortracker"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
	"spindrift.dev/launcher/internal/forge/git"
	"spindrift.dev/launcher/internal/gitexec"
	"spindrift.dev/launcher/internal/retry"
)

const (
	connectorWasmEnv   = "SPINDRIFT_BACKEND_CONNECTOR_WASM"
	connectorRoutesEnv = "SPINDRIFT_BACKEND_CONNECTOR_ROUTES"
	connectorToken     = "connector-test-token"
	connectorRepo      = "owner/repo"
)

// connectorHarness drives the compiled forgejo Connector, through the
// Connector host, against the scripted backend. cf and caps are the Code Forge
// composed from the Connector alone, whose PRForge is caps.PRForge; the fake's
// seeders are promoted, so the harnesses below script the backend directly.
type connectorHarness struct {
	*fakeForgejo
	t     *testing.T
	audit *bytes.Buffer
	// auditPath is the production constructor's audit file, which audit is read
	// from; empty when the Connector is composed directly.
	auditPath string
	routes    []connectorhost.Route
	conn      *connectorhost.Connector
	cf        forge.CodeForge
	caps      forge.Capabilities
}

// loadForgejoConnector loads the compiled forgejo Connector with its declared
// Route table bound to host and to the repository, the way core resolves a
// Connector row's `{base}` and `{repo}`. It skips the test when no Connector was
// built.
func loadForgejoConnector(t *testing.T, host string, client *http.Client, audit *bytes.Buffer) (*connectorhost.Connector, []connectorhost.Route) {
	t.Helper()
	path, table := declaredRoutes(t)
	var declared []connectorhost.Route
	for _, r := range table {
		r.Host = host
		declared = append(declared, r)
	}
	routes := connectortracker.Routes(declared, connectorRepo)
	wasm, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := connectorhost.LoadNetwork(context.Background(), wasm, connectorhost.Network{
		Name:   "forgejo",
		Routes: routes,
		Authorize: func(r *http.Request) error {
			r.Header.Set("Authorization", "Bearer "+connectorToken)
			return nil
		},
		Client: client,
		Audit:  connectorhost.NewAuditLog(audit),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn, routes
}

// declaredRoutes is the compiled Connector's path and its declared Route table
// with each host still {base}. It skips the test when no Connector was built.
func declaredRoutes(t *testing.T) (string, []connectorhost.Route) {
	t.Helper()
	path := os.Getenv(connectorWasmEnv)
	if path == "" {
		t.Skipf("%s unset: no compiled forgejo Connector to test; connectors/forgejo's connector-forgejo-conformance check runs it", connectorWasmEnv)
	}
	var table []struct {
		Method, Host, Path string
		FollowRedirect     bool
	}
	if err := json.Unmarshal([]byte(os.Getenv(connectorRoutesEnv)), &table); err != nil {
		t.Fatalf("%s: %v", connectorRoutesEnv, err)
	}
	var declared []connectorhost.Route
	for _, r := range table {
		if r.Host != backendrows.BaseHost {
			t.Fatalf("route %s %s names host %q, want %s", r.Method, r.Path, r.Host, backendrows.BaseHost)
		}
		declared = append(declared, connectorhost.Route{Method: r.Method, Host: r.Host, Path: r.Path, FollowRedirect: r.FollowRedirect})
	}
	return path, declared
}

func composeForge(conn *connectorhost.Connector, api string) (forge.CodeForge, forge.Capabilities, error) {
	return connectorforge.Compose(conn, connectorforge.Target{API: api, Repo: connectorRepo}, connectorforge.Git{
		WebBase:    api,
		Credential: gitexec.Credential{Username: "oauth2", Password: connectorToken},
		Plumbing: func(u string, cred gitexec.Credential) forge.CodeForge {
			return git.NewGitClient(u, "main", "Test Bot", "bot@example.com", git.WithCredential(cred))
		},
		Clock: retry.Clock{Now: time.Now, Sleep: func(time.Duration) {}},
	})
}

// newConnectorHarness loads the Connector and composes the Code Forge over it.
// The fake serves over TLS, since core refuses a non-https remote, and git
// reaches its bare repository through the environment: production git is
// https-only.
func newConnectorHarness(t *testing.T, production bool) *connectorHarness {
	t.Helper()
	for k, v := range map[string]string{
		"GIT_AUTHOR_NAME": "Test Bot", "GIT_AUTHOR_EMAIL": "bot@example.com",
		"GIT_COMMITTER_NAME": "Test Bot", "GIT_COMMITTER_EMAIL": "bot@example.com",
	} {
		t.Setenv(k, v)
	}
	f := startFakeForgejo(t, httptest.NewTLSServer)
	f.repo = forgetest.NewGitRepoFixture(t, "main")
	f.token, f.web = connectorToken, f.srv.URL
	// A PR whose head has no branch merges as a plain state flip.
	f.mergeHook = func(num string) error {
		f.mu.Lock()
		head := f.pulls[num].HeadRef
		f.mu.Unlock()
		if !f.branchExists(head) {
			return nil
		}
		return mergeIntoMain(t, f.repo.Bare, head)
	}
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url."+f.repo.Bare+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", f.srv.URL+"/"+connectorRepo+".git")

	h := &connectorHarness{fakeForgejo: f, t: t, audit: &bytes.Buffer{}}
	if production {
		var err error
		if h.cf, h.routes, h.auditPath, err = productionForge(t, f.srv); err != nil {
			t.Fatal(err)
		}
		h.caps = forge.ResolveCodeForge(h.cf)
		return h
	}
	h.conn, h.routes = loadForgejoConnector(t, f.srv.Listener.Addr().String(), f.srv.Client(), h.audit)
	var err error
	if h.cf, h.caps, err = composeForge(h.conn, f.srv.URL); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *connectorHarness) CodeForge() forge.CodeForge { return h.cf }
func (h *connectorHarness) BranchPrefix() string       { return "agent/issue-" }

func (h *connectorHarness) mergeRequests() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.merges
}

func (h *connectorHarness) scriptMerge(statuses ...int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.mergeStatuses = append(h.mergeStatuses, statuses...)
}

// seedLandable seeds a real agent branch one commit ahead of main and an open,
// ready PR for it, so a Merge lands the branch in the bare repo.
func (h *connectorHarness) seedLandable(num string) string {
	h.repo.SeedBranch(forge.AgentBranchName(h.BranchPrefix(), num), num)
	return h.SeedOpenPR(num)
}

func (h *connectorHarness) merged(url string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pulls[prNumFromURL(url)].Merged
}

// composedCodeForgeHarness is the harness the CodeForge contract runs against:
// the composed Code Forge, whose refs are PR URLs.
type composedCodeForgeHarness struct{ *connectorHarness }

var _ forgetest.CodeForgeHarness = composedCodeForgeHarness{}

func (h composedCodeForgeHarness) Forge() forge.CodeForge { return h.cf }

// Unreachable is closedBackendForge: Probe fails against the closed backend.
func (h composedCodeForgeHarness) Unreachable() forge.CodeForge { return h.closedBackendForge() }

// closedBackendForge composes against a second backend, then closes it. Its
// clone URL is outside the insteadOf rewrite, so git cannot reach it either.
func (h *connectorHarness) closedBackendForge() forge.CodeForge {
	h.t.Helper()
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	var cf forge.CodeForge
	var err error
	if h.auditPath != "" {
		cf, _, _, err = productionForge(h.t, srv)
	} else {
		var conn *connectorhost.Connector
		conn, _ = loadForgejoConnector(h.t, srv.Listener.Addr().String(), srv.Client(), &bytes.Buffer{})
		cf, _, err = composeForge(conn, srv.URL)
	}
	if err != nil {
		h.t.Fatal(err)
	}
	srv.Close()
	return cf
}

func (h composedCodeForgeHarness) SeedLandable(num string) string { return h.seedLandable(num) }
func (h composedCodeForgeHarness) AdvanceBase()                   { h.repo.AdvanceBase() }
func (h composedCodeForgeHarness) Landed(num string) bool         { return h.repo.Landed(num) }
func (h composedCodeForgeHarness) Rebased(num string) bool {
	return h.repo.Rebased(forge.AgentBranchName(h.BranchPrefix(), num))
}

// FailNextMerge makes the head and base genuinely conflict, so the forge
// refuses the merge and the PR reports itself unmergeable.
func (h composedCodeForgeHarness) FailNextMerge(ref string) {
	num := prNumFromURL(ref)
	h.repo.ConflictBase(num)
	h.SetMergeable(num, false)
}

func (h composedCodeForgeHarness) FailNextRebase(ref string) {
	h.repo.ConflictBase(prNumFromURL(ref))
}

// composedPRHarness is the harness the PRForge contract runs against: the
// PRForge is the one the composed Code Forge resolves.
type composedPRHarness struct{ *connectorHarness }

var _ forgetest.PRForgeHarness = composedPRHarness{}

func (h composedPRHarness) Forge() forge.PRForge { return h.caps.PRForge }

// composedHostMediationHarness is the harness the host-mediation contract runs
// against: the Code Forge composed for a read-only Box, so core relays the Box's
// bundle in its own git. The tracker half is a forge.Fake and not under test.
type composedHostMediationHarness struct {
	*connectorHarness
	fake *forge.Fake

	mu         sync.Mutex
	relayedSHA map[string]string
	seeded     map[string]bool
	prSeq      int
}

var (
	_ forgetest.HostMediationHarness = (*composedHostMediationHarness)(nil)
	_ forgetest.MirrorSeedHarness    = (*composedHostMediationHarness)(nil)
)

func (h *composedHostMediationHarness) Tracker() forge.IssueTracker { return h.fake.AsIssueFiler() }

func (h *composedHostMediationHarness) SeedBundle(ref string) string {
	outbox := h.t.TempDir()
	sha := forgetest.SeedRelayBundle(h.t, h.repo.Bare, "main", outbox, ref)
	h.mu.Lock()
	h.relayedSHA[ref] = sha
	h.mu.Unlock()
	return outbox
}

func (h *composedHostMediationHarness) BundleLanded(ref string) bool {
	h.mu.Lock()
	want, ok := h.relayedSHA[ref]
	h.mu.Unlock()
	return ok && forgetest.RevParse(h.t, h.repo.Bare, "refs/heads/"+ref) == want
}

func (h *composedHostMediationHarness) EmptyOutbox() string { return h.t.TempDir() }

// SeedDraftPRHead returns a head Forgejo can open a PR for (a branch in the
// repository), or, when failing, one it cannot (no such branch).
func (h *composedHostMediationHarness) SeedDraftPRHead(failing bool) string {
	if failing {
		return "agent/issue-hmfail"
	}
	const head = "agent/issue-hmpr1"
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.seeded[head] {
		h.repo.SeedBranch(head, "hmpr1")
		h.seeded[head] = true
	}
	return head
}

func (h *composedHostMediationHarness) SeedExistingOpenPR() (head, wantURL string) {
	h.mu.Lock()
	h.prSeq++
	num := h.prSeq
	h.mu.Unlock()
	head = "agent/issue-2407-adopt"
	h.repo.SeedBranch(head, "2407")
	return head, h.seedPullHead(strconv.Itoa(num), head, true)
}

func (h *composedHostMediationHarness) SeedCommentTarget(failing bool) string {
	if failing {
		h.fake.CommentErr = errors.New("simulated comment failure")
		return "901"
	}
	h.fake.CommentErr = nil
	return "902"
}

func (h *composedHostMediationHarness) CommentPosted(num, body string) bool {
	for _, c := range h.fake.CommentCalls {
		if c.Num == num && c.Body == body {
			return true
		}
	}
	return false
}

func (h *composedHostMediationHarness) MirrorBase() string { return "main" }

func (h *composedHostMediationHarness) RefSHA(ref string) string {
	return forgetest.RevParse(h.t, h.repo.Bare, "refs/heads/"+ref)
}

// UnseedableCodeForge's backend is closed, and its clone URL has no insteadOf
// rewrite to a local repository, so the clone fails.
func (h *composedHostMediationHarness) UnseedableCodeForge() (forge.CodeForge, string) {
	return h.closedBackendForge(), connectorToken
}

// routeCoverage accumulates, across the harnesses of one test, which of the
// Connector's declared Routes admitted a request.
type routeCoverage struct {
	routes   map[string]bool
	admitted map[string]int
}

// record folds one harness's audit log in and fails the test on a request the
// backend did not serve or the host did not send.
func (c *routeCoverage) record(t *testing.T, h *connectorHarness) {
	t.Helper()
	h.fakeForgejo.mu.Lock()
	unexpected := slices.Clone(h.unexpected)
	h.fakeForgejo.mu.Unlock()
	if len(unexpected) > 0 {
		t.Errorf("backend saw requests outside the Connector's declared work: %q", unexpected)
	}
	if c.routes == nil {
		c.routes, c.admitted = map[string]bool{}, map[string]int{}
	}
	key := map[string]string{}
	for _, r := range h.routes {
		key[r.String()] = r.Method + " " + r.Path
		c.routes[r.Method+" "+r.Path] = true
	}
	if h.auditPath != "" {
		raw, err := os.ReadFile(h.auditPath)
		if err != nil {
			t.Fatal(err)
		}
		h.audit.Write(raw)
	}
	for _, line := range bytes.Split(bytes.TrimSpace(h.audit.Bytes()), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var e connectorhost.AuditEntry
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatalf("audit line %q: %v", line, err)
		}
		if e.Decision != connectorhost.DecisionSent {
			t.Errorf("audit entry %+v: want every request sent", e)
		}
		c.admitted[key[e.Route]]++
	}
}

// assertUsed fails for every declared Route no recorded request used: that
// route is broader than the Connector's primitives need.
func (c *routeCoverage) assertUsed(t *testing.T) {
	t.Helper()
	for r := range c.routes {
		if c.admitted[r] == 0 {
			t.Errorf("route %q admitted no request: the table is broader than the Connector's reads and writes", r)
		}
	}
}

// TestConnector_Contracts runs the shared contracts against the Code Forge
// composed from the compiled Connector, directly and via the production
// constructor. Route coverage is judged over all runs together.
func TestConnector_Contracts(t *testing.T) {
	var cov routeCoverage
	t.Run("Capabilities", func(t *testing.T) {
		h := newConnectorHarness(t, false)
		testComposedCapabilities(t, h)
		cov.record(t, h)
	})
	for _, mode := range []struct {
		name       string
		production bool
	}{{"Composed", false}, {"Production", true}} {
		t.Run(mode.name, func(t *testing.T) {
			t.Run("PRForgeContract", func(t *testing.T) {
				h := newConnectorHarness(t, mode.production)
				forgetest.RunPRForgeContract(t, composedPRHarness{h})
				cov.record(t, h)
			})
			t.Run("CodeForgeContract", func(t *testing.T) {
				h := newConnectorHarness(t, mode.production)
				forgetest.RunCodeForgeContract(t, composedCodeForgeHarness{h})
				cov.record(t, h)
			})
			t.Run("HostMediationContract", func(t *testing.T) {
				h := &composedHostMediationHarness{
					connectorHarness: newConnectorHarness(t, mode.production), fake: forge.NewFake(),
					relayedSHA: map[string]string{}, seeded: map[string]bool{}, prSeq: 1500,
				}
				forgetest.RunHostMediationContract(t, h)
				cov.record(t, h.connectorHarness)
			})
			t.Run("Writes", func(t *testing.T) {
				h := newConnectorHarness(t, mode.production)
				t.Run("CommentPR", func(t *testing.T) { testCommentPR(t, h) })
				t.Run("BranchLifecycle", func(t *testing.T) { testBranchLifecycle(t, h) })
				t.Run("BranchProtection", func(t *testing.T) { testBranchProtection(t, h) })
				t.Run("MergeTransientAndPermanent", func(t *testing.T) { testMergeTransientAndPermanent(t, h) })
				t.Run("MergeRefusedWhileConflictCheckRuns", func(t *testing.T) { testMergeRefusedWhileConflictCheckRuns(t, h) })
				t.Run("HugeJobLogKeepsItsTail", func(t *testing.T) { testHugeJobLogKeepsItsTail(t, h) })
				cov.record(t, h)
			})
		})
	}
	t.Run("TrackerRoutes", func(t *testing.T) {
		w := newTrackerHarness(t)
		forgetest.RunTrackerContract(t, w)
		testTrackerRoutes(t, w)
		cov.record(t, w.connectorHarness)
	})
	t.Run("HostPostedComment", func(t *testing.T) {
		w := newTrackerHarness(t)
		forgetest.RunHostPostedCommentContract(t, w)
		cov.record(t, w.connectorHarness)
	})
	t.Run("IssueFiler", func(t *testing.T) {
		w := newTrackerHarness(t)
		forgetest.RunIssueFilerContract(t, w)
		cov.record(t, w.connectorHarness)
	})
	cov.assertUsed(t)
}

// declaredForgeCaps is exactly the forge capabilities the forgejo Connector's
// handshake declares: no CI run reader, token grant or push-only mode.
var declaredForgeCaps = []string{
	"forge", "forge.branches", "forge.prs", "forge.draftPRs", "forge.autoMerge", "forge.branchProtection", "forge.probe",
}

func testComposedCapabilities(t *testing.T, h *connectorHarness) {
	var got []string
	for _, c := range h.conn.Capabilities {
		if c == "forge" || strings.HasPrefix(c, "forge.") {
			got = append(got, c)
		}
	}
	slices.Sort(got)
	if want := slices.Sorted(slices.Values(declaredForgeCaps)); !slices.Equal(got, want) {
		t.Errorf("handshake forge capabilities = %v, want %v", got, want)
	}
	c := h.caps
	if c.PRForge == nil || c.DraftPRCreator == nil || c.BranchPusher == nil || c.BranchProtectionForge == nil || c.MergeCommitReader == nil || c.BranchDeleter == nil {
		t.Errorf("composed forge lacks a surface the Connector declares: %+v", c)
	}
	if c.CIRunReporter != nil {
		t.Errorf("composed forge offers a CI run reader the Connector does not declare")
	}
}

func testCommentPR(t *testing.T, h *connectorHarness) {
	url := h.SeedOpenPR("408")
	if err := h.caps.PRForge.CommentPR(url, "needs a human"); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if got := h.comments["408"]; !slices.Equal(got, []string{"needs a human"}) {
		t.Errorf("PR comments = %q, want the one posted", got)
	}
}

// BranchExists is exact, not a prefix match; a delete removes the branch and a
// repeat delete of a missing one is reported not-found, which core counts done.
func testBranchLifecycle(t *testing.T, h *connectorHarness) {
	short := forge.AgentBranchName(h.BranchPrefix(), "41")
	long := forge.AgentBranchName(h.BranchPrefix(), "410")
	h.repo.SeedBranch(long, "410")
	if ok, err := h.cf.BranchExists(long); err != nil || !ok {
		t.Fatalf("BranchExists(%q) = (%v, %v), want true", long, ok, err)
	}
	if ok, err := h.cf.BranchExists(short); err != nil || ok {
		t.Fatalf("BranchExists(%q) = (%v, %v), want false: %q only shares its prefix", short, ok, err, long)
	}
	if err := h.caps.BranchDeleter.DeleteBranch(long, "main"); err != nil {
		t.Fatalf("DeleteBranch(%q): %v", long, err)
	}
	if ok, err := h.cf.BranchExists(long); err != nil || ok {
		t.Errorf("BranchExists(%q) after delete = (%v, %v), want false", long, ok, err)
	}
	if err := h.caps.BranchDeleter.DeleteBranch(long, "main"); connectorhost.ErrorClass(err) != connectorhost.NotFound {
		t.Errorf("DeleteBranch(%q) of a missing branch = %v, want a not-found error", long, err)
	}
}

func testBranchProtection(t *testing.T, h *connectorHarness) {
	const open = "agent/issue-420"
	h.repo.SeedBranch(open, "420")
	h.mu.Lock()
	h.protected["main"] = true
	h.mu.Unlock()
	for branch, want := range map[string]bool{"main": true, open: false} {
		if got, err := h.caps.BranchProtectionForge.BranchProtected(branch); err != nil || got != want {
			t.Errorf("BranchProtected(%q) = (%v, %v), want (%v, nil)", branch, got, err, want)
		}
	}
	if _, err := h.caps.BranchProtectionForge.BranchProtected("agent/issue-no-such-branch"); err == nil {
		t.Error("BranchProtected of a missing branch reported an answer, want an error")
	}
}

// A transient merge failure is classed forge.ErrMergeTransient, the class
// settle's merge loop retries (settle's own tests drive that loop), and the
// Connector call itself is not retried; a permanent one is not retryable.
func testMergeTransientAndPermanent(t *testing.T, h *connectorHarness) {
	t.Run("Transient", func(t *testing.T) {
		url := h.seedLandable("421")
		h.scriptMerge(http.StatusBadGateway)
		before := h.mergeRequests()

		err := h.cf.Merge(url)
		if !errors.Is(err, forge.ErrMergeTransient) {
			t.Fatalf("Merge on a 502 = %v, want forge.ErrMergeTransient (the class core retries)", err)
		}
		if n := h.mergeRequests() - before; n != 1 {
			t.Errorf("backend saw %d merge requests, want 1: the Connector call is not retried", n)
		}
		if h.merged(url) || h.repo.Landed("421") {
			t.Fatal("a failed merge landed")
		}

		if err := h.cf.Merge(url); err != nil {
			t.Fatalf("a second Merge, as the merge loop would retry: %v", err)
		}
		if !h.merged(url) || !h.repo.Landed("421") {
			t.Errorf("retry: merged=%v landed=%v, want both", h.merged(url), h.repo.Landed("421"))
		}
	})
	t.Run("Permanent", func(t *testing.T) {
		url := h.seedLandable("422")
		h.scriptMerge(http.StatusMethodNotAllowed)
		before := h.mergeRequests()

		err := h.cf.Merge(url)
		if err == nil || errors.Is(err, forge.ErrMergeTransient) {
			t.Fatalf("Merge on a 405 = %v, want a permanent (non-transient) error", err)
		}
		if n := h.mergeRequests() - before; n != 1 {
			t.Errorf("backend saw %d merge requests, want 1", n)
		}
		if h.merged(url) || h.repo.Landed("422") {
			t.Error("a refused merge landed")
		}
	})
}

// A job log over the host's response cap (connectorhost's maxHTTPResponse,
// 4 MiB) arrives as its tail, not as a failed read: only a followRedirect Route
// makes the host keep the tail of an oversize body.
func testHugeJobLogKeepsItsTail(t *testing.T, h *connectorHarness) {
	url := h.SeedOpenPR("425")
	noise := "noise line\n"
	h.SeedFailingCheckLog(url, "ci/build", "HEAD-MARKER\n"+strings.Repeat(noise, (5<<20)/len(noise))+"FAIL TestHuge: boom\n")

	got, err := h.caps.PRForge.FailureDetail(url)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"FAIL TestHuge: boom", "[log excerpt for \"ci/build\" trimmed"} {
		if !strings.Contains(got, want) {
			t.Errorf("FailureDetail lacks %q", want)
		}
	}
	if strings.Contains(got, "HEAD-MARKER") {
		t.Error("FailureDetail kept the head of a log over the response cap")
	}
}

// Forgejo reports mergeable=false, not unknown, while its async conflict check
// runs, so core re-reads a refused merge's PR once before calling it a conflict.
func testMergeRefusedWhileConflictCheckRuns(t *testing.T, h *connectorHarness) {
	t.Run("SettlesMergeable", func(t *testing.T) {
		url := h.seedLandable("423")
		h.scriptMerge(http.StatusMethodNotAllowed)
		h.ScriptMergeableReads("423", false, true)

		if err := h.cf.Merge(url); !errors.Is(err, forge.ErrMergeBlockedByChecks) {
			t.Fatalf("Merge refused while the PR re-reads mergeable = %v, want forge.ErrMergeBlockedByChecks", err)
		}
	})
	t.Run("StaysConflicting", func(t *testing.T) {
		url := h.seedLandable("424")
		h.scriptMerge(http.StatusMethodNotAllowed)
		h.ScriptMergeableReads("424", false, false)

		if err := h.cf.Merge(url); !errors.Is(err, forge.ErrMergeConflict) {
			t.Fatalf("Merge refused while the PR stays unmergeable = %v, want forge.ErrMergeConflict", err)
		}
	})
}

// mergeIntoMain merges head into main in the bare repo with a real git merge,
// failing on a conflict.
func mergeIntoMain(t *testing.T, bare, head string) error {
	t.Helper()
	work := t.TempDir()
	if out, err := exec.Command("git", "clone", bare, work).CombinedOutput(); err != nil {
		return fmt.Errorf("git clone: %w: %s", err, out)
	}
	if out, err := exec.Command("git", "-C", work, "checkout", "main").CombinedOutput(); err != nil {
		return fmt.Errorf("git checkout main: %w: %s", err, out)
	}
	if out, err := exec.Command("git", "-C", work, "merge", "--no-ff", "origin/"+head, "-m", "merge "+head).CombinedOutput(); err != nil {
		exec.Command("git", "-C", work, "merge", "--abort").Run()
		return fmt.Errorf("merge conflict: %s", out)
	}
	if out, err := exec.Command("git", "-C", work, "push", "origin", "HEAD:main").CombinedOutput(); err != nil {
		return fmt.Errorf("git push: %w: %s", err, out)
	}
	return nil
}
