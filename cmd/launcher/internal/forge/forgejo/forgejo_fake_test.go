package forgejo_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

type fakePull struct {
	Number    int
	HTMLURL   string
	State     string
	Merged    bool
	Mergeable bool
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
	TargetURL   string `json:"target_url,omitempty"`
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
// commit-status, and repo endpoints. It backs forgejo's PRForge contract
// harness and, per issue #1961 slice 6, the CodeForge contract harness too,
// so it lives in its own file and its helper methods are shaped like
// exported ones, ready for both harnesses to embed.
type fakeForgejo struct {
	mu  sync.Mutex
	srv *httptest.Server

	pulls         map[string]*fakePull // keyed by PR number (string)
	shaToNum      map[string]string    // head SHA to PR number
	checkQueues   map[string][]forge.RollupState
	failingChecks map[string][]fakeStatus
	jobLogs       map[int]string // run number to the raw log of that run's second job
	enqueued      map[string]bool
	autoMergeOK   bool

	compareTotalCommits map[string]int // PR number to the total_commits the compare route reports

	// The non-auto-merge POST /pulls/{index}/merge path calls mergeHook
	// before it flips the pull merged. A non-nil error fails the merge
	// request instead of flipping state, which slice 6 uses to inject a
	// real git merge conflict. The nil default keeps slice 5's plain
	// in-memory flip.
	mergeHook func(num string) error

	// The remaining fields serve the compiled Connector's suite
	// (connector_test.go); the native harnesses leave them zero.
	//
	// web is the base of every html_url the fake serves (https://forge.test
	// when empty), repo the bare repository behind the branch routes, and token
	// the credential every request must carry (empty accepts any).
	web       string
	repo      *forgetest.GitRepoFixture
	token     string
	protected map[string]bool // branch to the protected flag its route serves
	// comments is each pull's posted comment bodies; mergeStatuses scripts the
	// statuses the next merge requests answer, in order, before the real answer
	// resumes; merges counts merge requests; nextNum numbers created pulls.
	comments      map[string][]string
	mergeStatuses []int
	// mergeableReads scripts a pull's mergeable flag per GET, in order, before
	// the pull's own flag resumes.
	mergeableReads map[string][]bool
	merges         int
	nextNum        int
	// unexpected is every request no route served and every one the credential
	// check rejected.
	unexpected []string
	// trk is the issue tracker half (forgejo_fake_tracker_test.go).
	trk fakeTracker
}

var (
	fakeForgejoPullsListRe = regexp.MustCompile(`^/api/v1/repos/owner/repo/pulls$`)
	fakeForgejoPullRe      = regexp.MustCompile(`^/api/v1/repos/owner/repo/pulls/([0-9]+)$`)
	fakeForgejoPullMergeRe = regexp.MustCompile(`^/api/v1/repos/owner/repo/pulls/([0-9]+)/merge$`)
	fakeForgejoStatusRe    = regexp.MustCompile(`^/api/v1/repos/owner/repo/commits/([^/]+)/status$`)
	fakeForgejoStatusesRe  = regexp.MustCompile(`^/api/v1/repos/owner/repo/commits/([^/]+)/statuses$`)
	fakeForgejoRunsRe      = regexp.MustCompile(`^/api/v1/repos/owner/repo/actions/runs$`)
	fakeForgejoRunJobsRe   = regexp.MustCompile(`^/api/v1/repos/owner/repo/actions/runs/([0-9]+)/jobs$`)
	fakeForgejoJobLogRe    = regexp.MustCompile(`^/api/v1/repos/owner/repo/actions/jobs/([0-9]+)/logs$`)
	fakeForgejoRepoRootRe  = regexp.MustCompile(`^/api/v1/repos/owner/repo$`)
	fakeForgejoCompareRe   = regexp.MustCompile(`^/api/v1/repos/owner/repo/compare/`)
	fakeForgejoBranchRe    = regexp.MustCompile(`^/api/v1/repos/owner/repo/branches/(.+)$`)
	fakeForgejoCommentRe   = regexp.MustCompile(`^/api/v1/repos/owner/repo/issues/([0-9]+)/comments$`)
	fakeForgejoIssueNumRe  = regexp.MustCompile(`issue-(\d+)`)
)

func newFakeForgejo(t *testing.T) *fakeForgejo {
	t.Helper()
	return startFakeForgejo(t, httptest.NewServer)
}

// startFakeForgejo serves the fake with start, httptest.NewServer or
// httptest.NewTLSServer.
func startFakeForgejo(t *testing.T, start func(http.Handler) *httptest.Server) *fakeForgejo {
	t.Helper()
	f := &fakeForgejo{
		pulls:         map[string]*fakePull{},
		shaToNum:      map[string]string{},
		checkQueues:   map[string][]forge.RollupState{},
		failingChecks: map[string][]fakeStatus{},
		jobLogs:       map[int]string{},
		enqueued:      map[string]bool{},
		protected:     map[string]bool{},
		comments:      map[string][]string{},
		nextNum:       900,
		trk:           newFakeTracker(),
	}
	f.srv = start(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

// URL returns the value to pass as ForgejoCodeForgeConfig.BaseURL.
func (f *fakeForgejo) URL() string { return f.srv.URL }

// prNumFromURL reads the trailing path segment, the same convention the real
// adapter's parsePRIndex uses.
func prNumFromURL(prURL string) string {
	trimmed := strings.TrimRight(prURL, "/")
	idx := strings.LastIndex(trimmed, "/")
	if idx < 0 {
		return trimmed
	}
	return trimmed[idx+1:]
}

// SeedOpenPR registers an open, non-draft pull for issue num and returns its
// html_url. The head ref must stay "agent/issue-"+num: handleCompare recovers
// the PR number from it.
func (f *fakeForgejo) SeedOpenPR(num string) string {
	return f.seedPull(num, false)
}

// SeedDraftPR mirrors SeedOpenPR but marks the pull draft. It backs the
// regression coverage for issue #2408: OpenPRForBranch must adopt a draft
// pull exactly as it adopts a non-draft one.
func (f *fakeForgejo) SeedDraftPR(num string) string {
	return f.seedPull(num, true)
}

func (f *fakeForgejo) seedPull(num string, draft bool) string {
	return f.seedPullHead(num, "agent/issue-"+num, draft)
}

// seedPullHead is seedPull for a head other than agent/issue-<num>.
func (f *fakeForgejo) seedPullHead(num, head string, draft bool) string {
	f.mu.Lock()
	defer f.mu.Unlock()

	n, _ := strconv.Atoi(num)
	web := f.web
	if web == "" {
		web = "https://forge.test"
	}
	url := web + "/owner/repo/pulls/" + num
	sha := "sha" + num
	title := "Issue " + num
	if draft {
		title = "WIP: " + title
	}
	f.pulls[num] = &fakePull{
		Number:    n,
		HTMLURL:   url,
		State:     "open",
		Merged:    false,
		Mergeable: true,
		Title:     title,
		HeadRef:   head,
		HeadSHA:   sha,
		BaseRef:   "main",
	}
	f.shaToNum[sha] = num
	return url
}

// SeedCheckStates scripts the RollupState values the combined commit-status
// route pops, one per call, in order.
func (f *fakeForgejo) SeedCheckStates(url string, states []forge.RollupState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checkQueues[prNumFromURL(url)] = append([]forge.RollupState(nil), states...)
}

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

// SeedFailingCheckLog is SeedFailingCheck for a check that ran as an Actions
// job: the status carries the job page as a relative target_url, as Forgejo
// does, and the log is served only through the REST chain (run number, run id,
// job id). The ids are offset from one another and the target is the run's
// second job, so a path that confuses them or the job index finds no log.
func (f *fakeForgejo) SeedFailingCheckLog(url, name, log string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	num := prNumFromURL(url)
	run := len(f.jobLogs) + 1
	f.jobLogs[run] = log
	f.failingChecks[num] = append(f.failingChecks[num], fakeStatus{
		Context:     name,
		State:       "failure",
		Description: "job failed",
		TargetURL:   fmt.Sprintf("/owner/repo/actions/runs/%d/jobs/1", run),
	})
}

// The id each seeded run number maps to: run ids, the two job ids of a run's
// jobs list (decoy first, target second), all distinct from the run number.
const (
	fakeRunIDOffset       = 100
	fakeTargetJobIDOffset = 200
	fakeDecoyJobIDOffset  = 900
)

func fakeRunID(run int) int       { return run + fakeRunIDOffset }
func fakeDecoyJobID(run int) int  { return run + fakeDecoyJobIDOffset }
func fakeTargetJobID(run int) int { return run + fakeTargetJobIDOffset }

// SeedAutoMergeAllowed sets the auto-merge eligibility the repo GET route
// reports through allow_merge_commits, allow_rebase, and allow_squash_merge.
func (f *fakeForgejo) SeedAutoMergeAllowed(allowed bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.autoMergeOK = allowed
}

// SetMergeable overrides num's mergeable flag. Slice 6's CodeForge harness
// sets it false alongside a real git conflict (GitRepoFixture's ConflictBase)
// so the adapter's classifyMergeFailure, which reads Mergeable over REST,
// reports forge.ErrMergeConflict and not forge.ErrMergeBlockedByChecks.
func (f *fakeForgejo) SetMergeable(num string, mergeable bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, ok := f.pulls[num]; ok {
		p.Mergeable = mergeable
	}
}

// ScriptMergeableReads makes num's next GETs report these mergeable flags in
// order, the way a real server's async conflict check reads false, then true.
func (f *fakeForgejo) ScriptMergeableReads(num string, reads ...bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.mergeableReads == nil {
		f.mergeableReads = map[string][]bool{}
	}
	f.mergeableReads[num] = append(f.mergeableReads[num], reads...)
}

// SeedNeedsUpdate scripts the compare route's total_commits, nonzero for true
// and 0 for false, because forgejo_prforge.go's NeedsUpdate reads that field
// from the swapped-refs compare call.
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

func (f *fakeForgejo) AutoMergeEnqueued(url string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.enqueued[prNumFromURL(url)]
}

// IsDraft is the fake's oracle for the "draft" field it serves, derived from
// the title exactly as fakeIsDraftTitle describes; pullPayload does the same.
func (f *fakeForgejo) IsDraft(url string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.pulls[prNumFromURL(url)]
	if !ok {
		return false
	}
	return fakeIsDraftTitle(p.Title)
}

// fakeWIPPrefixes mirrors real Forgejo's default WORK_IN_PROGRESS_PREFIXES
// config. Keep it separate from the adapter's own forgejoWIPPrefixes
// (forgejo_prforge.go): this fake stands in for a real server, which derives
// its draft field independently of this codebase's adapter.
var fakeWIPPrefixes = []string{"WIP:", "[WIP]"}

// fakeIsDraftTitle mirrors how real Forgejo derives a pull's served "draft"
// field entirely from its title (services/convert/pull.go sets Draft from
// pr.IsWorkInProgress(ctx), never from an independently settable flag).
func fakeIsDraftTitle(title string) bool {
	upper := strings.ToUpper(strings.TrimSpace(title))
	for _, prefix := range fakeWIPPrefixes {
		if strings.HasPrefix(upper, strings.ToUpper(prefix)) {
			return true
		}
	}
	return false
}

func pullPayload(p *fakePull) map[string]any {
	return map[string]any{
		"number":    p.Number,
		"html_url":  p.HTMLURL,
		"state":     p.State,
		"merged":    p.Merged,
		"mergeable": p.Mergeable,
		"draft":     fakeIsDraftTitle(p.Title),
		"title":     p.Title,
		"head":      map[string]any{"ref": p.HeadRef, "sha": p.HeadSHA},
		"base":      map[string]any{"ref": p.BaseRef},
	}
}

func (f *fakeForgejo) handle(w http.ResponseWriter, r *http.Request) {
	if f.token != "" && r.Header.Get("Authorization") != "Bearer "+f.token {
		f.reject(w, r)
		return
	}
	switch {
	case f.serveTracker(w, r):
	case r.Method == http.MethodGet && fakeForgejoRepoRootRe.MatchString(r.URL.Path):
		f.handleRepoRoot(w, r)
	case r.Method == http.MethodPost && fakeForgejoPullsListRe.MatchString(r.URL.Path):
		f.handleCreatePull(w, r)
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
	case r.Method == http.MethodGet && fakeForgejoJobLogRe.MatchString(r.URL.Path):
		f.handleJobLog(w, r, fakeForgejoJobLogRe.FindStringSubmatch(r.URL.Path)[1])
	case r.Method == http.MethodGet && fakeForgejoRunsRe.MatchString(r.URL.Path):
		f.handleRuns(w, r)
	case r.Method == http.MethodGet && fakeForgejoRunJobsRe.MatchString(r.URL.Path):
		f.handleRunJobs(w, r, fakeForgejoRunJobsRe.FindStringSubmatch(r.URL.Path)[1])
	case r.Method == http.MethodGet && fakeForgejoCompareRe.MatchString(r.URL.Path):
		f.handleCompare(w, r)
	case r.Method == http.MethodGet && fakeForgejoBranchRe.MatchString(r.URL.Path):
		f.handleGetBranch(w, fakeForgejoBranchRe.FindStringSubmatch(r.URL.Path)[1])
	case r.Method == http.MethodDelete && fakeForgejoBranchRe.MatchString(r.URL.Path):
		f.handleDeleteBranch(w, r, fakeForgejoBranchRe.FindStringSubmatch(r.URL.Path)[1])
	case r.Method == http.MethodPost && fakeForgejoCommentRe.MatchString(r.URL.Path):
		f.handleComment(w, r, fakeForgejoCommentRe.FindStringSubmatch(r.URL.Path)[1])
	default:
		f.reject(w, r)
	}
}

// reject records a request the fake does not serve, or serves to no credential.
func (f *fakeForgejo) reject(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.unexpected = append(f.unexpected, r.Method+" "+r.URL.RequestURI())
	f.mu.Unlock()
	http.NotFound(w, r)
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
	var nums []string
	for num, p := range f.pulls {
		if state == "open" && p.State != "open" {
			continue
		}
		nums = append(nums, num)
	}
	// Deterministic order matters here: listPulls walks pages until one
	// repeats the previous page's first item (issue #3978), and Go's
	// randomized map iteration order would otherwise make "first item" flap
	// between requests and defeat that guard, looping forever.
	sort.Slice(nums, func(i, j int) bool {
		ni, _ := strconv.Atoi(nums[i])
		nj, _ := strconv.Atoi(nums[j])
		return ni < nj
	})
	out := make([]map[string]any, len(nums))
	for i, num := range nums {
		out[i] = pullPayload(f.pulls[num])
	}
	f.mu.Unlock()

	// Paginate like the real server so a test's full pull set (always under
	// forgejoServerPageCap here) is served on page 1 and every subsequent
	// page comes back empty, ending listPulls' walk.
	json.NewEncoder(w).Encode(windowPage(r, out))
}

func (f *fakeForgejo) handleGetPull(w http.ResponseWriter, _ *http.Request, num string) {
	f.mu.Lock()
	p, ok := f.pulls[num]
	payload := map[string]any(nil)
	if ok {
		payload = pullPayload(p)
		if reads := f.mergeableReads[num]; len(reads) > 0 {
			payload["mergeable"], f.mergeableReads[num] = reads[0], reads[1:]
		}
	}
	f.mu.Unlock()
	if !ok {
		http.NotFound(w, nil)
		return
	}
	json.NewEncoder(w).Encode(payload)
}

func (f *fakeForgejo) handlePatchPull(w http.ResponseWriter, r *http.Request, num string) {
	var body struct {
		Title string `json:"title"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	f.mu.Lock()
	p, ok := f.pulls[num]
	if ok && body.Title != "" { // upstream EditPullRequest ignores an empty title
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
	f.merges++
	if len(f.mergeStatuses) > 0 {
		status := f.mergeStatuses[0]
		f.mergeStatuses = f.mergeStatuses[1:]
		f.mu.Unlock()
		w.WriteHeader(status)
		return
	}
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
		// A failing status makes the combined state failure, as on a real
		// server, so a failing check is seen without a scripted queue.
		if failing := f.failingChecks[num]; len(failing) > 0 {
			json.NewEncoder(w).Encode(map[string]any{"state": "failure", "total_count": len(failing)})
			return
		}
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
// mirroring forgejoCompare in forgejo_prforge.go. It recovers the PR number
// from the head ref embedded in the compare path, which seedPull always
// writes as agent/issue-<num>.
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

func (f *fakeForgejo) handleRuns(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	run, err := strconv.Atoi(r.URL.Query().Get("run_number"))
	if _, ok := f.jobLogs[run]; err != nil || !ok {
		w.Write([]byte(`{"workflow_runs":[]}`))
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"workflow_runs": []map[string]int{{"id": fakeRunID(run)}}})
}

func (f *fakeForgejo) handleRunJobs(w http.ResponseWriter, r *http.Request, runID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, _ := strconv.Atoi(runID)
	run := id - fakeRunIDOffset
	if _, ok := f.jobLogs[run]; !ok || fakeRunID(run) != id {
		http.NotFound(w, r)
		return
	}
	json.NewEncoder(w).Encode([]map[string]int{{"id": fakeDecoyJobID(run)}, {"id": fakeTargetJobID(run)}})
}

func (f *fakeForgejo) handleJobLog(w http.ResponseWriter, r *http.Request, jobID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, _ := strconv.Atoi(jobID)
	log, ok := f.jobLogs[id-fakeTargetJobIDOffset]
	if !ok || fakeTargetJobID(id-fakeTargetJobIDOffset) != id {
		http.NotFound(w, r)
		return
	}
	w.Write([]byte(log))
}

func (f *fakeForgejo) branchExists(branch string) bool {
	return f.repo != nil && exec.Command("git", "-C", f.repo.Bare, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch).Run() == nil
}

func (f *fakeForgejo) handleGetBranch(w http.ResponseWriter, branch string) {
	if !f.branchExists(branch) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	f.mu.Lock()
	protected := f.protected[branch]
	f.mu.Unlock()
	json.NewEncoder(w).Encode(map[string]any{"name": branch, "protected": protected})
}

func (f *fakeForgejo) handleDeleteBranch(w http.ResponseWriter, r *http.Request, branch string) {
	if !f.branchExists(branch) {
		http.NotFound(w, r)
		return
	}
	if out, err := exec.Command("git", "-C", f.repo.Bare, "branch", "-D", branch).CombinedOutput(); err != nil {
		http.Error(w, string(out), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleCreatePull opens a pull for a head branch that exists, and refuses one
// whose head already has an open pull, as Forgejo does.
func (f *fakeForgejo) handleCreatePull(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title string `json:"title"`
		Head  string `json:"head"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	if !f.branchExists(body.Head) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	f.mu.Lock()
	for _, p := range f.pulls {
		if p.HeadRef == body.Head && p.State == "open" {
			f.mu.Unlock()
			w.WriteHeader(http.StatusConflict)
			return
		}
	}
	f.nextNum++
	num := strconv.Itoa(f.nextNum)
	f.mu.Unlock()

	f.seedPullHead(num, body.Head, false)
	f.mu.Lock()
	f.pulls[num].Title = body.Title
	payload := pullPayload(f.pulls[num])
	f.mu.Unlock()
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(payload)
}

func (f *fakeForgejo) handleComment(w http.ResponseWriter, r *http.Request, num string) {
	var body struct {
		Body string `json:"body"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.pulls[num]; !ok {
		http.NotFound(w, r)
		return
	}
	f.comments[num] = append(f.comments[num], body.Body)
	w.WriteHeader(http.StatusCreated)
}
