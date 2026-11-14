package github

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

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
	gitSecret          = "git-secret"
)

// restIssue is one issue in the scripted GitHub REST backend.
type restIssue struct {
	num        int
	title      string
	body       string
	labels     []string
	blockedBy  []string
	failNative bool
	// unlabeled names the labels removed from the issue, oldest first.
	unlabeled []string
	pull      bool
	comments  []string
}

// scriptedContext is one node of a statusCheckRollup contexts union, in the
// GraphQL wire shape.
type scriptedContext struct {
	TypeName    string `json:"__typename"`
	Name        string `json:"name,omitempty"`
	Conclusion  string `json:"conclusion,omitempty"`
	Summary     string `json:"summary,omitempty"`
	DetailsURL  string `json:"detailsUrl,omitempty"`
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
	base     string
	title    string
	body     string
	draft    bool
	merged   bool
	closed   bool
	enqueued bool
	mergeSHA string
	// behind is how many commits the base has that the head lacks.
	behind   int
	files    []string
	comments []string
	rollups  []string
	contexts []scriptedContext
}

// restBackend serves exactly the REST and GraphQL requests the github Connector
// makes and records any other request, so one outside the Route table's
// subtree fails the test instead of passing silently.
// jobLogs is the Actions job logs by job id: a read answers 302 to a blob path
// on the same server, as GitHub redirects to pre-signed storage.
// The server's own host stands in for github.com: the repository's URL and
// every PR's html_url name it, and git is pointed at repo, the real bare
// repository behind branches and merges.
type restBackend struct {
	*httptest.Server
	mu         sync.Mutex
	issues     map[int]*restIssue
	prs        map[int]*restPR
	jobLogs    map[string]string
	unexpected []string
	// listStatus, when set, is the status every issues-list read answers with.
	listStatus int
	// host and web are the backend's host and the repository's web URL.
	host string
	web  string
	repo *forgetest.GitRepoFixture
	// autoMerge is the repository's auto-merge setting.
	autoMerge  bool
	protection map[string]branchRules
	// merges counts merge requests; mergeStatuses scripts the status the next
	// merge requests answer, in order, before the real answer resumes.
	merges        int
	mergeStatuses []int
	// labels is the repository's label registry; hits counts every request the
	// server received, accepted or not; tokens are extra Authorization values
	// it accepts beside connectorToken's, and auths the Authorization of each
	// request it accepted, in order.
	labels []string
	hits   int
	tokens map[string]bool
	auths  []string
}

// newRESTBackend starts the backend and a real bare repository. Production git
// is https-only, so git reaches the bare repo through the environment: no
// config file, and Credential.Apply extends the count.
func newRESTBackend(t *testing.T) *restBackend {
	t.Helper()
	for k, v := range map[string]string{
		"GIT_AUTHOR_NAME": "Test Bot", "GIT_AUTHOR_EMAIL": "bot@example.com",
		"GIT_COMMITTER_NAME": "Test Bot", "GIT_COMMITTER_EMAIL": "bot@example.com",
	} {
		t.Setenv(k, v)
	}
	b := &restBackend{
		issues: map[int]*restIssue{}, prs: map[int]*restPR{}, jobLogs: map[string]string{},
		protection: map[string]branchRules{}, repo: forgetest.NewGitRepoFixture(t, "main"),
	}
	b.Server = httptest.NewTLSServer(http.HandlerFunc(b.serve))
	t.Cleanup(b.Close)
	b.host = b.Listener.Addr().String()
	b.web = "https://" + b.host + "/" + connectorRepo
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url."+b.repo.Bare+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", b.web+".git")
	return b
}

