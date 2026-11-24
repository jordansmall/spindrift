// Package connectortrackertest is a scripted forgejo-shaped issue backend and a
// loaded tracker Connector behind one forgetest.Harness, so the composed
// Connector tracker is driven through the same seams production uses: the Route
// table and the audit log. It takes a Connector of any name: its wasm, its
// declared routes and any scripted answers its dialect needs.
//
// The backend models issue reads and, by label name, label writes (POST and
// DELETE under /issues/{n}/labels); every other write answers 404. The tracker
// is Connector-only: a core method the Connector does not serve fails with
// connectorhost.ErrProtocolUnsupported.
package connectortrackertest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"spindrift.dev/launcher/internal/connectorforge/connectorforgetest"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectortracker"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
	"spindrift.dev/launcher/internal/retry"
)

const (
	// Repo is the repository the scripted backend serves.
	Repo = connectorforgetest.Repo

	apiPath    = "/api/v1"
	issuesPath = apiPath + "/repos/" + Repo + "/issues"
)

// Labels are the dispatch labels the tracker is built with; they mirror the
// defaults in lib/env-schema.nix.
var Labels = forge.DispatchLabels{
	Dispatchable: "ready-for-agent",
	InProgress:   "agent-in-progress",
	Complete:     "agent-complete",
	Failed:       "agent-failed",
	Ambiguous:    "agent-ambiguous-spec",
}

type issue struct {
	title, body string
	labels      []string
	comments    []forge.Comment
	deps        []int
	failDeps    bool
}

// Backend is the scripted forgejo-shaped issue REST backend.
type Backend struct {
	*httptest.Server
	// Host is the backend's host:port, the one host its Route table declares.
	Host string

	script []connectorforgetest.Scripted

	reqMu sync.Mutex
	reqs  map[string]int

	mu     sync.Mutex
	issues map[int]*issue
}

func newBackend(t *testing.T, script []connectorforgetest.Scripted) *Backend {
	t.Helper()
	b := &Backend{script: script, issues: map[int]*issue{}, reqs: map[string]int{}}
	b.Server = httptest.NewTLSServer(http.HandlerFunc(b.serve))
	t.Cleanup(b.Close)
	b.Host = b.Listener.Addr().String()
	return b
}

func (b *Backend) serve(w http.ResponseWriter, r *http.Request) {
	b.reqMu.Lock()
	b.reqs[r.Method+" "+r.URL.Path]++
	b.reqMu.Unlock()
	if connectorforgetest.ServeScript(w, r, b.script) {
		return
	}
	if !strings.HasPrefix(r.URL.Path, issuesPath) {
		http.NotFound(w, r)
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	rest := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, issuesPath), "/"), "/")
	if rest[0] == "" {
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		b.list(w, r)
		return
	}
	n, err := strconv.Atoi(rest[0])
	i := b.issues[n]
	if err != nil || i == nil {
		http.NotFound(w, r)
		return
	}
	if len(rest) >= 2 && rest[1] == "labels" && b.editLabels(w, r, i, rest[2:]) {
		return
	}
	if r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	switch {
	case len(rest) == 1:
		writeJSON(w, wireIssue(n, i))
	case len(rest) == 2 && rest[1] == "comments":
		out := []map[string]any{}
		for _, c := range i.comments {
			out = append(out, map[string]any{"user": map[string]string{"login": c.Author}, "created_at": c.CreatedAt, "body": c.Body})
		}
		writeJSON(w, out)
	case len(rest) == 2 && rest[1] == "dependencies":
		if i.failDeps {
			http.Error(w, "scripted failure", http.StatusInternalServerError)
			return
		}
		out := []map[string]int{}
		for _, d := range i.deps {
			out = append(out, map[string]int{"number": d})
		}
		writeJSON(w, out)
	default:
		http.NotFound(w, r)
	}
}

