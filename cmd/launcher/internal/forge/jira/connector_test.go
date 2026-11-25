package jira_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectorrecord"
	"spindrift.dev/launcher/internal/connectortracker"
	"spindrift.dev/launcher/internal/dedupmarker"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
	"spindrift.dev/launcher/internal/forge/jira"
	"spindrift.dev/launcher/internal/hostpaths"
	"spindrift.dev/launcher/internal/retry"
)

const (
	connectorWasmEnv   = "SPINDRIFT_BACKEND_CONNECTOR_WASM"
	connectorRoutesEnv = "SPINDRIFT_BACKEND_CONNECTOR_ROUTES"
	connectorProject   = "PROJ"
	connectorEmail     = "bot@example.com"
	connectorUsername  = "svc-bot"
	connectorToken     = "connector-test-token"
	jiraTimeLayout     = "2006-01-02T15:04:05.000-0700"
)

// connectorHarness drives the compiled jira Connector, through the Connector
// host, against the scripted Jira backend served over TLS (the host requires
// https).
type connectorHarness struct {
	*jiraHarness
	cloud  bool
	routes []connectorhost.Route
	audit  *bytes.Buffer
	// auditPath, when set, is the file the tracker audits to in place of audit.
	auditPath string
	tr        forge.IssueTracker

	seenMu sync.Mutex
	seen   []string // "METHOD path" of every request the backend received
	badReq []string // requests outside the Connector's reads, or without its credential
}

func newConnectorHarness(t *testing.T, cloud bool) *connectorHarness {
	return newConnectorHarnessWith(t, cloud, connectortracker.Lifecycle{Labels: testLabels})
}

