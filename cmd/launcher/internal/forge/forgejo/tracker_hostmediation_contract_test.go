package forgejo_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"sync"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgejo"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

var nativeTrackerCommentRe = regexp.MustCompile(`^/api/v1/repos/owner/repo/issues/([0-9]+)/comments$`)

// nativeTrackerLabels is the repo label registry the fake serves; PostIssue
// resolves names to these IDs before the create. A slice so the served order
// is deterministic.
var nativeTrackerLabels = []struct {
	ID   int64
	Name string
}{{7, "bug"}}

func nativeTrackerLabelName(id int64) (string, bool) {
	for _, l := range nativeTrackerLabels {
		if l.ID == id {
			return l.Name, true
		}
	}
	return "", false
}

type nativeTrackerFiledIssue struct {
	Title  string
	Body   string
	Labels []string
}

// nativeTrackerHarness pins the native forgejo tracker (the production tracker
// for every non-primary forgejo row until #7122) to the host-mediation
// contracts, against an httptest server standing in for Forgejo's REST API.
type nativeTrackerHarness struct {
	t   *testing.T
	srv *httptest.Server
	tr  forge.IssueTracker

	mu sync.Mutex
	// Keys are pre-registered issue numbers; an absent number 404s, the
	// fault case.
	comments map[string][]string
	// filedIssues is keyed by the number nextIssueNum hands out, mirroring
	// Forgejo's auto-incrementing issue numbering.
	filedIssues  map[int]nativeTrackerFiledIssue
	nextIssueNum int
}

var (
	_ forgetest.HostPostedCommentHarness = (*nativeTrackerHarness)(nil)
	_ forgetest.IssueFilerHarness        = (*nativeTrackerHarness)(nil)
)

func newNativeTrackerHarness(t *testing.T) *nativeTrackerHarness {
	t.Helper()
	h := &nativeTrackerHarness{
		t:            t,
		comments:     map[string][]string{"801": nil},
		filedIssues:  map[int]nativeTrackerFiledIssue{},
		nextIssueNum: 1000,
	}
	h.srv = httptest.NewServer(http.HandlerFunc(h.handle))
	t.Cleanup(h.srv.Close)
	h.tr = forgejo.NewForgejoClient(forgejo.ForgejoConfig{
		BaseURL: h.srv.URL,
		Repo:    "owner/repo",
		Token:   "tok",
	})
	return h
}

func (h *nativeTrackerHarness) Tracker() forge.IssueTracker           { return h.tr }
func (h *nativeTrackerHarness) IssueFilerTracker() forge.IssueTracker { return h.tr }

func (h *nativeTrackerHarness) handle(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && nativeTrackerCommentRe.MatchString(r.URL.Path):
		num := nativeTrackerCommentRe.FindStringSubmatch(r.URL.Path)[1]
		h.mu.Lock()
		_, ok := h.comments[num]
		h.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body struct {
			Body string `json:"body"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		h.mu.Lock()
		h.comments[num] = append(h.comments[num], body.Body)
		h.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/repos/owner/repo/issues":
		var body struct {
			Title  string  `json:"title"`
			Body   string  `json:"body"`
			Labels []int64 `json:"labels"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.Title == "fail-issue" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var names []string
		for _, id := range body.Labels {
			name, ok := nativeTrackerLabelName(id)
			if !ok {
				h.t.Errorf("issue create carried unknown label id %d", id)
				w.WriteHeader(http.StatusUnprocessableEntity)
				return
			}
			names = append(names, name)
		}
		h.mu.Lock()
		num := h.nextIssueNum
		h.nextIssueNum++
		h.filedIssues[num] = nativeTrackerFiledIssue{Title: body.Title, Body: body.Body, Labels: names}
		h.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{
			"number":   num,
			"html_url": fmt.Sprintf("https://forge.test/owner/repo/issues/%d", num),
		})
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/owner/repo/labels":
		items := make([]map[string]any, 0, len(nativeTrackerLabels))
		for _, l := range nativeTrackerLabels {
			items = append(items, map[string]any{"id": l.ID, "name": l.Name})
		}
		body, _ := json.Marshal(items)
		serveLabels(w, r, string(body))
	default:
		http.NotFound(w, r)
	}
}

func (h *nativeTrackerHarness) SeedCommentTarget(failing bool) (num string) {
	if failing {
		return "999"
	}
	return "801"
}

func (h *nativeTrackerHarness) CommentPosted(num, body string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Contains(h.comments[num], body)
}

func (h *nativeTrackerHarness) SeedIssueFilerTarget(failing bool) (title, body string, labels []string) {
	if failing {
		return "fail-issue", "body", nil
	}
	return "widget: filed issue", "Filed by the host-mediation contract.", []string{"bug"}
}

func (h *nativeTrackerHarness) IssuePosted(title, body string, labels []string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, iss := range h.filedIssues {
		if iss.Title != title || iss.Body != body {
			continue
		}
		got := append([]string(nil), iss.Labels...)
		want := append([]string(nil), labels...)
		slices.Sort(got)
		slices.Sort(want)
		if slices.Equal(got, want) {
			return true
		}
	}
	return false
}

func TestNativeTracker_HostPostedCommentContract(t *testing.T) {
	forgetest.RunHostPostedCommentContract(t, newNativeTrackerHarness(t))
}

func TestNativeTracker_IssueFilerContract(t *testing.T) {
	forgetest.RunIssueFilerContract(t, newNativeTrackerHarness(t))
}
