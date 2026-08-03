package forgejo_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// fakePull is one scripted pull request in fakeForgejo's in-memory backend.
type fakePull struct {
	Number    int
	HTMLURL   string
	State     string
	Merged    bool
	Mergeable bool
	Draft     bool
	Title     string
	HeadRef   string
	HeadSHA   string
	BaseRef   string
}

// fakeStatus is one scripted commit-status entry, mirroring the shape
// Forgejo's /commits/{sha}/statuses endpoint returns.
type fakeStatus struct {
	Context     string `json:"context"`
	State       string `json:"state"`
	Description string `json:"description"`
}

// rollupToForgejoState maps a canonical forge.RollupState to the lowercase
// combined-status state string Forgejo's /commits/{sha}/status endpoint
// returns.
var rollupToForgejoState = map[forge.RollupState]string{
	forge.StateSuccess: "success",
	forge.StatePending: "pending",
	forge.StateFailure: "failure",
	forge.StateError:   "error",
}

// fakeForgejo is an in-memory stand-in for the Forgejo REST API's pull,
// commit-status, and repo endpoints, backing forgejo's PRForge contract
// harness (and, per issue #1961 slice 6, the CodeForge contract harness too
// — hence its own file and exported-shaped helper methods, ready to be
// embedded by both).
type fakeForgejo struct {
	mu  sync.Mutex
	srv *httptest.Server

	pulls         map[string]*fakePull // keyed by PR number (string)
	shaToNum      map[string]string    // head SHA -> PR number
	checkQueues   map[string][]forge.RollupState
	failingChecks map[string][]fakeStatus
	enqueued      map[string]bool
	autoMergeOK   bool

	compareTotalCommits map[string]int // PR num -> total_commits the compare route reports

	// mergeHook, when set, is invoked by the non-auto-merge POST
	// /pulls/{index}/merge path before the pull is flipped merged. A
	// non-nil error fails the merge request rather than flipping state —
	// slice 6 uses this to inject a real git merge/conflict. nil (the
	// default) keeps slice 5's plain in-memory flip.
	mergeHook func(num string) error
}

var (
	fakeForgejoPullsListRe = regexp.MustCompile(`^/api/v1/repos/owner/repo/pulls$`)
	fakeForgejoPullRe      = regexp.MustCompile(`^/api/v1/repos/owner/repo/pulls/([0-9]+)$`)
	fakeForgejoPullMergeRe = regexp.MustCompile(`^/api/v1/repos/owner/repo/pulls/([0-9]+)/merge$`)
	fakeForgejoStatusRe    = regexp.MustCompile(`^/api/v1/repos/owner/repo/commits/([^/]+)/status$`)
	fakeForgejoStatusesRe  = regexp.MustCompile(`^/api/v1/repos/owner/repo/commits/([^/]+)/statuses$`)
	fakeForgejoRepoRootRe  = regexp.MustCompile(`^/api/v1/repos/owner/repo$`)
	fakeForgejoCompareRe   = regexp.MustCompile(`^/api/v1/repos/owner/repo/compare/`)
	fakeForgejoIssueNumRe  = regexp.MustCompile(`issue-(\d+)`)
)