func newConnectorHarnessWith(t *testing.T, cloud bool, lc connectortracker.Lifecycle) *connectorHarness {
	t.Helper()
	h, path, table := newConnectorBackend(t, cloud)
	wasm, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(h.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	// The table spells the site as {base}; the scripted backend stands in for it.
	for _, r := range table {
		h.routes = append(h.routes, connectorhost.Route{Method: r.Method, Host: u.Host, Path: r.Path})
	}

	h.routes = connectortracker.Routes(h.routes, connectorProject)

	dialect, name := "server", "jira"
	if cloud {
		dialect = "cloud"
	}
	conn, err := connectorhost.LoadNetwork(context.Background(), wasm, connectorhost.Network{
		Name:   name,
		Routes: h.routes,
		Authorize: func(r *http.Request) error {
			r.SetBasicAuth(connectorIdentity(cloud), connectorToken)
			return nil
		},
		Client: h.srv.Client(),
		Audit:  connectorhost.NewAuditLog(h.audit),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	if lc.WebHost == "" {
		lc.WebHost = u.Host
	}
	h.tr, err = connectortracker.New(conn, lc,
		connectortracker.Target{API: h.srv.URL, Repo: connectorProject, Dialect: dialect}, retry.NoSleepClock())
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// connectorIdentity is the Basic identity the operator sets: an email on
// Cloud, a username on Server.
func connectorIdentity(cloud bool) string {
	if cloud {
		return connectorEmail
	}
	return connectorUsername
}

// newConnectorBackend serves the scripted backend over TLS for a Connector
// harness and returns the compiled module's path and the route table, whose
// hosts are still {base}. The backend checks that each request carries the
// credential the host attaches: Basic in both dialects, as the production row
// has one scheme and JIRA_EMAIL is the identity on Server too.
func newConnectorBackend(t *testing.T, cloud bool) (*connectorHarness, string, []connectorhost.Route) {
	t.Helper()
	path := os.Getenv(connectorWasmEnv)
	if path == "" {
		t.Skipf("%s unset: no compiled jira Connector to test; connectors/jira's connector-jira-conformance check runs it", connectorWasmEnv)
	}
	var table []connectorhost.Route
	if err := json.Unmarshal([]byte(os.Getenv(connectorRoutesEnv)), &table); err != nil {
		t.Fatalf("%s: %v", connectorRoutesEnv, err)
	}
	for _, r := range table {
		if r.Host != "{base}" {
			t.Fatalf("route %s %s names host %q, want {base}", r.Method, r.Path, r.Host)
		}
	}

	h := &connectorHarness{cloud: cloud, audit: &bytes.Buffer{}}
	req, _ := http.NewRequest(http.MethodGet, "https://example.invalid", nil)
	req.SetBasicAuth(connectorIdentity(cloud), connectorToken)
	wantAuth := req.Header.Get("Authorization")
	h.jiraHarness = newJiraBackend(t, cloud, func(next http.Handler) *httptest.Server {
		return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h.record(r, wantAuth)
			next.ServeHTTP(w, r)
		}))
	})
	return h, path, table
}

// connectorBuild is how a harness builds its tracker: Direct hands the compiled
// Connector to connectortracker.New with native writes that fail every call;
// Production is jira.Strategy.NewIssueTracker, which has no fallback.
type connectorBuild struct {
	name       string
	production bool
}

var connectorBuilds = []connectorBuild{{"Direct", false}, {"Production", true}}

// harness builds the harness for tracker kind tr under the given status
// mapping and lifecycle, in testLabels.
func (b connectorBuild) harness(t *testing.T, cloud bool, tr dispatchkind.Tracker, mapping, lifecycle, issueType string) *connectorHarness {
	t.Helper()
	if b.production {
		return newProductionHarness(t, cloud, tr, mapping, lifecycle, issueType)
	}
	lc, err := connectorLifecycle(tr, testLabels, forge.ResearchVerdictLabels(), mapping, lifecycle, issueType)
	if err != nil {
		t.Fatal(err)
	}
	return newConnectorHarnessWith(t, cloud, lc)
}

// newProductionHarness drives the tracker the production constructor builds
// from a jira row, as the launcher would: the row, the knobs and the
// credential env are the operator's, only the site is the scripted backend.
func newProductionHarness(t *testing.T, cloud bool, tr dispatchkind.Tracker, mapping, lifecycle, issueType string, adjust ...func(*backendrows.Row)) *connectorHarness {
	t.Helper()
	h, err := tryProductionHarness(t, cloud, tr, mapping, lifecycle, issueType, adjust...)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// tryProductionHarness is newProductionHarness returning the constructor's error.
func tryProductionHarness(t *testing.T, cloud bool, tr dispatchkind.Tracker, mapping, lifecycle, issueType string, adjust ...func(*backendrows.Row)) (*connectorHarness, error) {
	t.Helper()
	h, path, table := newConnectorBackend(t, cloud)
	t.Chdir(t.TempDir()) // the tracker's audit file lands under the host log dir
	pwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	h.auditPath = filepath.Join(hostpaths.LogDir(pwd), connectorrecord.Name("jira", ".audit.jsonl"))

	t.Setenv("JIRA_TOKEN", connectorToken)
	r := backendrows.Row{
		Name: "jira", Module: path, Routes: table,
		CredentialEnv: "JIRA_TOKEN", CredentialScheme: backendrows.SchemeBasic,
		IdentityEnv: "JIRA_EMAIL", BaseEnv: jira.JiraBaseURLEnv,
	}
	for _, f := range adjust {
		f(&r)
	}
	row, err := json.Marshal([]backendrows.Row{r})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(backendrows.Snapshot())
	env := map[string]string{
		jira.JiraBaseURLEnv:       h.srv.URL,
		jira.JiraProjectKeyEnv:    connectorProject,
		jira.JiraIssueTypeEnv:     issueType,
		jira.JiraStatusMappingEnv: mapping,
		jira.JiraLifecycleEnv:     lifecycle,
		// The identity reaches the launcher only through the document, never
		// the process env; a Server username must not flip the dialect, which
		// only JIRA_DEPLOYMENT selects.
		jira.JiraEmailEnv: connectorIdentity(cloud),
	}
	if !cloud {
		env[jira.JiraDeploymentEnv] = "server"
	}
	if err := backendrows.Use(row, func(k string) string { return env[k] }); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(jira.ConnectorClientForTest(h.srv.Client()))

	h.tr, err = jira.Strategy.NewIssueTracker(
		backendkit.Shared{Labels: testLabels, VerdictLabels: forge.ResearchVerdictLabels(), Tracker: tr, Connectors: newRuntime(t)},
		func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	if err != nil {
		return nil, err
	}
	live, _ := backendrows.Lookup("jira")
	h.routes = connectortracker.Routes(live.Routes, connectorProject)
	return h, nil
}

// record notes a backend request and flags one a read-only Connector must not
// make: anything outside its Route table (reads, plus Cloud's count, which
// writes nothing), or one lacking the host-attached credential.
func (h *connectorHarness) record(r *http.Request, wantAuth string) {
	h.seenMu.Lock()
	defer h.seenMu.Unlock()
	line := r.Method + " " + r.URL.Path
	h.seen = append(h.seen, line)
	admitted := slices.ContainsFunc(h.routes, func(rt connectorhost.Route) bool {
		return rt.Method == r.Method && strings.HasPrefix(r.URL.Path, rt.Path)
	})
	if !admitted || r.Header.Get("Authorization") != wantAuth {
		h.badReq = append(h.badReq, line)
	}
}

func (h *connectorHarness) Tracker() forge.IssueTracker { return h.tr }

// ID spells the kit's decimal ids as keys of the one project.
func (h *connectorHarness) ID(n string) string { return connectorProject + "-" + n }

func (h *connectorHarness) SeedState(num string, state forge.DispatchState) {
	h.mu.Lock()
	defer h.mu.Unlock()
	rec := h.issues[num]
	rec.labels = append(rec.labels, testLabels.Label(state))
}

func (h *connectorHarness) mutate(num string, f func(*jiraIssueRecord)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	f(h.issues[num])
}

func (h *connectorHarness) auditEntries(t *testing.T) []connectorhost.AuditEntry {
	t.Helper()
	raw := h.audit.String()
	if h.auditPath != "" {
		b, err := os.ReadFile(h.auditPath)
		if err != nil {
			t.Fatal(err)
		}
		raw = string(b)
	}
	var out []connectorhost.AuditEntry
	for _, l := range strings.Split(strings.TrimSpace(raw), "\n") {
		if l == "" {
			continue
		}
		var e connectorhost.AuditEntry
		if err := json.Unmarshal([]byte(l), &e); err != nil {
			t.Fatalf("audit line %q: %v", l, err)
		}
		out = append(out, e)
	}
	return out
}

var (
	_ forgetest.Harness        = (*connectorHarness)(nil)
	_ forgetest.StateSeeder    = (*connectorHarness)(nil)
	_ forgetest.NativeCapable  = (*connectorHarness)(nil)
	_ forgetest.CommentCapable = (*connectorHarness)(nil)
	_ forgetest.IDMinter       = (*connectorHarness)(nil)
)

var connectorFlavours = []struct {
	name  string
	cloud bool
}{{"Server", false}, {"Cloud", true}}

func TestConnector_ReadContracts(t *testing.T) {
	// Cloud's search walks /search/jql, Server's /search: both sit under the
	// one search route, so a route is judged over the union of the two runs.
	admitted := map[string]int{}
	var routes []connectorhost.Route
	for _, fl := range connectorFlavours {
		t.Run(fl.name, func(t *testing.T) {
			// A status no scripted issue holds keeps the contract's states label-
			// selected while still giving the doctor reads a mapping to check.
			h := newConnectorHarnessWith(t, fl.cloud, connectortracker.Lifecycle{
				Labels:    testLabels,
				Statuses:  map[forge.DispatchState]string{forge.Dispatchable: "To Do"},
				IssueType: "Task",
			})
			h.workflowStatuses = []string{"To Do"}
			forgetest.RunTrackerReadContract(t, h)
			exerciseDoctorReads(t, h.tr)

			// Jira has no minimized-comment concept, so the Connector attests the
			// thread itself; it also serves the reverse dependency direction.
			if _, ok := forge.As[forge.CommentLister](h.tr); !ok {
				t.Error("Connector tracker does not serve comments: it must advertise tracker.comments")
			}
			if _, ok := forge.As[forge.BlockersLister](h.tr); !ok {
				t.Error("Connector tracker does not serve BlocksOf: it must advertise tracker.blocks")
			}

			h.seenMu.Lock()
			bad := slices.Clone(h.badReq)
			h.seenMu.Unlock()
			if len(bad) > 0 {
				t.Errorf("backend saw requests outside the Connector's reads: %q", bad)
			}
			key := map[string]string{}
			for _, r := range h.routes {
				key[r.String()] = r.Method + " " + r.Path
			}
			for _, e := range h.auditEntries(t) {
				if e.Decision != connectorhost.DecisionSent {
					t.Errorf("audit entry %+v: want every request sent", e)
				}
				admitted[key[e.Route]]++
			}
			routes = h.routes
		})
	}
	// A route no request used is broader than the reads need.
	for _, r := range routes {
		// The write routes (label edit, transition) and the label list are
		// exercised by TestConnector_TrackerContract, not these read runs.
		if ((r.Method == "PUT" || r.Method == "POST") && r.Path == "/rest/api/2/issue") ||
			(r.Method == "GET" && r.Path == "/rest/api/2/label") {
			continue
		}
		if admitted[r.Method+" "+r.Path] == 0 {
			t.Errorf("route %s %s admitted no request: the table is broader than the Connector's reads", r.Method, r.Path)
		}
	}
}

func TestConnector_BlocksOfReturnsOutwardLinks(t *testing.T) {
	for _, fl := range connectorFlavours {
		t.Run(fl.name, func(t *testing.T) {
			h := newConnectorHarness(t, fl.cloud)
			h.SeedIssue(forge.Issue{Number: "PROJ-7", Title: "blocker"})
			h.mutate("PROJ-7", func(r *jiraIssueRecord) { r.blocks = []string{"PROJ-8", "PROJ-9", "PROJ-8"} })
			h.SeedNativeDeps("PROJ-7", []string{"PROJ-3"})

			bl, ok := forge.As[forge.BlockersLister](h.tr)
			if !ok {
				t.Fatal("Connector tracker is not a forge.BlockersLister")
			}
			got, err := bl.BlocksOf("PROJ-7")
			if err != nil {
				t.Fatalf("BlocksOf: %v", err)
			}
			want := []forge.Dependency{
				{ID: "PROJ-8", Source: forge.DepSourceNative},
				{ID: "PROJ-9", Source: forge.DepSourceNative},
			}
			if !slices.Equal(got, want) {
				t.Errorf("BlocksOf = %v, want %v (outward links only, deduped)", got, want)
			}
			deps, err := forge.DepsOf(h.tr, "PROJ-7")
			if err != nil {
				t.Fatalf("DepsOf: %v", err)
			}
			if wantDeps := []forge.Dependency{{ID: "PROJ-3", Source: forge.DepSourceNative}}; !slices.Equal(deps, wantDeps) {
				t.Errorf("DepsOf = %v, want %v (inward links only)", deps, wantDeps)
			}
		})
	}
}

func TestConnector_KeyRoundTripsAndOutOfGrammarIsRefused(t *testing.T) {
	for _, fl := range connectorFlavours {
		t.Run(fl.name, func(t *testing.T) {
			h := newConnectorHarness(t, fl.cloud)
			h.SeedIssue(forge.Issue{Number: "PROJ-7", Title: "round trip", Labels: []string{testLabels.Dispatchable}})
			got, err := h.tr.Issue("PROJ-7")
			if err != nil {
				t.Fatalf("Issue: %v", err)
			}
			if got.Number != "PROJ-7" {
				t.Errorf("Issue(PROJ-7).Number = %q", got.Number)
			}
			listed, err := h.tr.ListIssues(forge.Dispatchable)
			if err != nil {
				t.Fatalf("ListIssues: %v", err)
			}
			if len(listed) != 1 || listed[0].Number != "PROJ-7" {
				t.Errorf("ListIssues = %+v, want only PROJ-7", listed)
			}

			for _, bad := range []string{"proj-7", "PROJ-07", "PROJ-0", "7"} {
				h.mutate("PROJ-7", func(r *jiraIssueRecord) { r.wireKey = bad })
				if _, err := h.tr.Issue("PROJ-7"); !errors.Is(err, connectortracker.ErrAnswer) {
					t.Errorf("Issue with backend key %q: err = %v, want ErrAnswer", bad, err)
				}
				if _, err := h.tr.ListIssues(forge.Dispatchable); !errors.Is(err, connectortracker.ErrAnswer) {
					t.Errorf("ListIssues with backend key %q: err = %v, want ErrAnswer", bad, err)
				}
			}
			h.mutate("PROJ-7", func(r *jiraIssueRecord) { r.wireKey = "" })

			// A dependency id outside the grammar is refused the same way (DepsOf
			// itself falls back to the body rather than surface the refusal).
			h.SeedNativeDeps("PROJ-7", []string{"PROJ-8", "not-a-key"})
			nr, ok := forge.As[forge.NativeDepsReader](h.tr)
			if !ok {
				t.Fatal("Connector tracker is not a forge.NativeDepsReader")
			}
			if _, err := nr.NativeDepsOf("PROJ-7"); !errors.Is(err, connectortracker.ErrAnswer) {
				t.Errorf("NativeDepsOf with a malformed dependency id: err = %v, want ErrAnswer", err)
			}
		})
	}
}

// The Connector returns the dedup and research markers as fields; core
// restores them to the text a reader greps for: the dedup marker closes the
// issue body and the research marker opens the marked comment only.
func TestConnector_MarkersRestoredOnRead(t *testing.T) {
	for _, fl := range connectorFlavours {
		t.Run(fl.name, func(t *testing.T) {
			h := newConnectorHarness(t, fl.cloud)

			marker := dedupmarker.Build([]string{"alpha", "beta"})
			h.SeedIssue(forge.Issue{Number: "PROJ-7", Title: "finding", Body: "the finding text"})
			h.mutate("PROJ-7", func(r *jiraIssueRecord) {
				prop, _ := json.Marshal(map[string]string{"marker": marker})
				r.props = map[string]json.RawMessage{"spindrift.dedup": prop}
			})
			h.SeedComments("PROJ-7", []forge.Comment{
				{Author: "alice", CreatedAt: "2026-01-02T03:04:05Z", Body: "## Verdict\n\nstatus=recommend"},
				{Author: "bob", CreatedAt: "2026-01-03T04:05:06Z", Body: "plain reply"},
			})
			h.mutate("PROJ-7", func(r *jiraIssueRecord) {
				r.comments[0].Properties = []jiraWireProperty{{Key: "spindrift-research", Value: true}}
			})

			iss, err := h.tr.Issue("PROJ-7")
			if err != nil {
				t.Fatalf("Issue: %v", err)
			}
			if !strings.HasPrefix(iss.Body, "the finding text") || !strings.HasSuffix(iss.Body, marker) {
				t.Errorf("body = %q, want the text closed by the dedup marker %q", iss.Body, marker)
			}

			cl, ok := forge.As[forge.CommentLister](h.tr)
			if !ok {
				t.Fatal("Connector tracker does not serve comments")
			}
			got, err := cl.Comments("PROJ-7")
			if err != nil {
				t.Fatalf("Comments: %v", err)
			}
			want := []forge.Comment{
				{Author: "alice", CreatedAt: "2026-01-02T03:04:05Z", Body: forge.ResearchMarker + "\n## Verdict\n\nstatus=recommend"},
				{Author: "bob", CreatedAt: "2026-01-03T04:05:06Z", Body: "plain reply"},
			}
			if !slices.Equal(got, want) {
				t.Errorf("comments differ\n got: %+v\nwant: %+v", got, want)
			}
		})
	}
}

// IsPriorityCapable: core resolves the agent-priority-* label family for every
// Connector-backed tracker, so jira issues read through the Connector carry a
// tier rather than defaulting to PriorityNormal.
func (h *connectorHarness) IsPriorityCapable() {}

// Jira reports a comment's created time in its own layout; the Connector
// re-spells it as RFC3339, so the scripted backend must speak Jira's.
func (h *connectorHarness) SeedComments(num string, comments []forge.Comment) {
	wire := slices.Clone(comments)
	for i, c := range wire {
		at, err := time.Parse(time.RFC3339, c.CreatedAt)
		if err != nil {
			panic(err)
		}
		wire[i].CreatedAt = at.Format(jiraTimeLayout)
	}
	h.jiraHarness.SeedComments(num, wire)
}

// A state mapped to a native status lists through the compiled Connector by
// that status, not only by its label: the scripted backend matches an issue on
// the status OR the label, so the status-only issue appearing proves the
// status selector reached the backend's JQL.
func TestConnector_ListIssuesSelectsByStatus(t *testing.T) {
	for _, fl := range connectorFlavours {
		t.Run(fl.name, func(t *testing.T) {
			h := newConnectorHarnessWith(t, fl.cloud, connectortracker.Lifecycle{
				Labels:   testLabels,
				Statuses: map[forge.DispatchState]string{forge.Dispatchable: "To Do"},
			})
			for _, num := range []string{"PROJ-1", "PROJ-2", "PROJ-3", "PROJ-4"} {
				h.SeedIssue(forge.Issue{Number: num, Title: num})
			}
			h.mutate("PROJ-1", func(r *jiraIssueRecord) { r.status = "To Do" })
			h.mutate("PROJ-2", func(r *jiraIssueRecord) { r.status = "In Progress" })
			h.mutate("PROJ-3", func(r *jiraIssueRecord) { r.labels = []string{testLabels.Label(forge.Dispatchable)} })

			got, err := h.tr.ListIssues(forge.Dispatchable)
			if err != nil {
				t.Fatalf("ListIssues: %v", err)
			}
			var nums []string
			for _, iss := range got {
				nums = append(nums, iss.Number)
			}
			if want := []string{"PROJ-1", "PROJ-3"}; !slices.Equal(nums, want) {
				t.Errorf("ListIssues(Dispatchable) = %v, want %v (the To Do issue and the labelled one)", nums, want)
			}
		})
	}
}
