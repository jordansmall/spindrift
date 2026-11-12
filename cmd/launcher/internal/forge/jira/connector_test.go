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
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectortracker"
	"spindrift.dev/launcher/internal/dedupmarker"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
	"spindrift.dev/launcher/internal/forge/jira"
)

const (
	connectorWasmEnv   = "SPINDRIFT_BACKEND_CONNECTOR_WASM"
	connectorRoutesEnv = "SPINDRIFT_BACKEND_CONNECTOR_ROUTES"
	connectorProject   = "PROJ"
	connectorEmail     = "bot@example.com"
	connectorToken     = "connector-test-token"
	jiraTimeLayout     = "2006-01-02T15:04:05.000-0700"
)

// failingWrites is the native adapter's role in a read-only contract run: the
// reads must never reach it.
type failingWrites struct{ t *testing.T }

func (w failingWrites) fail(name string) error {
	w.t.Errorf("read contract called the native adapter's %s", name)
	return nil
}

func (w failingWrites) ListIssues(forge.DispatchState) ([]forge.Issue, error) {
	return nil, w.fail("ListIssues")
}
func (w failingWrites) ListOpenIssues() ([]forge.Issue, error) { return nil, w.fail("ListOpenIssues") }
func (w failingWrites) Issue(string) (forge.Issue, error)      { return forge.Issue{}, w.fail("Issue") }
func (w failingWrites) TransitionState(string, forge.DispatchState, forge.DispatchState) error {
	return w.fail("TransitionState")
}
func (w failingWrites) CompleteVerdict(string, forge.Verdict) error { return w.fail("CompleteVerdict") }
func (w failingWrites) Comment(string, string) error                { return w.fail("Comment") }
func (w failingWrites) Probe() (string, error)                      { return "", w.fail("Probe") }
func (w failingWrites) ListLabels() ([]string, error)               { return nil, w.fail("ListLabels") }
func (w failingWrites) CreateLabel(string, string, string) error    { return w.fail("CreateLabel") }

// connectorHarness drives the compiled jira Connector, through the Connector
// host, against the scripted Jira backend served over TLS (the host requires
// https). Only the tracker, comments, and native-dependency reads are
// scripted; the embedded backend's own native tracker is unset.
type connectorHarness struct {
	*jiraHarness
	cloud  bool
	routes []connectorhost.Route
	audit  *bytes.Buffer
	tr     forge.IssueTracker

	seenMu sync.Mutex
	seen   []string // "METHOD path" of every request the backend received
	badReq []string // requests outside the Connector's reads, or without its credential
}

func newConnectorHarness(t *testing.T, cloud bool) *connectorHarness {
	return newConnectorHarnessWith(t, cloud, connectortracker.Lifecycle{Labels: testLabels})
}