// newFakeForgejo starts the fake server and registers its cleanup on t.
func newFakeForgejo(t *testing.T) *fakeForgejo {
	t.Helper()
	f := &fakeForgejo{
		pulls:         map[string]*fakePull{},
		shaToNum:      map[string]string{},
		checkQueues:   map[string][]forge.RollupState{},
		failingChecks: map[string][]fakeStatus{},
		enqueued:      map[string]bool{},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

// URL returns the fake server's base URL — the value to pass as
// ForgejoCodeForgeConfig.BaseURL.
func (f *fakeForgejo) URL() string { return f.srv.URL }

// prNumFromURL extracts the trailing path segment from a PR URL, the same
// convention the real adapter's parsePRIndex reads.
func prNumFromURL(prURL string) string {
	trimmed := strings.TrimRight(prURL, "/")
	idx := strings.LastIndex(trimmed, "/")
	if idx < 0 {
		return trimmed
	}
	return trimmed[idx+1:]
}

// SeedOpenPR registers an OPEN, non-draft pull for issue num, whose head ref
// is the agent branch for num ("agent/issue-"+num) and head SHA is a
// per-PR synthetic value, and returns its html_url.
func (f *fakeForgejo) SeedOpenPR(num string) string {
	f.mu.Lock()
	defer f.mu.Unlock()

	n, _ := strconv.Atoi(num)
	url := "https://forge.test/owner/repo/pulls/" + num
	sha := "sha" + num
	f.pulls[num] = &fakePull{
		Number:    n,
		HTMLURL:   url,
		State:     "open",
		Merged:    false,
		Mergeable: true,
		Draft:     false,
		Title:     "Issue " + num,
		HeadRef:   "agent/issue-" + num,
		HeadSHA:   sha,
		BaseRef:   "main",
	}
	f.shaToNum[sha] = num
	return url
}

// SeedCheckStates scripts the sequence of RollupState values the combined
// commit-status route pops, in order, for url's PR.
func (f *fakeForgejo) SeedCheckStates(url string, states []forge.RollupState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checkQueues[prNumFromURL(url)] = append([]forge.RollupState(nil), states...)
}

// SeedFailingCheck scripts one failing commit-status entry for url's PR.
func (f *fakeForgejo) SeedFailingCheck(url, name, conclusion, summary string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	num := prNumFromURL(url)
	f.failingChecks[num] = append(f.failingChecks[num], fakeStatus{
		Context:     name,
		State:       strings.ToLower(conclusion),
		Description: summary,
	})
}

// SeedAutoMergeAllowed scripts the repo-level auto-merge-eligibility bool the
// repo GET route reflects into allow_merge_commits/allow_rebase/
// allow_squash_merge.
func (f *fakeForgejo) SeedAutoMergeAllowed(allowed bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.autoMergeOK = allowed
}

// SetMergeable overrides num's PR's mergeable flag — slice 6's CodeForge
// harness flips this false alongside a real git conflict (GitRepoFixture's
// ConflictBase) so the adapter's classifyMergeFailure, which queries
// Mergeable via REST, reports forge.ErrMergeConflict rather than
// forge.ErrMergeBlockedByChecks for a scripted merge failure.
func (f *fakeForgejo) SetMergeable(num string, mergeable bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, ok := f.pulls[num]; ok {
		p.Mergeable = mergeable
	}
}

// SeedNeedsUpdate scripts num's compare-route total_commits: >0 (needsUpdate
// true) or 0 (false) — mirrors forgejo_prforge.go's NeedsUpdate, which reads
// total_commits from the swapped-refs compare call.
func (f *fakeForgejo) SeedNeedsUpdate(url string, needsUpdate bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.compareTotalCommits == nil {
		f.compareTotalCommits = map[string]int{}
	}
	n := 0
	if needsUpdate {
		n = 1
	}
	f.compareTotalCommits[prNumFromURL(url)] = n
}

// AutoMergeEnqueued reports whether the merge route recorded url's PR as
// enqueued via merge_when_checks_succeed.
func (f *fakeForgejo) AutoMergeEnqueued(url string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.enqueued[prNumFromURL(url)]
}

func pullPayload(p *fakePull) map[string]any {
	return map[string]any{
		"number":    p.Number,
		"html_url":  p.HTMLURL,
		"state":     p.State,
		"merged":    p.Merged,
		"mergeable": p.Mergeable,
		"draft":     p.Draft,
		"title":     p.Title,
		"head":      map[string]any{"ref": p.HeadRef, "sha": p.HeadSHA},
		"base":      map[string]any{"ref": p.BaseRef},
	}
}

func (f *fakeForgejo) handle(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && fakeForgejoRepoRootRe.MatchString(r.URL.Path):
		f.handleRepoRoot(w, r)
	case r.Method == http.MethodGet && fakeForgejoPullsListRe.MatchString(r.URL.Path):
		f.handlePullsList(w, r)
	case r.Method == http.MethodPost && fakeForgejoPullMergeRe.MatchString(r.URL.Path):
		f.handleMerge(w, r, fakeForgejoPullMergeRe.FindStringSubmatch(r.URL.Path)[1])
	case r.Method == http.MethodGet && fakeForgejoPullRe.MatchString(r.URL.Path):
		f.handleGetPull(w, r, fakeForgejoPullRe.FindStringSubmatch(r.URL.Path)[1])
	case r.Method == http.MethodPatch && fakeForgejoPullRe.MatchString(r.URL.Path):
		f.handlePatchPull(w, r, fakeForgejoPullRe.FindStringSubmatch(r.URL.Path)[1])
	case r.Method == http.MethodGet && fakeForgejoStatusRe.MatchString(r.URL.Path):
		f.handleCommitStatus(w, r, fakeForgejoStatusRe.FindStringSubmatch(r.URL.Path)[1])
	case r.Method == http.MethodGet && fakeForgejoStatusesRe.MatchString(r.URL.Path):
		f.handleCommitStatuses(w, r, fakeForgejoStatusesRe.FindStringSubmatch(r.URL.Path)[1])
	case r.Method == http.MethodGet && fakeForgejoCompareRe.MatchString(r.URL.Path):
		f.handleCompare(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeForgejo) handleRepoRoot(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	allowed := f.autoMergeOK
	f.mu.Unlock()
	json.NewEncoder(w).Encode(map[string]any{
		"full_name":           "owner/repo",
		"allow_merge_commits": allowed,
		"allow_rebase":        allowed,
		"allow_squash_merge":  allowed,
	})
}

func (f *fakeForgejo) handlePullsList(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	f.mu.Lock()
	var out []map[string]any
	for _, p := range f.pulls {
		if state == "open" && p.State != "open" {
			continue
		}
		out = append(out, pullPayload(p))
	}
	f.mu.Unlock()
	if out == nil {
		out = []map[string]any{}
	}
	json.NewEncoder(w).Encode(out)
}

func (f *fakeForgejo) handleGetPull(w http.ResponseWriter, _ *http.Request, num string) {
	f.mu.Lock()
	p, ok := f.pulls[num]
	f.mu.Unlock()
	if !ok {
		http.NotFound(w, nil)
		return
	}
	json.NewEncoder(w).Encode(pullPayload(p))
}

func (f *fakeForgejo) handlePatchPull(w http.ResponseWriter, r *http.Request, num string) {
	var body struct {
		Title string `json:"title"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	f.mu.Lock()
	p, ok := f.pulls[num]
	if ok {
		p.Title = body.Title
	}
	f.mu.Unlock()

	if !ok {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (f *fakeForgejo) handleMerge(w http.ResponseWriter, r *http.Request, num string) {
	var body struct {
		Do                     string `json:"Do"`
		MergeWhenChecksSucceed bool   `json:"merge_when_checks_succeed"`
		DeleteBranchAfterMerge bool   `json:"delete_branch_after_merge"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	f.mu.Lock()
	_, ok := f.pulls[num]
	f.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}

	if body.MergeWhenChecksSucceed {
		f.mu.Lock()
		f.enqueued[num] = true
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		return
	}

	f.mu.Lock()
	hook := f.mergeHook
	f.mu.Unlock()
	if hook != nil {
		if err := hook(num); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
	}

	f.mu.Lock()
	if p, ok := f.pulls[num]; ok {
		p.Merged = true
		p.State = "closed"
	}
	f.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (f *fakeForgejo) handleCommitStatus(w http.ResponseWriter, _ *http.Request, sha string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	num, ok := f.shaToNum[sha]
	if !ok {
		json.NewEncoder(w).Encode(map[string]any{"state": "", "total_count": 0})
		return
	}
	queue := f.checkQueues[num]
	if len(queue) == 0 {
		json.NewEncoder(w).Encode(map[string]any{"state": "", "total_count": 0})
		return
	}
	next := queue[0]
	f.checkQueues[num] = queue[1:]
	json.NewEncoder(w).Encode(map[string]any{
		"state":       rollupToForgejoState[next],
		"total_count": 1,
	})
}

// handleCompare serves the swapped-refs compare route NeedsUpdate reads,
// mirroring forgejoCompare in forgejo_prforge.go: {"total_commits": <n>}.
// The PR number is recovered from the head ref embedded in the compare
// path, which is always agent/issue-<num>.
func (f *fakeForgejo) handleCompare(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	match := fakeForgejoIssueNumRe.FindStringSubmatch(r.URL.Path)
	n := 0
	if match != nil {
		n = f.compareTotalCommits[match[1]]
	}
	json.NewEncoder(w).Encode(map[string]any{"total_commits": n})
}

func (f *fakeForgejo) handleCommitStatuses(w http.ResponseWriter, _ *http.Request, sha string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	num, ok := f.shaToNum[sha]
	statuses := []fakeStatus{}
	if ok {
		statuses = append(statuses, f.failingChecks[num]...)
	}
	json.NewEncoder(w).Encode(statuses)
}
