package github

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"spindrift.dev/launcher/internal/connectorforge"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectortracker"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

const (
	connectorWasmEnv   = "SPINDRIFT_BACKEND_CONNECTOR_WASM"
	connectorRoutesEnv = "SPINDRIFT_BACKEND_CONNECTOR_ROUTES"
	connectorToken     = "connector-test-token"
	connectorRepo      = "owner/repo"
	connectorWebHost   = "github.com"
)

// restIssue is one issue in the scripted GitHub REST backend.
type restIssue struct {
	num        int
	title      string
	body       string
	labels     []string
	blockedBy  []string
	failNative bool
	pull       bool
}

// scriptedContext is one node of a statusCheckRollup contexts union, in the
// GraphQL wire shape.
type scriptedContext struct {
	TypeName    string `json:"__typename"`
	Name        string `json:"name,omitempty"`
	Conclusion  string `json:"conclusion,omitempty"`
	Summary     string `json:"summary,omitempty"`
	Context     string `json:"context,omitempty"`
	State       string `json:"state,omitempty"`
	Description string `json:"description,omitempty"`
}

// restPR is one pull request in the scripted backend. rollups is the queue of
// states successive rollup reads return; once it is drained the rollup is null
// (no checks reported) unless contexts are scripted, which are served whatever
// the queue holds.
type restPR struct {
	num      int
	branch   string
	draft    bool
	merged   bool
	closed   bool
	rollups  []string
	contexts []scriptedContext
}

// restBackend serves exactly the REST and GraphQL reads the github Connector
// makes and records any other request, so a read outside the Route table's
// subtree fails the test instead of passing silently.
type restBackend struct {
	*httptest.Server
	mu         sync.Mutex
	issues     map[int]*restIssue
	prs        map[int]*restPR
	unexpected []string
}

func newRESTBackend(t *testing.T) *restBackend {
	t.Helper()
	b := &restBackend{issues: map[int]*restIssue{}, prs: map[int]*restPR{}}
	b.Server = httptest.NewTLSServer(http.HandlerFunc(b.serve))
	t.Cleanup(b.Close)
	return b
}

func (b *restBackend) serve(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	reject := func(why string) {
		b.unexpected = append(b.unexpected, r.Method+" "+r.URL.RequestURI()+": "+why)
		http.NotFound(w, r)
	}
	if r.Header.Get("Authorization") != "Bearer "+connectorToken {
		reject("missing host credential")
		return
	}
	if r.URL.Path == "/graphql" {
		b.graphql(w, r, reject)
		return
	}
	if rest, ok := strings.CutPrefix(r.URL.Path, "/repos/"+connectorRepo+"/pulls"); ok {
		b.pulls(w, r, rest, reject)
		return
	}
	base := "/repos/" + connectorRepo + "/issues"
	rest, ok := strings.CutPrefix(r.URL.Path, base)
	if r.Method != http.MethodGet || !ok {
		reject("not a read of the repo's issues")
		return
	}
	q := r.URL.Query()
	switch parts := strings.Split(strings.TrimPrefix(rest, "/"), "/"); {
	case rest == "":
		b.list(w, q)
	case len(parts) == 1:
		if i := b.find(parts[0]); i != nil {
			writeJSON(w, wireIssue(i))
			return
		}
		reject("no such issue")
	case len(parts) == 3 && parts[1] == "dependencies" && parts[2] == "blocked_by":
		i := b.find(parts[0])
		if i == nil {
			reject("no such issue")
			return
		}
		if i.failNative {
			i.failNative = false
			http.Error(w, "scripted native failure", http.StatusInternalServerError)
			return
		}
		deps := []map[string]any{}
		for _, id := range i.blockedBy {
			n, _ := strconv.Atoi(id)
			deps = append(deps, map[string]any{"number": n})
		}
		writeJSON(w, deps)
	default:
		reject("unknown endpoint")
	}
}

func prHTMLURL(num int) string {
	return "https://" + connectorWebHost + "/" + connectorRepo + "/pull/" + strconv.Itoa(num)
}