func (b *restBackend) serve(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.hits++
	reject := func(why string) {
		b.unexpected = append(b.unexpected, r.Method+" "+r.URL.RequestURI()+": "+why)
		http.NotFound(w, r)
	}
	if id, ok := strings.CutPrefix(r.URL.Path, "/blob/"); ok {
		// The redirect hop is uncredentialed, as pre-signed storage URLs are.
		log, found := b.jobLogs[id]
		if r.Header.Get("Authorization") != "" || len(r.Header.Values("Accept")) != 0 || !found {
			reject("blob read with a credential or guest header, or for no such log")
			return
		}
		_, _ = w.Write([]byte(log))
		return
	}
	auth := r.Header.Get("Authorization")
	if auth != "Bearer "+connectorToken && !b.tokens[auth] {
		reject("missing host credential")
		return
	}
	b.auths = append(b.auths, auth)
	if r.URL.Path == "/installation/repositories" {
		// Token introspection of an installation token: it may push.
		writeJSON(w, map[string]any{"repositories": []map[string]any{
			{"full_name": connectorRepo, "permissions": map[string]any{"push": true}},
		}})
		return
	}
	if r.URL.Path == "/graphql" {
		b.graphql(w, r, reject)
		return
	}
	if b.serveWrite(w, r, reject) {
		return
	}
	if rest, ok := strings.CutPrefix(r.URL.Path, "/repos/"+connectorRepo+"/actions/jobs/"); ok {
		id, ok := strings.CutSuffix(rest, "/logs")
		if _, found := b.jobLogs[id]; r.Method != http.MethodGet || !ok || !found {
			reject("not a read of a scripted job log")
			return
		}
		http.Redirect(w, r, b.URL+"/blob/"+id, http.StatusFound)
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
	case rest == "" && b.listStatus != 0:
		w.WriteHeader(b.listStatus)
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
	case len(parts) == 2 && parts[1] == "timeline":
		i := b.find(parts[0])
		if i == nil {
			reject("no such issue")
			return
		}
		// A labeled event first, so the read must pick out the unlabeled ones.
		events := []map[string]any{{"event": "labeled", "label": map[string]any{"name": "bug"}}}
		for _, name := range i.unlabeled {
			events = append(events, map[string]any{"event": "unlabeled", "label": map[string]any{"name": name}})
		}
		writeJSON(w, events)
	default:
		reject("unknown endpoint")
	}
}

func (b *restBackend) wirePR(p *restPR) map[string]any {
	state, mergedAt := "open", any(nil)
	if p.merged || p.closed {
		state = "closed"
	}
	if p.merged {
		mergedAt = "2026-01-01T00:00:00Z"
	}
	var mergeable any
	if !p.merged && !p.closed {
		_, mergeable = b.rebaseTree(p)
	}
	// An open PR carries GitHub's test-merge commit, which is not a landing.
	mergeCommit := fakeSHA(p.num)
	if p.merged {
		mergeCommit = p.mergeSHA
	}
	return map[string]any{
		"number": p.num, "html_url": b.prURL(p.num), "state": state, "merged_at": mergedAt, "draft": p.draft,
		"node_id": nodeID(p.num), "mergeable": mergeable, "merge_commit_sha": mergeCommit,
		"head": map[string]string{"ref": p.branch, "sha": b.headSHA(p)},
		"base": map[string]string{"ref": p.base},
	}
}

// pulls serves GET .../pulls?head=owner:branch (exact head match, newest
// first), POST .../pulls, and one PR's GET, files and PUT merge.
func (b *restBackend) pulls(w http.ResponseWriter, r *http.Request, rest string, reject func(string)) {
	q := r.URL.Query()
	parts := strings.Split(strings.TrimPrefix(rest, "/"), "/")
	p := b.prByNumber(parts[0])
	switch {
	case rest == "" && r.Method == http.MethodPost:
		b.openPR(w, r, reject)
	case rest == "" && r.Method == http.MethodGet:
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
			out = append(out, b.wirePR(b.prs[n]))
		}
		writeJSON(w, page(q, out))
	case p == nil || !strings.HasPrefix(rest, "/"):
		reject("no such pull request")
	case len(parts) == 1 && r.Method == http.MethodGet:
		writeJSON(w, b.wirePR(p))
	case len(parts) == 2 && parts[1] == "files" && r.Method == http.MethodGet:
		out := []any{}
		for _, f := range p.files {
			out = append(out, map[string]string{"filename": f})
		}
		writeJSON(w, page(q, out))
	case len(parts) == 2 && parts[1] == "merge" && r.Method == http.MethodPut:
		b.mergePR(w, r, p, reject)
	default:
		reject("not a request the Connector makes of a pull request")
	}
}