func newConnectorHarnessWith(t *testing.T, cloud bool, lc connectortracker.Lifecycle) *connectorHarness {
	t.Helper()
	path := os.Getenv(connectorWasmEnv)
	if path == "" {
		t.Skipf("%s unset: no compiled jira Connector to test", connectorWasmEnv)
	}
	wasm, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var table []struct{ Method, Host, Path string }
	if err := json.Unmarshal([]byte(os.Getenv(connectorRoutesEnv)), &table); err != nil {
		t.Fatalf("%s: %v", connectorRoutesEnv, err)
	}

	h := &connectorHarness{cloud: cloud, audit: &bytes.Buffer{}}
	wantAuth := "Bearer " + connectorToken
	if cloud {
		req, _ := http.NewRequest(http.MethodGet, "https://example.invalid", nil)
		req.SetBasicAuth(connectorEmail, connectorToken)
		wantAuth = req.Header.Get("Authorization")
	}
	h.jiraHarness = newJiraBackend(t, cloud, func(next http.Handler) *httptest.Server {
		return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h.record(r, wantAuth)
			next.ServeHTTP(w, r)
		}))
	})
	u, err := url.Parse(h.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	// The table spells the site as {base}; the scripted backend stands in for it.
	for _, r := range table {
		if r.Host != "{base}" {
			t.Fatalf("route %s %s names host %q, want {base}", r.Method, r.Path, r.Host)
		}
		h.routes = append(h.routes, connectorhost.Route{Method: r.Method, Host: u.Host, Path: r.Path})
	}

	dialect, name := "server", "jira"
	if cloud {
		dialect = "cloud"
	}
	conn, err := connectorhost.LoadNetwork(context.Background(), wasm, connectorhost.Network{
		Name:   name,
		Routes: h.routes,
		Authorize: func(r *http.Request) error {
			if cloud {
				r.SetBasicAuth(connectorEmail, connectorToken)
			} else {
				r.Header.Set("Authorization", "Bearer "+connectorToken)
			}
			return nil
		},
		Client: h.srv.Client(),
		Audit:  connectorhost.NewAuditLog(h.audit),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	h.tr, err = connectortracker.New(conn, failingWrites{t}, lc,
		connectortracker.Target{API: h.srv.URL, Repo: connectorProject, Dialect: dialect})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// record notes a backend request and flags one a read-only Connector must not
// make: anything but a GET under the search or issue roots, or one lacking the
// host-attached credential.
func (h *connectorHarness) record(r *http.Request, wantAuth string) {
	h.seenMu.Lock()
	defer h.seenMu.Unlock()
	line := r.Method + " " + r.URL.Path
	h.seen = append(h.seen, line)
	read := r.Method == http.MethodGet &&
		(strings.HasPrefix(r.URL.Path, "/rest/api/2/search") || strings.HasPrefix(r.URL.Path, "/rest/api/2/issue/"))
	if !read || r.Header.Get("Authorization") != wantAuth {
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
	var out []connectorhost.AuditEntry
	for _, l := range strings.Split(strings.TrimSpace(h.audit.String()), "\n") {
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
			h := newConnectorHarness(t, fl.cloud)
			forgetest.RunTrackerReadContract(t, h)

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

// The Connector returns the dedup and research markers as fields; core must
// restore them to exactly the text the native adapter reads back.
func TestConnector_MarkersMatchNative(t *testing.T) {
	for _, fl := range connectorFlavours {
		t.Run(fl.name, func(t *testing.T) {
			h := newConnectorHarness(t, fl.cloud)
			email := ""
			if fl.cloud {
				email = connectorEmail
			}
			native := jira.NewJiraClient(jira.JiraConfig{
				BaseURL:    h.srv.URL,
				ProjectKey: connectorProject,
				Email:      email,
				Token:      connectorToken,
				Labels:     testLabels,
				HTTPClient: h.srv.Client(),
			})

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

			nIss, err := native.Issue("PROJ-7")
			if err != nil {
				t.Fatalf("native Issue: %v", err)
			}
			cIss, err := h.tr.Issue("PROJ-7")
			if err != nil {
				t.Fatalf("Connector Issue: %v", err)
			}
			if !strings.Contains(nIss.Body, marker) {
				t.Fatalf("native body lacks the dedup marker %q:\n%s", marker, nIss.Body)
			}
			if cIss.Body != nIss.Body {
				t.Errorf("Connector body differs from native\n got: %q\nwant: %q", cIss.Body, nIss.Body)
			}

			nCom, err := native.(forge.CommentLister).Comments("PROJ-7")
			if err != nil {
				t.Fatalf("native Comments: %v", err)
			}
			cl, ok := forge.As[forge.CommentLister](h.tr)
			if !ok {
				t.Fatal("Connector tracker does not serve comments")
			}
			cCom, err := cl.Comments("PROJ-7")
			if err != nil {
				t.Fatalf("Connector Comments: %v", err)
			}
			if len(nCom) != 2 || !strings.HasPrefix(nCom[0].Body, forge.ResearchMarker+"\n") || strings.Contains(nCom[1].Body, forge.ResearchMarker) {
				t.Fatalf("native comments do not carry the research marker on the marked comment only: %+v", nCom)
			}
			// The native adapter passes Jira's created time through; the Connector
			// re-spells the same instant as RFC3339.
			for i, c := range nCom {
				at, err := time.Parse(jiraTimeLayout, c.CreatedAt)
				if err != nil {
					t.Fatalf("native comment created %q: %v", c.CreatedAt, err)
				}
				nCom[i].CreatedAt = at.Format(time.RFC3339)
			}
			if !slices.Equal(cCom, nCom) {
				t.Errorf("Connector comments differ from native\n got: %+v\nwant: %+v", cCom, nCom)
			}
		})
	}
}

// IsPriorityCapable: core resolves the agent-priority-* label family for every
// Connector-backed tracker, so jira issues read through the Connector carry a
// tier where the native adapter leaves them PriorityNormal.
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
