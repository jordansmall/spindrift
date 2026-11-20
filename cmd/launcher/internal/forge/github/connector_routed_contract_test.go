package github

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

// The contract runs: the Strategy's production constructors under a plain github
// row, so every read and write is the Connector's. The gh on PATH is the
// harness's trap (countGH): any call to it is a native fallback and fails the run.

// routedContractEnv is a plain github row over the scripted backend, with the
// tracker and Code Forge the Strategy builds for it.
type routedContractEnv struct {
	h      *connectorHarness
	shared backendkit.Shared
	tr     forge.IssueTracker
	cf     forge.CodeForge
	calls  func() int
	logDir string
}

func newRoutedContractEnv(t *testing.T) *routedContractEnv {
	t.Helper()
	h, _, calls, logDir := routedLogHarness(t, useGithubRowAt)
	s := backendkit.Shared{
		RepoSlug: connectorRepo, Labels: testLabels, VerdictLabels: forge.ResearchVerdictLabels(),
		BaseBranch: "main", GitUserName: "Test Bot", GitUserEmail: "bot@example.com",
	}
	tr, err := Strategy.NewIssueTracker(s, nil)
	if err != nil {
		t.Fatal(err)
	}
	cf, err := Strategy.NewCodeForge(s, nil, forge.SanitizedParent{}, tr)
	if err != nil {
		t.Fatal(err)
	}
	return &routedContractEnv{h: h, shared: s, tr: tr, cf: cf, calls: calls, logDir: logDir}
}

// assertSplit is the run's proof that the contract stayed on the Connector: gh
// never ran, the Connector sent each wanted read and none was refused, the
// backend saw nothing outside its API, and the primary log holds exactly the
// wantFailed failures the contract injected.
func (e *routedContractEnv) assertSplit(t *testing.T, wantFailed int, wantReads []string) {
	t.Helper()
	if n := e.calls(); n != 0 {
		t.Errorf("the contract ran gh %d times under a plain row", n)
	}

	audits := routedFiles(t, e.logDir, ".audit.jsonl")
	if len(audits) != 1 {
		t.Fatalf("primary audit files = %v, want one", audits)
	}
	raw, err := os.ReadFile(audits[0])
	if err != nil {
		t.Fatal(err)
	}
	var sent []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var a connectorhost.AuditEntry
		if err := json.Unmarshal([]byte(line), &a); err != nil {
			t.Fatalf("audit line %q: %v", line, err)
		}
		if a.Decision != connectorhost.DecisionSent {
			t.Errorf("Connector request %s %s was %s: %s", a.Method, a.Path, a.Decision, a.Reason)
		}
		sent = append(sent, a.Method+" "+a.Path)
	}
	for _, w := range wantReads {
		if !slices.ContainsFunc(sent, func(k string) bool { return strings.HasPrefix(k, w) }) {
			t.Errorf("the Connector sent no %q; sent %q", w, slices.Compact(slices.Sorted(slices.Values(sent))))
		}
	}
	failed := 0
	for _, log := range routedFiles(t, e.logDir, ".log") {
		raw, _ := os.ReadFile(log)
		failed += strings.Count(string(raw), "\n")
	}
	if failed != wantFailed {
		t.Errorf("primary log holds %d failed reads, want %d", failed, wantFailed)
	}
	e.h.backend.mu.Lock()
	unexpected := slices.Clone(e.h.backend.unexpected)
	e.h.backend.mu.Unlock()
	if len(unexpected) > 0 {
		t.Errorf("backend saw requests outside the API: %q", unexpected)
	}
}

// routedTrackerHarness is the tracker contract's harness over a plain row.
type routedTrackerHarness struct{ *routedContractEnv }

var (
	_ forgetest.Harness                 = routedTrackerHarness{}
	_ forgetest.NativeCapable           = routedTrackerHarness{}
	_ forgetest.NativeFailureIsolatable = routedTrackerHarness{}
	_ forgetest.PriorityCapable         = routedTrackerHarness{}
	_ forgetest.CommentCapable          = routedTrackerHarness{}
)