// graphql serves the repository-identity query, the pull-request mutations, and
// the statusCheckRollup query for one PR, popping its next scripted rollup
// state.
func (b *restBackend) graphql(w http.ResponseWriter, r *http.Request, reject func(string)) {
	if r.Method != http.MethodPost {
		reject("graphql is POST only")
		return
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		reject("unreadable graphql body: " + err.Error())
		return
	}
	var req struct {
		Query     string `json:"query"`
		Variables struct {
			Owner  string `json:"owner"`
			Repo   string `json:"repo"`
			Number int    `json:"number"`
			ID     string `json:"id"`
		} `json:"variables"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		reject("undecodable graphql body: " + err.Error())
		return
	}
	switch {
	case strings.Contains(req.Query, "removeLabelsFromLabelable"):
		b.removeLabels(w, raw, reject)
		return
	case strings.Contains(req.Query, "label(name:"):
		b.labelIDs(w, raw, req.Variables.Owner+"/"+req.Variables.Repo, req.Variables.Number, reject)
		return
	case strings.Contains(req.Query, "viewer{"):
		// Token introspection of a classic token: its grant rides the header.
		w.Header().Set("X-OAuth-Scopes", "read:org, repo")
		writeJSON(w, map[string]any{"data": map[string]any{"viewer": map[string]any{"login": "bot"}}})
		return
	case strings.Contains(req.Query, "autoMergeAllowed"):
		b.repoQuery(w, req.Variables.Owner, req.Variables.Repo)
		return
	case strings.HasPrefix(req.Query, "mutation"):
		b.mutation(w, req.Query, req.Variables.ID, reject)
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
		if !strings.Contains(req.Query, "detailsUrl") {
			// GitHub returns only the fields the query selects.
			nodes = append([]scriptedContext(nil), nodes...)
			for i := range nodes {
				nodes[i].DetailsURL = ""
			}
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

// connectorHarness drives the compiled github Connector, through the
// Connector host, against the scripted REST backend.
// pf is the read shadow's PRForge (connectorforge.New over native writes the
// reads must never reach); cf and caps are the Code Forge composed from the
// Connector alone, whose PRForge is caps.PRForge.
type connectorHarness struct {
	t       *testing.T
	backend *restBackend
	routes  []connectorhost.Route
	audit   *bytes.Buffer
	conn    *connectorhost.Connector
	tr      forge.IssueTracker
	pf      forge.PRForge
	cf      forge.CodeForge
	caps    forge.Capabilities
}

func newConnectorHarness(t *testing.T) *connectorHarness { return newComposedHarness(t, false) }

// loadGithubConnector loads the compiled github Connector with its declared
// Route table bound to host and to repo. The table names the real API host; the
// scripted backend stands in for it.
func loadGithubConnector(t *testing.T, host, repo string, client *http.Client, audit io.Writer) (*connectorhost.Connector, []connectorhost.Route) {
	t.Helper()
	return loadGithubConnectorAs(t, host, repo, client, audit, stampConnectorToken)
}

// stampConnectorToken is the static credential the Connector's requests carry
// unless a test supplies its own.
func stampConnectorToken(r *http.Request) error {
	r.Header.Set("Authorization", "Bearer "+connectorToken)
	return nil
}

// githubConnectorRoutes returns the compiled github Connector's path and its
// declared Route table bound to host and to repo, skipping the test when no
// Connector was built.
func githubConnectorRoutes(t *testing.T, host, repo string) (string, []connectorhost.Route) {
	t.Helper()
	path := os.Getenv(connectorWasmEnv)
	if path == "" {
		t.Skipf("%s unset: no compiled github Connector to test; connectors/github's connector-github-conformance check runs it", connectorWasmEnv)
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
		if r.Host != "api.github.com" {
			t.Fatalf("route %s %s names host %q, want api.github.com", r.Method, r.Path, r.Host)
		}
		declared = append(declared, connectorhost.Route{Method: r.Method, Host: host, Path: r.Path, FollowRedirect: r.FollowRedirect})
	}
	return path, connectortracker.Routes(declared, repo)
}

// loadGithubConnectorAs is loadGithubConnector with the credential authorize
// stamps on each request.
func loadGithubConnectorAs(t *testing.T, host, repo string, client *http.Client, audit io.Writer, authorize func(*http.Request) error) (*connectorhost.Connector, []connectorhost.Route) {
	t.Helper()
	path, routes := githubConnectorRoutes(t, host, repo)
	wasm, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := connectorhost.LoadNetwork(context.Background(), wasm, connectorhost.Network{
		Name:      "github",
		Routes:    routes,
		Authorize: authorize,
		Client:    client,
		Audit:     connectorhost.NewAuditLog(audit),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	// The guest speaks the 0.3 forge write surface; its version
	// comes from the ldflag in connectors/github/package.nix, not the SDK default.
	if !strings.HasPrefix(conn.ProtocolVersion, "0.3.") {
		t.Fatalf("handshake protocol version = %q, want 0.3.x (the protocolVersion ldflag in connectors/github/package.nix)", conn.ProtocolVersion)
	}
	return conn, routes
}

// composeForge composes the Code Forge over conn for the repository target
// names.
func composeForge(conn *connectorhost.Connector, api, targetRepo string, readOnlyBox bool) (forge.CodeForge, forge.Capabilities, error) {
	return connectorforge.Compose(conn, connectorforge.Target{API: api, Repo: targetRepo}, connectorforge.Git{
		Credential:  gitexec.Credential{Username: "bot", Password: gitSecret},
		ReadOnlyBox: readOnlyBox,
		Plumbing: func(u string, cred gitexec.Credential) forge.CodeForge {
			return git.NewGitClient(u, "main", "Test Bot", "bot@example.com", git.WithCredential(cred))
		},
		Clock: retry.Clock{Now: time.Now, Sleep: func(time.Duration) {}},
	})
}

// newComposedHarness loads the Connector and composes the Code Forge over it.
// readOnlyBox is the Box's mode: a read-only Box relays its bundle.
func newComposedHarness(t *testing.T, readOnlyBox bool) *connectorHarness {
	return newHarnessFor(t, readOnlyBox, connectorRepo)
}

// newHarnessFor is newComposedHarness with the composed forge's Target naming
// targetRepo while the Route table stays pinned to connectorRepo: the hostile
// fixture for a Connector that reaches outside its declared repository.
func newHarnessFor(t *testing.T, readOnlyBox bool, targetRepo string) *connectorHarness {
	return newHarnessWith(t, readOnlyBox, targetRepo, connectortracker.Lifecycle{Labels: testLabels}, nil)
}

// newHarnessWith is newHarnessFor with the tracker's lifecycle and the
// credential the Connector's requests carry (nil means stampConnectorToken).
func newHarnessWith(t *testing.T, readOnlyBox bool, targetRepo string, lc connectortracker.Lifecycle, authorize func(*http.Request) error) *connectorHarness {
	t.Helper()
	if authorize == nil {
		authorize = stampConnectorToken
	}
	b := newRESTBackend(t)
	h := &connectorHarness{t: t, backend: b, audit: &bytes.Buffer{}}
	conn, routes := loadGithubConnectorAs(t, b.host, connectorRepo, b.Client(), h.audit, authorize)
	h.conn, h.routes = conn, routes
	var err error
	h.tr, err = connectortracker.New(conn, failingWrites{t}, lc, connectortracker.Target{API: b.URL, Repo: connectorRepo})
	if err != nil {
		t.Fatal(err)
	}
	h.pf, err = connectorforge.New(conn, failingPRWrites{t}, connectorforge.Target{API: b.URL, Repo: connectorRepo}, b.host)
	if err != nil {
		t.Fatal(err)
	}
	h.cf, h.caps, err = composeForge(conn, b.URL, targetRepo, readOnlyBox)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *connectorHarness) Tracker() forge.IssueTracker { return h.tr }

func (h *connectorHarness) Forge() forge.PRForge       { return h.pf }
func (h *connectorHarness) CodeForge() forge.CodeForge { return h.cf }

func (h *connectorHarness) BranchPrefix() string { return "agent/issue-" }

func (h *connectorHarness) mutatePR(num string, f func(*restPR)) {
	n, _ := strconv.Atoi(num)
	h.backend.mu.Lock()
	defer h.backend.mu.Unlock()
	p := h.backend.prs[n]
	if p == nil {
		p = &restPR{num: n, branch: "agent/issue-" + num, base: "main"}
		h.backend.prs[n] = p
	}
	f(p)
}

func (h *connectorHarness) SeedOpenPR(num string) string {
	h.mutatePR(num, func(*restPR) {})
	return h.backend.prURL(atoi(num))
}

func (h *connectorHarness) SeedDraftPR(num string) string {
	h.mutatePR(num, func(p *restPR) { p.draft = true })
	return h.backend.prURL(atoi(num))
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

func (h *connectorHarness) SeedFailingCheckLog(url, check, log string) {
	h.SeedJobLogFailure(url, check, "7001", log)
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

// TestConnector_Contracts runs the shared contracts against the compiled
// Connector: the tracker and PR reads (through the read shadow's PRForge), and
// the whole Code Forge composed from the Connector. A route no request in either
// run used is broader than the Connector needs, so coverage is judged over both.
func TestConnector_Contracts(t *testing.T) {
	var cov routeCoverage
	t.Run("Reads", func(t *testing.T) {
		h := newConnectorHarness(t)
		t.Run("Tracker", func(t *testing.T) { forgetest.RunTrackerReadContract(t, h) })
		t.Run("PRForge", func(t *testing.T) { forgetest.RunPRForgeReadContract(t, h) })
		t.Run("JobLogExcerpt", func(t *testing.T) { testJobLogExcerpt(t, h) })

		// REST reports no minimized state, so the Connector must not attest
		// comments; they stay on the native adapter.
		if _, ok := forge.As[forge.CommentLister](h.tr); ok {
			t.Error("Connector tracker serves comments: it must not advertise tracker.comments")
		}
		cov.record(t, h)
	})
	t.Run("ComposedForge", func(t *testing.T) {
		h := newComposedHarness(t, false)
		t.Run("Capabilities", func(t *testing.T) { testComposedCapabilities(t, h) })
		t.Run("PRForgeContract", func(t *testing.T) { forgetest.RunPRForgeContract(t, composedPRHarness{h}) })
		for _, c := range []struct {
			name string
			run  func(*testing.T, *connectorHarness)
		}{
			{"MergeLandsHead", testMergeLandsHead},
			{"MergeableTracksReality", testMergeableTracksReality},
			{"MergeRefusesDraft", testMergeRefusesDraft},
			{"HeadCommitSHA", testHeadCommitSHA},
			{"CreateDraftPRAdopts", testCreateDraftPRAdopts},
			{"ListPRFiles", testListPRFiles},
			{"CommentPR", testCommentPR},
			{"BranchLifecycle", testBranchLifecycle},
			{"BranchProtection", testBranchProtection},
			{"Probe", testProbe},
			{"TokenGrant", testTokenGrant},
			{"BaseMovedOn", testBaseMovedOn},
			{"MergeTransientAndPermanent", testMergeTransientAndPermanent},
		} {
			t.Run(c.name, func(t *testing.T) { c.run(t, h) })
		}
		cov.record(t, h)
	})
	t.Run("CodeForgeContract", func(t *testing.T) {
		h := newComposedHarness(t, false)
		forgetest.RunCodeForgeContract(t, composedCodeForgeHarness{h})
		cov.record(t, h)
	})
	t.Run("HostMediationContract", func(t *testing.T) {
		h := newComposedHostMediationHarness(t)
		forgetest.RunHostMediationContract(t, h)
		cov.record(t, h.connectorHarness)
	})
	t.Run("TrackerWrites", func(t *testing.T) {
		w := newTrackerWriteHarness(t)
		testTrackerWrites(t, w)
		cov.record(t, w.h)
	})
	t.Run("HostPostedComment", func(t *testing.T) {
		w := newTrackerWriteHarness(t)
		forgetest.RunHostPostedCommentContract(t, w)
		cov.record(t, w.h)
	})
	t.Run("IssueFiler", func(t *testing.T) {
		w := newTrackerWriteHarness(t)
		forgetest.RunIssueFilerContract(t, w)
		cov.record(t, w.h)
	})
	cov.assertUsed(t)
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
	want := renderFailureDetail(native, nil)
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

// SeedJobLogFailure scripts a failing GitHub Actions job on the PR: a CheckRun
// whose detailsUrl names the job, and the job's log.
func (h *connectorHarness) SeedJobLogFailure(url, name, jobID, log string) {
	h.mutatePR(prNum(url), func(p *restPR) {
		p.contexts = append(p.contexts, scriptedContext{
			TypeName: "CheckRun", Name: name, Conclusion: "FAILURE", Summary: "exit status 1",
			DetailsURL: h.backend.web + "/actions/runs/900/job/" + jobID,
		})
	})
	h.backend.mu.Lock()
	defer h.backend.mu.Unlock()
	h.backend.jobLogs[jobID] = log
}

// A failing job's log reaches the fix pass's detail as a capped tail, fetched
// by the Connector through the log Route's one redirect hop.
func testJobLogExcerpt(t *testing.T, h *connectorHarness) {
	url := h.SeedOpenPR("302")
	log := "FIRST-LINE-MARKER\n" + strings.Repeat("padding line of test output\n", 400) + "--- FAIL: TestX\nLAST-LINE-MARKER\n"
	h.SeedJobLogFailure(url, "unit", "12345", log)

	got, err := h.Forge().FailureDetail(url)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"unit: FAILURE\nexit status 1\n", "--- FAIL: TestX\nLAST-LINE-MARKER", `[log excerpt for "unit" trimmed`} {
		if !strings.Contains(got, want) {
			t.Errorf("FailureDetail lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "FIRST-LINE-MARKER") {
		t.Errorf("FailureDetail kept the head of a log over the excerpt cap:\n%s", got)
	}

	var redirected bool
	for _, e := range h.auditEntries(t) {
		if strings.HasSuffix(e.Path, "/actions/jobs/12345/logs") {
			redirected = e.Decision == connectorhost.DecisionSent && e.RedirectHost != ""
		}
	}
	if !redirected {
		t.Errorf("audit lacks a sent log read with a followed redirect:\n%s", h.audit)
	}
}