func wirePR(p *restPR) map[string]any {
	state, mergedAt := "open", any(nil)
	if p.merged || p.closed {
		state = "closed"
	}
	if p.merged {
		mergedAt = "2026-01-01T00:00:00Z"
	}
	return map[string]any{
		"number": p.num, "html_url": prHTMLURL(p.num), "state": state, "merged_at": mergedAt, "draft": p.draft,
	}
}

// pulls serves GET .../pulls?head=owner:branch (exact head match, newest
// first) and GET .../pulls/{n}.
func (b *restBackend) pulls(w http.ResponseWriter, r *http.Request, rest string, reject func(string)) {
	if r.Method != http.MethodGet {
		reject("not a read of the repo's pulls")
		return
	}
	q := r.URL.Query()
	if rest == "" {
		if q.Get("state") != "all" || q.Get("sort") != "created" || q.Get("direction") != "desc" {
			reject("unexpected pulls query")
			return
		}
		owner, _, _ := strings.Cut(connectorRepo, "/")
		var nums []int
		for n, p := range b.prs {
			if q.Get("head") == owner+":"+p.branch {
				nums = append(nums, n)
			}
		}
		sort.Sort(sort.Reverse(sort.IntSlice(nums)))
		out := []any{}
		for _, n := range nums {
			out = append(out, wirePR(b.prs[n]))
		}
		writeJSON(w, page(q, out))
		return
	}
	n, err := strconv.Atoi(strings.TrimPrefix(rest, "/"))
	if p := b.prs[n]; err == nil && p != nil && strings.HasPrefix(rest, "/") {
		writeJSON(w, wirePR(p))
		return
	}
	reject("no such pull request")
}