func (p routedTrackerHarness) Tracker() forge.IssueTracker              { return p.tr }
func (p routedTrackerHarness) SeedIssue(i forge.Issue)                  { p.h.SeedIssue(i) }
func (p routedTrackerHarness) FailNativeDeps(n string)                  { p.h.FailNativeDeps(n) }
func (p routedTrackerHarness) SeedNativeDeps(n string, ids []string)    { p.h.SeedNativeDeps(n, ids) }
func (p routedTrackerHarness) SeedComments(n string, c []forge.Comment) { p.h.SeedComments(n, c) }
func (routedTrackerHarness) IsolatesNativeFailure()                     {}
func (routedTrackerHarness) IsPriorityCapable()                         {}

// With a plain github row the tracker contract passes: every read and every
// issue write (claim, transition, verdict, comment) is the Connector's, and gh
// runs nothing.
func TestPlainRow_TrackerContract(t *testing.T) {
	e := newRoutedContractEnv(t)

	forgetest.RunTrackerContract(t, routedTrackerHarness{e})

	// Two failures are recorded: the injected native-deps read, and the verdict
	// the contract settles on an issue that is no longer in progress. The lost
	// claim race it also drives is core policy and stays out of the log.
	e.assertSplit(t, 2,
		[]string{"GET /repos/" + connectorRepo + "/issues", "GET /repos/" + connectorRepo + "/issues/302/dependencies/blocked_by", "POST /graphql", "POST /repos/" + connectorRepo + "/issues"})
}

// routedPRHarness is the PRForge contract's harness over a plain row: the
// seeds and oracles are the scripted backend's, the PRForge under test the
// Strategy's Code Forge.
type routedPRHarness struct {
	composedPRHarness
	cf forge.CodeForge
}

func (p routedPRHarness) Forge() forge.PRForge       { return p.cf.(forge.PRForge) }
func (p routedPRHarness) CodeForge() forge.CodeForge { return p.cf }

// With a plain github row the PRForge contract passes: PR and check reads and
// every PR write, merge included, are the Connector's, and gh runs nothing.
func TestPlainRow_PRForgeContract(t *testing.T) {
	e := newRoutedContractEnv(t)

	forgetest.RunPRForgeContract(t, routedPRHarness{composedPRHarness{e.h}, e.cf})

	e.assertSplit(t, 0,
		[]string{"GET /repos/" + connectorRepo + "/pulls", "GET /repos/" + connectorRepo + "/compare/", "POST /graphql", "PUT /repos/" + connectorRepo + "/pulls/"})
}

// routedCodeForgeHarness is the CodeForge contract's harness over a plain row:
// the Strategy's Code Forge under test, the scripted backend's repo the oracle.
type routedCodeForgeHarness struct {
	composedCodeForgeHarness
	cf forge.CodeForge
}

func (h routedCodeForgeHarness) Forge() forge.CodeForge { return h.cf }

// Unreachable swaps the row for one whose backend is already closed and builds
// the Strategy's Code Forge over it, so Probe fails on the production path. The
// swap outlives the call (the forge resolves its row per call); no contract case
// after Probe needs the reachable backend.
func (h routedCodeForgeHarness) Unreachable() forge.CodeForge {
	h.t.Helper()
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	host := srv.Listener.Addr().String()
	srv.Close()
	useGithubRowAt(h.t, host)
	cf, err := Strategy.NewCodeForge(backendkit.Shared{RepoSlug: connectorRepo, Labels: testLabels}, nil, forge.SanitizedParent{}, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	return cf
}

// With a plain github row the CodeForge contract passes: merge, rebase (core's
// git) and probe all run, and gh runs nothing.
func TestPlainRow_CodeForgeContract(t *testing.T) {
	e := newRoutedContractEnv(t)

	forgetest.RunCodeForgeContract(t, routedCodeForgeHarness{composedCodeForgeHarness{e.h}, e.cf})

	if n := e.calls(); n != 0 {
		t.Errorf("the CodeForge contract ran gh %d times under a plain row", n)
	}
}