// editLabels serves the label writes: POST .../labels with {"labels":[names]}
// adds, DELETE .../labels/{name} removes and answers 404 for a label the issue
// does not carry. name is the decoded path after "labels". It reports whether
// it handled the request.
func (b *Backend) editLabels(w http.ResponseWriter, r *http.Request, i *issue, name []string) bool {
	switch {
	case r.Method == http.MethodPost && len(name) == 0:
		var body struct {
			Labels []string `json:"labels"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return true
		}
		for _, l := range body.Labels {
			if !slices.Contains(i.labels, l) {
				i.labels = append(i.labels, l)
			}
		}
		writeJSON(w, wireLabels(i.labels))
	case r.Method == http.MethodDelete && len(name) > 0:
		l := strings.Join(name, "/")
		if !slices.Contains(i.labels, l) {
			http.NotFound(w, r)
			return true
		}
		i.labels = slices.DeleteFunc(i.labels, func(have string) bool { return have == l })
		w.WriteHeader(http.StatusNoContent)
	default:
		return false
	}
	return true
}

// list answers the open issues carrying every label in the labels filter, in
// number order, up to limit.
func (b *Backend) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var want []string
	if l := q.Get("labels"); l != "" {
		want = strings.Split(l, ",")
	}
	limit := 0
	if l := q.Get("limit"); l != "" {
		var err error
		if limit, err = strconv.Atoi(l); err != nil {
			http.Error(w, "limit: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	var nums []int
	for n, i := range b.issues {
		if q.Get("state") == "open" && !slices.ContainsFunc(want, func(l string) bool { return !slices.Contains(i.labels, l) }) {
			nums = append(nums, n)
		}
	}
	sort.Ints(nums)
	if limit > 0 && len(nums) > limit {
		nums = nums[:limit]
	}
	out := []map[string]any{}
	for _, n := range nums {
		out = append(out, wireIssue(n, b.issues[n]))
	}
	writeJSON(w, out)
}

func wireLabels(names []string) []map[string]string {
	labels := []map[string]string{}
	for _, l := range names {
		labels = append(labels, map[string]string{"name": l})
	}
	return labels
}

func wireIssue(n int, i *issue) map[string]any {
	return map[string]any{"number": n, "title": i.title, "body": i.body, "state": "open", "labels": wireLabels(i.labels)}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// Harness implements forgetest.Harness and forgetest.StateSeeder over the
// composed tracker; New adds the optional interfaces the Connector advertises.
type Harness struct {
	t *testing.T
	// Backend is the scripted tracker the Connector talks to.
	Backend *Backend
	// Conn is the loaded Connector.
	Conn *connectorhost.Connector
	// Audit receives the Connector's audit log.
	Audit *bytes.Buffer

	tr forge.IssueTracker
}

// New starts the backend, loads c against it and builds its tracker. The
// returned harness implements forgetest.CommentCapable and
// forgetest.NativeCapable (with forgetest.NativeFailureIsolatable) exactly when
// c advertises tracker.comments and tracker.nativeDeps, and
// forgetest.PriorityCapable always: core resolves priority from the labels.
func New(t *testing.T, c connectorforgetest.Connector) forgetest.Harness {
	t.Helper()
	b := newBackend(t, c.Script)
	h := &Harness{t: t, Backend: b, Audit: &bytes.Buffer{}}
	conn, err := connectorhost.LoadNetwork(context.Background(), c.Wasm, connectorhost.Network{
		Name:   "fixture-tracker",
		Routes: c.BoundRoutes(b.Host),
		Authorize: func(r *http.Request) error {
			r.Header.Set("Authorization", "Bearer "+connectorforgetest.Token)
			return nil
		},
		Client: b.Client(),
		Audit:  connectorhost.NewAuditLog(h.Audit),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	h.Conn = conn
	h.tr, err = connectortracker.New(conn, connectortracker.Lifecycle{Labels: Labels, VerdictLabels: forge.ResearchVerdictLabels(), WebHost: b.Host},
		connectortracker.Target{API: b.URL + apiPath, Repo: Repo}, retry.NoSleepClock())
	if err != nil {
		t.Fatal(err)
	}

	comments := slices.Contains(conn.Capabilities, connectortracker.CapComments)
	native := slices.Contains(conn.Capabilities, connectortracker.CapNativeDeps)
	switch {
	case comments && native:
		return struct {
			*Harness
			commentSeeder
			nativeSeeder
		}{h, commentSeeder{h}, nativeSeeder{h}}
	case comments:
		return struct {
			*Harness
			commentSeeder
		}{h, commentSeeder{h}}
	case native:
		return struct {
			*Harness
			nativeSeeder
		}{h, nativeSeeder{h}}
	}
	return h
}

// Tracker is the composed Connector tracker under test.
func (h *Harness) Tracker() forge.IssueTracker { return h.tr }

// IsPriorityCapable marks the tracker as resolving priority from its labels.
func (h *Harness) IsPriorityCapable() {}

// mutate applies f to issue num, creating it when absent.
func (h *Harness) mutate(num string, f func(*issue)) {
	n, err := strconv.Atoi(num)
	if err != nil {
		h.t.Fatalf("issue number %q: %v", num, err)
	}
	h.Backend.mu.Lock()
	defer h.Backend.mu.Unlock()
	i := h.Backend.issues[n]
	if i == nil {
		i = &issue{}
		h.Backend.issues[n] = i
	}
	f(i)
}

// SeedIssue scripts iss as an open issue in the backend.
func (h *Harness) SeedIssue(iss forge.Issue) {
	h.mutate(iss.Number, func(i *issue) {
		i.title, i.body, i.labels = iss.Title, iss.Body, slices.Clone(iss.Labels)
	})
}

// SeedState adds the dispatch label for state to the seeded issue num.
func (h *Harness) SeedState(num string, state forge.DispatchState) {
	h.mutate(num, func(i *issue) { i.labels = append(i.labels, Labels.Label(state)) })
}

// FailNativeDeps makes the backend answer 500 to num's dependencies.
func (h *Harness) FailNativeDeps(num string) {
	h.mutate(num, func(i *issue) { i.failDeps = true })
}

type commentSeeder struct{ h *Harness }

func (s commentSeeder) SeedComments(num string, comments []forge.Comment) {
	s.h.mutate(num, func(i *issue) { i.comments = slices.Clone(comments) })
}

type nativeSeeder struct{ h *Harness }

func (s nativeSeeder) SeedNativeDeps(num string, ids []string) {
	deps := make([]int, len(ids))
	for k, id := range ids {
		n, err := strconv.Atoi(id)
		if err != nil {
			s.h.t.Fatalf("native dependency id %q: %v", id, err)
		}
		deps[k] = n
	}
	s.h.mutate(num, func(i *issue) { i.deps = deps })
}

func (nativeSeeder) IsolatesNativeFailure() {}

var (
	_ forgetest.Harness         = (*Harness)(nil)
	_ forgetest.StateSeeder     = (*Harness)(nil)
	_ forgetest.PriorityCapable = (*Harness)(nil)
)

// Requests is how many requests the backend has answered for method and path,
// scripted or modelled: a count of what a Connector actually sent.
func (h *Harness) Requests(method, path string) int {
	h.Backend.reqMu.Lock()
	defer h.Backend.reqMu.Unlock()
	return h.Backend.reqs[method+" "+path]
}