// graphql serves the statusCheckRollup query for one PR, popping its next
// scripted rollup state.
func (b *restBackend) graphql(w http.ResponseWriter, r *http.Request, reject func(string)) {
	if r.Method != http.MethodPost {
		reject("graphql is POST only")
		return
	}
	var req struct {
		Query     string `json:"query"`
		Variables struct {
			Owner  string `json:"owner"`
			Repo   string `json:"repo"`
			Number int    `json:"number"`
		} `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		reject("undecodable graphql body: " + err.Error())
		return
	}
	if req.Variables.Owner+"/"+req.Variables.Repo != connectorRepo || !strings.Contains(req.Query, "statusCheckRollup") {
		reject("graphql query for another repository or not a rollup read")
		return
	}
	p := b.prs[req.Variables.Number]
	if p == nil {
		reject("no such pull request")
		return
	}
	var rollup any
	if len(p.rollups) > 0 || len(p.contexts) > 0 {
		state := ""
		if len(p.rollups) > 0 {
			state, p.rollups = p.rollups[0], p.rollups[1:]
		}
		nodes := p.contexts
		if nodes == nil {
			nodes = []scriptedContext{}
		}
		rollup = map[string]any{"state": state, "contexts": map[string]any{"nodes": nodes}}
	}
	writeJSON(w, map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
		"commits": map[string]any{"nodes": []any{map[string]any{"commit": map[string]any{"statusCheckRollup": rollup}}}},
	}}}})
}

func (b *restBackend) find(num string) *restIssue {
	n, err := strconv.Atoi(num)
	if err != nil {
		return nil
	}
	return b.issues[n]
}

func (b *restBackend) list(w http.ResponseWriter, q url.Values) {
	var nums []int
	for n := range b.issues {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	var out []any
	for _, n := range nums {
		i := b.issues[n]
		if want := q.Get("labels"); want != "" && !hasLabel(i.labels, want) {
			continue
		}
		out = append(out, wireIssue(i))
	}
	writeJSON(w, page(q, out))
}

func hasLabel(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}

// page applies per_page and page the way GitHub does.
func page(q url.Values, all []any) []any {
	per, err := strconv.Atoi(q.Get("per_page"))
	if err != nil || per < 1 {
		per = 30
	}
	pg, err := strconv.Atoi(q.Get("page"))
	if err != nil || pg < 1 {
		pg = 1
	}
	lo := min((pg-1)*per, len(all))
	hi := min(lo+per, len(all))
	return append([]any{}, all[lo:hi]...)
}

func wireIssue(i *restIssue) map[string]any {
	labels := []map[string]string{}
	for _, l := range i.labels {
		labels = append(labels, map[string]string{"name": l})
	}
	m := map[string]any{
		"number": i.num, "title": i.title, "body": i.body, "state": "open", "labels": labels,
	}
	if i.pull {
		m["pull_request"] = map[string]any{"url": "https://example.invalid/pull"}
	}
	return m
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

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

// failingPRWrites is the native adapter's role as the PRForge in a read-only
// contract run: the reads must never reach it. The methods the Connector has no
// primitive for (Mergeable, HeadCommitSHA, NeedsUpdate, ListPRFiles,
// CanAutoMerge) are not part of the read contract either.
type failingPRWrites struct{ t *testing.T }

func (w failingPRWrites) fail(name string) error {
	w.t.Errorf("read contract called the native adapter's %s", name)
	return nil
}

func (w failingPRWrites) OpenPRForBranch(string) (forge.PR, bool, error) {
	return forge.PR{}, false, w.fail("OpenPRForBranch")
}
func (w failingPRWrites) PRForBranch(string) (string, bool, error) {
	return "", false, w.fail("PRForBranch")
}
func (w failingPRWrites) PRState(string) (forge.PRState, error) { return "", w.fail("PRState") }
func (w failingPRWrites) Mergeable(string) (forge.MergeableState, error) {
	return "", w.fail("Mergeable")
}
func (w failingPRWrites) CheckState(string) (forge.RollupState, error) {
	return "", w.fail("CheckState")
}
func (w failingPRWrites) HeadCommitSHA(string) (string, error) { return "", w.fail("HeadCommitSHA") }
func (w failingPRWrites) NeedsUpdate(string) (bool, error)     { return false, w.fail("NeedsUpdate") }
func (w failingPRWrites) FailureDetail(string) (string, error) {
	return "", w.fail("FailureDetail")
}
func (w failingPRWrites) ListPRFiles(string) ([]string, error) { return nil, w.fail("ListPRFiles") }
func (w failingPRWrites) CanAutoMerge() (bool, error)          { return false, w.fail("CanAutoMerge") }
func (w failingPRWrites) EnqueueAutoMerge(string) error        { return w.fail("EnqueueAutoMerge") }
func (w failingPRWrites) MarkReady(string) error               { return w.fail("MarkReady") }
func (w failingPRWrites) CommentPR(string, string) error       { return w.fail("CommentPR") }
func (w failingPRWrites) MarkDraft(string) error               { return w.fail("MarkDraft") }

// mergeStandIn is the CodeForge of a read-only PRForge run: Merge is a native
// write the Connector cannot perform, so it marks the scripted PR merged
// instead. The rest of CodeForge is unreachable from the read cases.
type mergeStandIn struct {
	forge.CodeForge
	h *connectorHarness
}

func (mergeStandIn) AgentBranch(num string) string { return "agent/issue-" + num }

func (m mergeStandIn) Merge(ref string) error {
	m.h.mutatePR(prNum(ref), func(p *restPR) { p.merged = true })
	return nil
}

// connectorHarness drives the compiled github Connector, through the
// Connector host, against the scripted REST backend.
type connectorHarness struct {
	backend *restBackend
	routes  []connectorhost.Route
	audit   *bytes.Buffer
	tr      forge.IssueTracker
	pf      forge.PRForge
}

func newConnectorHarness(t *testing.T) *connectorHarness {
	t.Helper()
	path := os.Getenv(connectorWasmEnv)
	if path == "" {
		t.Skipf("%s unset: no compiled github Connector to test", connectorWasmEnv)
	}
	wasm, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var table []struct{ Method, Host, Path string }
	if err := json.Unmarshal([]byte(os.Getenv(connectorRoutesEnv)), &table); err != nil {
		t.Fatalf("%s: %v", connectorRoutesEnv, err)
	}

	b := newRESTBackend(t)
	u, err := url.Parse(b.URL)
	if err != nil {
		t.Fatal(err)
	}
	// The table names the real API host; the scripted backend stands in for it.
	var declared []connectorhost.Route
	for _, r := range table {
		if r.Host != "api.github.com" {
			t.Fatalf("route %s %s names host %q, want api.github.com", r.Method, r.Path, r.Host)
		}
		declared = append(declared, connectorhost.Route{Method: r.Method, Host: u.Host, Path: r.Path})
	}
	h := &connectorHarness{
		backend: b,
		routes:  connectortracker.Routes(declared, connectorRepo),
		audit:   &bytes.Buffer{},
	}
	conn, err := connectorhost.LoadNetwork(context.Background(), wasm, connectorhost.Network{
		Name:   "github",
		Routes: h.routes,
		Authorize: func(r *http.Request) error {
			r.Header.Set("Authorization", "Bearer "+connectorToken)
			return nil
		},
		Client: b.Client(),
		Audit:  connectorhost.NewAuditLog(h.audit),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	h.tr, err = connectortracker.New(conn, failingWrites{t}, testLabels, connectortracker.Target{API: b.URL, Repo: connectorRepo})
	if err != nil {
		t.Fatal(err)
	}
	h.pf, err = connectorforge.New(conn, failingPRWrites{t}, connectorforge.Target{API: b.URL, Repo: connectorRepo}, connectorWebHost)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *connectorHarness) Tracker() forge.IssueTracker { return h.tr }

func (h *connectorHarness) Forge() forge.PRForge       { return h.pf }
func (h *connectorHarness) CodeForge() forge.CodeForge { return mergeStandIn{h: h} }

func (h *connectorHarness) mutatePR(num string, f func(*restPR)) {
	n, _ := strconv.Atoi(num)
	h.backend.mu.Lock()
	defer h.backend.mu.Unlock()
	p := h.backend.prs[n]
	if p == nil {
		p = &restPR{num: n, branch: "agent/issue-" + num}
		h.backend.prs[n] = p
	}
	f(p)
}

func (h *connectorHarness) SeedOpenPR(num string) string {
	h.mutatePR(num, func(*restPR) {})
	return prHTMLURL(atoi(num))
}

func (h *connectorHarness) SeedDraftPR(num string) string {
	h.mutatePR(num, func(p *restPR) { p.draft = true })
	return prHTMLURL(atoi(num))
}

func (h *connectorHarness) IsDraft(url string) bool {
	h.backend.mu.Lock()
	defer h.backend.mu.Unlock()
	return h.backend.prs[atoi(prNum(url))].draft
}

func (h *connectorHarness) SeedCheckStates(url string, states []forge.RollupState) {
	h.mutatePR(prNum(url), func(p *restPR) {
		for _, s := range states {
			p.rollups = append(p.rollups, string(s))
		}
	})
}

func (h *connectorHarness) SeedFailingCheck(url, name, conclusion, summary string) {
	h.mutatePR(prNum(url), func(p *restPR) {
		p.contexts = append(p.contexts, scriptedContext{TypeName: "CheckRun", Name: name, Conclusion: conclusion, Summary: summary})
	})
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func (h *connectorHarness) mutate(num string, f func(*restIssue)) {
	n, _ := strconv.Atoi(num)
	h.backend.mu.Lock()
	defer h.backend.mu.Unlock()
	i := h.backend.issues[n]
	if i == nil {
		i = &restIssue{num: n}
		h.backend.issues[n] = i
	}
	f(i)
}

func (h *connectorHarness) SeedIssue(iss forge.Issue) {
	h.mutate(iss.Number, func(i *restIssue) {
		i.title, i.body, i.labels = iss.Title, iss.Body, append([]string(nil), iss.Labels...)
	})
}

func (h *connectorHarness) SeedState(num string, state forge.DispatchState) {
	h.mutate(num, func(i *restIssue) { i.labels = append(i.labels, testLabels.Label(state)) })
}

func (h *connectorHarness) SeedNativeDeps(num string, ids []string) {
	h.mutate(num, func(i *restIssue) { i.blockedBy = ids })
}

func (h *connectorHarness) FailNativeDeps(num string) {
	h.mutate(num, func(i *restIssue) { i.failNative = true })
}

func (h *connectorHarness) IsolatesNativeFailure() {}

func (h *connectorHarness) IsPriorityCapable() {}

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
	_ forgetest.StateSeeder        = (*connectorHarness)(nil)
	_ forgetest.PRForgeReadHarness = (*connectorHarness)(nil)
)

func TestConnector_ReadContracts(t *testing.T) {
	h := newConnectorHarness(t)
	t.Run("Tracker", func(t *testing.T) { forgetest.RunTrackerReadContract(t, h) })
	t.Run("PRForge", func(t *testing.T) { forgetest.RunPRForgeReadContract(t, h) })

	// REST reports no minimized state, so the Connector must not attest
	// comments; they stay on the native adapter.
	if _, ok := forge.As[forge.CommentLister](h.tr); ok {
		t.Error("Connector tracker serves comments: it must not advertise tracker.comments")
	}

	h.backend.mu.Lock()
	unexpected := h.backend.unexpected
	h.backend.mu.Unlock()
	if len(unexpected) > 0 {
		t.Errorf("backend saw requests outside the Connector's reads: %q", unexpected)
	}

	admitted := map[string]int{}
	for _, e := range h.auditEntries(t) {
		if e.Decision != connectorhost.DecisionSent {
			t.Errorf("audit entry %+v: want every request sent", e)
		}
		admitted[e.Route]++
	}
	// A route no request used is broader than the reads need.
	for _, r := range h.routes {
		if admitted[r.String()] == 0 {
			t.Errorf("route %q admitted no request: the table is broader than the Connector's reads", r)
		}
	}
}

// TestConnector_FailureDetailMatchesNative pins the failing sets connectorforge
// copies from ci_rollup.go: one context of every CheckRun conclusion and
// StatusContext state must render exactly as the native adapter renders them.
func TestConnector_FailureDetailMatchesNative(t *testing.T) {
	h := newConnectorHarness(t)
	var native []failureDetailContext
	var nodes []scriptedContext
	for _, c := range []string{"SUCCESS", "NEUTRAL", "SKIPPED", "FAILURE", "TIMED_OUT", "CANCELLED", "ACTION_REQUIRED", "STARTUP_FAILURE", "STALE"} {
		native = append(native, failureDetailContext{TypeName: "CheckRun", Name: "run-" + c, Conclusion: c, Summary: "summary " + c})
		nodes = append(nodes, scriptedContext{TypeName: "CheckRun", Name: "run-" + c, Conclusion: c, Summary: "summary " + c})
	}
	for _, s := range []string{"SUCCESS", "PENDING", "EXPECTED", "FAILURE", "ERROR"} {
		native = append(native, failureDetailContext{TypeName: "StatusContext", Context: "status-" + s, State: s, Description: "description " + s})
		nodes = append(nodes, scriptedContext{TypeName: "StatusContext", Context: "status-" + s, State: s, Description: "description " + s})
	}
	url := h.SeedOpenPR("301")
	h.mutatePR("301", func(p *restPR) { p.contexts = nodes })

	got, err := h.Forge().FailureDetail(url)
	if err != nil {
		t.Fatal(err)
	}
	want := renderFailureDetail(native)
	if want == "" {
		t.Fatal("native rendered no failure detail for the failing contexts")
	}
	if got != want {
		t.Errorf("Connector FailureDetail differs from native\n got: %q\nwant: %q", got, want)
	}
}

func TestConnector_ListIssuesOmitsPullRequests(t *testing.T) {
	h := newConnectorHarness(t)
	h.SeedIssue(forge.Issue{Number: "11", Title: "issue"})
	h.SeedState("11", forge.Dispatchable)
	h.SeedIssue(forge.Issue{Number: "12", Title: "pull request"})
	h.SeedState("12", forge.Dispatchable)
	h.mutate("12", func(i *restIssue) { i.pull = true })

	got, err := h.tr.ListIssues(forge.Dispatchable)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Number != "11" {
		t.Errorf("ListIssues = %+v, want only issue 11", got)
	}
}
