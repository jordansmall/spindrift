package settle

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgejo"
	"spindrift.dev/launcher/internal/outcome"
)

// fakeForgejoIssueServer is a minimal stateful stand-in for a Forgejo
// instance's issue REST endpoints, tracking one issue's label set and
// open/closed state across the GET/PUT (labels)/PATCH (state)/POST
// (comments) calls a real merge-and-close settle run drives — unlike
// forgejo_test.go's per-call fixed-response handlers, this needs to reflect
// TransitionState's label swap back to verifyMerged's own GET before
// CloseMergedIssue's GET+PATCH, and record whether the issue actually ended
// up closed.
type fakeForgejoIssueServer struct {
	mu           sync.Mutex
	state        string
	labels       []string
	closeCalls   int
	commentCalls []string
}

func newFakeForgejoIssueServer(initialLabels []string) *fakeForgejoIssueServer {
	return &fakeForgejoIssueServer{state: "open", labels: append([]string(nil), initialLabels...)}
}

func (f *fakeForgejoIssueServer) issueJSON(num int) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	type label struct {
		Name string `json:"name"`
	}
	labels := make([]label, len(f.labels))
	for i, l := range f.labels {
		labels[i] = label{Name: l}
	}
	body, _ := json.Marshal(struct {
		Number int     `json:"number"`
		Title  string  `json:"title"`
		Body   string  `json:"body"`
		State  string  `json:"state"`
		Labels []label `json:"labels"`
	}{Number: num, Title: "t", Body: "", State: f.state, Labels: labels})
	return body
}

func (f *fakeForgejoIssueServer) handler(t *testing.T, repoPath string, num int) http.HandlerFunc {
	issuePath := repoPath + "/issues/55"
	labelsPath := issuePath + "/labels"
	commentsPath := issuePath + "/comments"
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == issuePath:
			w.Write(f.issueJSON(num))
		case r.Method == http.MethodPut && r.URL.Path == labelsPath:
			var payload struct {
				Labels []string `json:"labels"`
			}
			json.NewDecoder(r.Body).Decode(&payload)
			f.mu.Lock()
			f.labels = payload.Labels
			f.mu.Unlock()
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPatch && r.URL.Path == issuePath:
			var payload map[string]string
			json.NewDecoder(r.Body).Decode(&payload)
			f.mu.Lock()
			if payload["state"] == "closed" {
				f.state = "closed"
			}
			f.closeCalls++
			f.mu.Unlock()
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && r.URL.Path == commentsPath:
			var payload struct {
				Body string `json:"body"`
			}
			json.NewDecoder(r.Body).Decode(&payload)
			f.mu.Lock()
			f.commentCalls = append(f.commentCalls, payload.Body)
			f.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}
}

// TestSettle_ImmediateMergeClosesForgejoIssue is the forgejo-backed variant
// of TestSettle_ImmediateMergeClosesIssue (issue #2259): it wires a real
// forgejoClient (forge.NewFake there is a shared fake, not forgejo-specific)
// pointed at an httptest server as Settle's IssueTracker, so the confirmed
// merge case drives forgejoClient.CloseMergedIssue over a real HTTP
// PATCH — the end-to-end path acceptance criterion #2 asks for, on top of
// the forgejo package's own CloseMergedIssue unit tests (already landed).
// The Code Forge stays forge.Fake (per ADR 0013, forgejo implements only the
// IssueTracker seam — code still lands via github/git in production).
func TestSettle_ImmediateMergeClosesForgejoIssue(t *testing.T) {
	const issNum = "55"
	const repoPath = "/api/v1/repos/owner/repo"
	const prURL = "https://github.com/owner/repo/pull/55"

	srv := newFakeForgejoIssueServer([]string{"agent-in-progress"})
	ts := httptest.NewServer(srv.handler(t, repoPath, 55))
	defer ts.Close()

	it := forgejo.NewForgejoClient(forgejo.ForgejoConfig{
		BaseURL: ts.URL,
		Repo:    "owner/repo",
		Token:   "tok",
		Labels:  testDispatchLabels,
	})

	cf := forge.NewFake(testDispatchLabels)
	cf.SetCheckStates(prURL, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})

	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: prURL, Status: "ready", Note: "ok"},
		},
	}

	s := newTestSettle(baseConfig(), it, cf)
	s.Settle(dispatch.NewFake(), issNum, 0, result)

	srv.mu.Lock()
	closeCalls, finalState, comments := srv.closeCalls, srv.state, srv.commentCalls
	srv.mu.Unlock()

	if finalState != "closed" {
		t.Errorf("forgejo issue state = %q, want %q (settle's post-merge backstop must close it end-to-end)", finalState, "closed")
	}
	if closeCalls != 1 {
		t.Errorf("PATCH close calls = %d, want 1", closeCalls)
	}
	if len(comments) != 1 {
		t.Errorf("usage comment posts = %d, want 1", len(comments))
	}
}
