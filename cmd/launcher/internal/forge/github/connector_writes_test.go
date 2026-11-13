package github

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

// branchRules scripts what GitHub reports of one branch's protection: the
// status of the classic-protection read, and how many ruleset rules apply.
type branchRules struct {
	classic  int
	rulesets int
}

// hostileRepo is a repository the Connector's Route table does not declare.
const hostileRepo = "elsewhere/repo"

func nodeID(num int) string { return "PR_node_" + strconv.Itoa(num) }

func fakeSHA(num int) string { return fmt.Sprintf("%040x", num) }

func (b *restBackend) prURL(num int) string {
	return b.web + "/pull/" + strconv.Itoa(num)
}

// gitOut runs git in the bare repo behind the scripted forge.
func (b *restBackend) gitOut(args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", b.repo.Bare}, args...)...).Output()
	return strings.TrimSpace(string(out)), err
}

// headSHA is the head branch's real commit when the bare repo has it, so a
// landed PR is real, and a stable fake otherwise.
func (b *restBackend) headSHA(p *restPR) string {
	if sha, err := b.gitOut("rev-parse", "--verify", "--quiet", "refs/heads/"+p.branch); err == nil {
		return sha
	}
	return fakeSHA(p.num)
}

func (b *restBackend) headExists(p *restPR) bool {
	_, err := b.gitOut("rev-parse", "--verify", "--quiet", "refs/heads/"+p.branch)
	return err == nil
}

// rebaseTree is the tree of p's head rebased onto its base, and false when the
// two conflict. A PR whose head is not a real branch never conflicts.
func (b *restBackend) rebaseTree(p *restPR) (string, bool) {
	if !b.headExists(p) {
		return "", true
	}
	tree, err := b.gitOut("merge-tree", "--write-tree", "refs/heads/"+p.base, "refs/heads/"+p.branch)
	if err != nil {
		return "", false
	}
	return tree, true
}

func (b *restBackend) prByNumber(num string) *restPR {
	n, err := strconv.Atoi(num)
	if err != nil {
		return nil
	}
	return b.prs[n]
}

func writeJSONStatus(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func gqlError(w http.ResponseWriter, msg string) {
	writeJSON(w, map[string]any{"errors": []map[string]string{{"message": msg}}})
}

// serveWrite answers the REST routes of the forge write half that are not a
// pull request's own, reporting whether it recognised the request.
func (b *restBackend) serveWrite(w http.ResponseWriter, r *http.Request, reject func(string)) bool {
	repo := "/repos/" + connectorRepo
	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && strings.HasPrefix(path, repo+"/compare/"):
		basehead := strings.TrimPrefix(path, repo+"/compare/")
		_, head, _ := strings.Cut(basehead, "...")
		for _, p := range b.prs {
			if p.branch == head {
				writeJSON(w, map[string]any{"behind_by": p.behind})
				return true
			}
		}
		reject("compare for a head no PR has")
	case r.Method == http.MethodGet && strings.HasPrefix(path, repo+"/git/matching-refs/heads/"):
		// matching-refs is a prefix match, as on GitHub.
		prefix := "refs/heads/" + strings.TrimPrefix(path, repo+"/git/matching-refs/heads/")
		out, _ := b.gitOut("for-each-ref", "--format=%(refname)", "refs/heads/")
		refs := []map[string]string{}
		for _, ref := range strings.Fields(out) {
			if strings.HasPrefix(ref, prefix) {
				refs = append(refs, map[string]string{"ref": ref})
			}
		}
		writeJSON(w, refs)
	case r.Method == http.MethodDelete && strings.HasPrefix(path, repo+"/git/refs/heads/"):
		ref := "refs/heads/" + strings.TrimPrefix(path, repo+"/git/refs/heads/")
		if _, err := b.gitOut("rev-parse", "--verify", "--quiet", ref); err != nil {
			writeJSONStatus(w, http.StatusUnprocessableEntity, map[string]string{"message": "Reference does not exist"})
			return true
		}
		if _, err := b.gitOut("update-ref", "-d", ref); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return true
		}
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && strings.HasPrefix(path, repo+"/branches/") && strings.HasSuffix(path, "/protection"):
		branch := strings.TrimSuffix(strings.TrimPrefix(path, repo+"/branches/"), "/protection")
		status := http.StatusNotFound
		if rules, ok := b.protection[branch]; ok {
			status = rules.classic
		}
		writeJSONStatus(w, status, map[string]string{"message": "scripted protection read"})
	case r.Method == http.MethodGet && strings.HasPrefix(path, repo+"/rules/branches/"):
		branch := strings.TrimPrefix(path, repo+"/rules/branches/")
		rules := make([]map[string]string, b.protection[branch].rulesets)
		for i := range rules {
			rules[i] = map[string]string{"type": "pull_request"}
		}
		writeJSON(w, rules)
	case r.Method == http.MethodPost && strings.HasPrefix(path, repo+"/issues/") && strings.HasSuffix(path, "/comments"):
		p := b.prByNumber(strings.TrimSuffix(strings.TrimPrefix(path, repo+"/issues/"), "/comments"))
		var in struct {
			Body string `json:"body"`
		}
		if p == nil || json.NewDecoder(r.Body).Decode(&in) != nil {
			reject("comment on no such pull request, or an undecodable body")
			return true
		}
		p.comments = append(p.comments, in.Body)
		writeJSONStatus(w, http.StatusCreated, map[string]any{})
	default:
		return false
	}
	return true
}

// openPR creates a PR as GitHub does: the head must be a real branch, and one
// open PR per head.
func (b *restBackend) openPR(w http.ResponseWriter, r *http.Request, reject func(string)) {
	var in struct {
		Title, Body, Base, Head string
		Draft                   bool
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		reject("undecodable pull request: " + err.Error())
		return
	}
	// Opened PRs number above every number the tests seed by hand.
	n := 1000
	for _, p := range b.prs {
		if p.branch == in.Head && !p.closed && !p.merged {
			writeJSONStatus(w, http.StatusUnprocessableEntity, map[string]string{"message": "A pull request already exists for " + in.Head})
			return
		}
		n = max(n, p.num+1)
	}
	p := &restPR{num: n, branch: in.Head, base: in.Base, draft: in.Draft, title: in.Title, body: in.Body}
	if !b.headExists(p) {
		writeJSONStatus(w, http.StatusUnprocessableEntity, map[string]string{"message": "Validation Failed: head invalid"})
		return
	}
	b.prs[n] = p
	writeJSONStatus(w, http.StatusCreated, b.wirePR(p))
}

// mergePR lands p's head on its base in the real repo, as GitHub's rebase merge
// does: a draft, closed or merged PR and a conflicting one are refused with 405.
func (b *restBackend) mergePR(w http.ResponseWriter, r *http.Request, p *restPR, reject func(string)) {
	b.merges++
	if len(b.mergeStatuses) > 0 {
		status := b.mergeStatuses[0]
		b.mergeStatuses = b.mergeStatuses[1:]
		writeJSONStatus(w, status, map[string]string{"message": "scripted merge failure"})
		return
	}
	var in struct {
		MergeMethod string `json:"merge_method"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.MergeMethod != "rebase" {
		reject("merge request that is not a rebase merge")
		return
	}
	refuse := func() {
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"message": "Pull Request is not mergeable"})
	}
	if p.draft || p.merged || p.closed {
		refuse()
		return
	}
	sha := b.headSHA(p)
	if b.headExists(p) {
		tree, ok := b.rebaseTree(p)
		if !ok {
			refuse()
			return
		}
		base := "refs/heads/" + p.base
		if _, err := b.gitOut("merge-base", "--is-ancestor", base, sha); err != nil {
			commit, err := b.gitOut("commit-tree", tree, "-p", base, "-m", "Rebase PR "+strconv.Itoa(p.num))
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			sha = commit
		}
		if _, err := b.gitOut("update-ref", base, sha); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	p.merged, p.mergeSHA = true, sha
	writeJSON(w, map[string]any{"merged": true, "sha": sha})
}

// repoQuery answers the repository-identity GraphQL query.
func (b *restBackend) repoQuery(w http.ResponseWriter, owner, repo string) {
	web := b.web
	switch owner + "/" + repo {
	case connectorRepo:
	case hostileRepo:
		web = "https://" + b.host + "/" + hostileRepo
	default:
		writeJSON(w, map[string]any{"data": map[string]any{"repository": nil}})
		return
	}
	writeJSON(w, map[string]any{"data": map[string]any{"repository": map[string]any{
		"url": web, "nameWithOwner": owner + "/" + repo, "autoMergeAllowed": b.autoMerge,
	}}})
}

// mutation applies a pull-request GraphQL mutation. As on GitHub, a flip to the
// state the PR is already in is an error, and a draft cannot take auto-merge.
func (b *restBackend) mutation(w http.ResponseWriter, query, id string, reject func(string)) {
	var p *restPR
	for _, c := range b.prs {
		if nodeID(c.num) == id {
			p = c
		}
	}
	if p == nil {
		reject("mutation on no such pull request node")
		return
	}
	field := ""
	for _, f := range []string{"markPullRequestReadyForReview", "convertPullRequestToDraft", "enablePullRequestAutoMerge"} {
		if strings.Contains(query, f) {
			field = f
		}
	}
	switch {
	case field == "markPullRequestReadyForReview" && !p.draft:
		gqlError(w, "Pull request is not a draft")
		return
	case field == "markPullRequestReadyForReview":
		p.draft = false
	case field == "convertPullRequestToDraft" && p.draft:
		gqlError(w, "Pull request is already a draft")
		return
	case field == "convertPullRequestToDraft":
		p.draft = true
	case field == "enablePullRequestAutoMerge" && !strings.Contains(query, "mergeMethod:REBASE"):
		reject("auto-merge enabled with a method other than rebase")
		return
	case field == "enablePullRequestAutoMerge" && p.draft:
		gqlError(w, "Pull request is in draft state")
		return
	case field == "enablePullRequestAutoMerge":
		p.enqueued = true
	default:
		reject("unknown mutation")
		return
	}
	writeJSON(w, map[string]any{"data": map[string]any{field: map[string]any{"clientMutationId": nil}}})
}

// routeCoverage accumulates, across the harnesses of one test, which of the
// Connector's declared Routes admitted a request.
type routeCoverage struct {
	routes   map[string]bool
	admitted map[string]int
}

// record folds one harness's audit log in and fails the test on a request the
// backend did not expect or the host did not send.
func (c *routeCoverage) record(t *testing.T, h *connectorHarness) {
	t.Helper()
	h.backend.mu.Lock()
	unexpected := slices.Clone(h.backend.unexpected)
	h.backend.mu.Unlock()
	if len(unexpected) > 0 {
		t.Errorf("backend saw requests outside the Connector's declared work: %q", unexpected)
	}
	if c.routes == nil {
		c.routes, c.admitted = map[string]bool{}, map[string]int{}
	}
	// Harnesses differ in the backend host their Routes name, so a route is
	// keyed by method and path.
	key := map[string]string{}
	for _, r := range h.routes {
		key[r.String()] = r.Method + " " + r.Path
		c.routes[r.Method+" "+r.Path] = true
	}
	for _, e := range h.auditEntries(t) {
		if e.Decision != connectorhost.DecisionSent {
			t.Errorf("audit entry %+v: want every request sent", e)
		}
		c.admitted[key[e.Route]]++
	}
}

// assertUsed fails for every declared Route no recorded request used: that
// route is broader than the Connector's reads and writes need.
func (c *routeCoverage) assertUsed(t *testing.T) {
	t.Helper()
	for r := range c.routes {
		if c.admitted[r] == 0 {
			t.Errorf("route %q admitted no request: the table is broader than the Connector's reads and writes", r)
		}
	}
}

// composedPRHarness is the harness the PRForge contract runs against: the
// PRForge is the one the composed Code Forge resolves.
type composedPRHarness struct{ *connectorHarness }

var _ forgetest.PRForgeHarness = composedPRHarness{}

func (h composedPRHarness) Forge() forge.PRForge { return h.caps.PRForge }

func (h composedPRHarness) SeedAutoMergeAllowed(allowed bool) {
	h.backend.mu.Lock()
	defer h.backend.mu.Unlock()
	h.backend.autoMerge = allowed
}

func (h composedPRHarness) SeedNeedsUpdate(url string, needsUpdate bool) {
	behind := 0
	if needsUpdate {
		behind = 1
	}
	h.mutatePR(prNum(url), func(p *restPR) { p.behind = behind })
}

func (h composedPRHarness) AutoMergeEnqueued(url string) bool {
	return h.prView(url).enqueued
}

// prView snapshots the scripted PR behind url.
func (h *connectorHarness) prView(url string) restPR {
	h.backend.mu.Lock()
	defer h.backend.mu.Unlock()
	return *h.backend.prs[atoi(prNum(url))]
}

// Merged reports whether the forge merged url's PR.
func (h *connectorHarness) Merged(url string) bool { return h.prView(url).merged }

// SeedLandablePR seeds a real agent branch one commit ahead of main and an open,
// ready PR for it, so a Merge lands the branch in the bare repo.
func (h *connectorHarness) SeedLandablePR(num string) string {
	h.t.Helper()
	h.backend.repo.SeedBranch(forge.AgentBranchName(h.BranchPrefix(), num), num)
	return h.SeedOpenPR(num)
}

func (h *connectorHarness) SeedProtection(branch string, classic, rulesets int) {
	h.backend.mu.Lock()
	defer h.backend.mu.Unlock()
	h.backend.protection[branch] = branchRules{classic: classic, rulesets: rulesets}
}

func (h *connectorHarness) comments(url string) []string { return h.prView(url).comments }

// declaredForgeCaps is exactly the forge capabilities the github Connector's
// handshake declares: no CI run URL and no push-only mode.
var declaredForgeCaps = []string{
	"forge", "forge.branches", "forge.prs", "forge.draftPRs", "forge.autoMerge", "forge.branchProtection", "forge.probe",
}

func testComposedCapabilities(t *testing.T, h *connectorHarness) {
	var got []string
	for _, c := range h.conn.Capabilities {
		if c == "forge" || strings.HasPrefix(c, "forge.") {
			got = append(got, c)
		}
	}
	slices.Sort(got)
	if want := slices.Sorted(slices.Values(declaredForgeCaps)); !slices.Equal(got, want) {
		t.Errorf("handshake forge capabilities = %v, want %v", got, want)
	}
	c := h.caps
	if c.PRForge == nil || c.DraftPRCreator == nil || c.BranchPusher == nil || c.BranchProtectionForge == nil || c.MergeCommitReader == nil || c.BranchDeleter == nil {
		t.Errorf("composed forge lacks a surface the Connector declares: %+v", c)
	}
	if c.CIRunReporter != nil {
		t.Error("composed forge offers a CIRunReporter, but the Connector declares no forge.ciRun")
	}
}

// A landed PR is on the base branch in the repo, and its merge commit is what
// the forge reports for it; an unmerged PR reports none.
func testMergeLandsHead(t *testing.T, h *connectorHarness) {
	url := h.SeedLandablePR("401")
	if sha, err := h.caps.MergeCommitReader.MergeCommit(url); err != nil || sha != "" {
		t.Fatalf("MergeCommit before merge = (%q, %v), want none", sha, err)
	}
	if err := h.cf.Merge(url); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if !h.Merged(url) || !h.backend.repo.Landed("401") {
		t.Errorf("after Merge: merged=%v landed=%v, want both", h.Merged(url), h.backend.repo.Landed("401"))
	}
	if sha, err := h.caps.MergeCommitReader.MergeCommit(url); err != nil || sha != h.backend.repo.BranchSHA("main") {
		t.Errorf("MergeCommit = (%q, %v), want main's tip %s", sha, err, h.backend.repo.BranchSHA("main"))
	}
}

// A clean PR reports mergeable; a conflicting one does not land, and the PR
// reports the conflict.
func testMergeableTracksReality(t *testing.T, h *connectorHarness) {
	url := h.SeedLandablePR("402")
	if got, err := h.caps.PRForge.Mergeable(url); err != nil || got != forge.MergeableMergeable {
		t.Fatalf("Mergeable = (%q, %v), want %q", got, err, forge.MergeableMergeable)
	}

	conflicted := h.SeedLandablePR("403")
	h.backend.repo.ConflictBase("403")
	if got, err := h.caps.PRForge.Mergeable(conflicted); err != nil || got != forge.MergeableConflicting {
		t.Errorf("Mergeable of a conflicting PR = (%q, %v), want %q", got, err, forge.MergeableConflicting)
	}
	if err := h.cf.Merge(conflicted); err == nil || h.Merged(conflicted) {
		t.Errorf("Merge of a conflicting PR = %v, merged=%v, want an error and no merge", err, h.Merged(conflicted))
	}
}

// A draft PR is refused by the forge, so it takes MarkReady before it lands.
func testMergeRefusesDraft(t *testing.T, h *connectorHarness) {
	url := h.SeedLandablePR("404")
	if err := h.caps.PRForge.MarkDraft(url); err != nil {
		t.Fatal(err)
	}
	if err := h.cf.Merge(url); err == nil || h.Merged(url) || h.backend.repo.Landed("404") {
		t.Fatalf("Merge of a draft = %v, merged=%v, landed=%v, want an error and nothing landed", err, h.Merged(url), h.backend.repo.Landed("404"))
	}
	if err := h.caps.PRForge.MarkReady(url); err != nil {
		t.Fatal(err)
	}
	if err := h.cf.Merge(url); err != nil || !h.backend.repo.Landed("404") {
		t.Errorf("Merge after MarkReady: err=%v landed=%v", err, h.backend.repo.Landed("404"))
	}
}

func testHeadCommitSHA(t *testing.T, h *connectorHarness) {
	url := h.SeedLandablePR("405")
	got, err := h.caps.PRForge.HeadCommitSHA(url)
	if want := h.backend.repo.BranchSHA(forge.AgentBranchName(h.BranchPrefix(), "405")); err != nil || got != want {
		t.Errorf("HeadCommitSHA = (%q, %v), want %q", got, err, want)
	}
}

// A hand-off opens its PR as a draft, and a retried hand-off adopts the PR it
// opened rather than failing or opening a second one.
func testCreateDraftPRAdopts(t *testing.T, h *connectorHarness) {
	branch := forge.AgentBranchName(h.BranchPrefix(), "406")
	h.backend.repo.SeedBranch(branch, "406")
	url, created, err := h.caps.DraftPRCreator.CreateDraftPR("title", "body", "main", branch)
	if err != nil || !created {
		t.Fatalf("CreateDraftPR = (%q, %v, %v), want a created PR", url, created, err)
	}
	if !h.IsDraft(url) {
		t.Errorf("IsDraft(%q) = false, want the hand-off PR to be a draft", url)
	}
	again, created, err := h.caps.DraftPRCreator.CreateDraftPR("title", "body", "main", branch)
	if err != nil || created || again != url {
		t.Errorf("second CreateDraftPR = (%q, %v, %v), want (%q, false, nil)", again, created, err, url)
	}
	if got, ok, err := h.caps.PRForge.PRForBranch(branch); err != nil || !ok || got != url {
		t.Errorf("PRForBranch = (%q, %v, %v), want %q", got, ok, err, url)
	}
}

// A PR with more files than one page lists them all, in order.
func testListPRFiles(t *testing.T, h *connectorHarness) {
	url := h.SeedOpenPR("407")
	var want []string
	for i := range 230 {
		want = append(want, fmt.Sprintf("dir/file-%03d.go", i))
	}
	h.mutatePR("407", func(p *restPR) { p.files = want })
	got, err := h.caps.PRForge.ListPRFiles(url)
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("ListPRFiles = %d files, %v; want the %d seeded in order", len(got), err, len(want))
	}
}

func testCommentPR(t *testing.T, h *connectorHarness) {
	url := h.SeedOpenPR("408")
	if err := h.caps.PRForge.CommentPR(url, "needs a human"); err != nil {
		t.Fatal(err)
	}
	if got := h.comments(url); !slices.Equal(got, []string{"needs a human"}) {
		t.Errorf("PR comments = %q, want the one posted", got)
	}
}

// BranchExists is exact, not GitHub's prefix match; a delete removes the branch
// and a repeat delete of a missing one counts as done.
func testBranchLifecycle(t *testing.T, h *connectorHarness) {
	short := forge.AgentBranchName(h.BranchPrefix(), "41")
	long := forge.AgentBranchName(h.BranchPrefix(), "410")
	h.backend.repo.SeedBranch(long, "410")
	if ok, err := h.cf.BranchExists(long); err != nil || !ok {
		t.Fatalf("BranchExists(%q) = (%v, %v), want true", long, ok, err)
	}
	if ok, err := h.cf.BranchExists(short); err != nil || ok {
		t.Fatalf("BranchExists(%q) = (%v, %v), want false: %q only shares its prefix", short, ok, err, long)
	}
	if err := h.caps.BranchDeleter.DeleteBranch(long, "main"); err != nil {
		t.Fatalf("DeleteBranch(%q): %v", long, err)
	}
	if ok, err := h.cf.BranchExists(long); err != nil || ok {
		t.Errorf("BranchExists(%q) after delete = (%v, %v), want false", long, ok, err)
	}
	if err := h.caps.BranchDeleter.DeleteBranch(long, "main"); connectorhost.ErrorClass(err) != connectorhost.NotFound {
		t.Errorf("DeleteBranch(%q) of a missing branch = %v, want a not-found error", long, err)
	}
}

func testBranchProtection(t *testing.T, h *connectorHarness) {
	h.SeedProtection("main", http.StatusOK, 0)
	h.SeedProtection("release/1", http.StatusNotFound, 1)
	h.SeedProtection("dev", http.StatusNotFound, 0)
	h.SeedProtection("restricted", http.StatusForbidden, 0)
	h.SeedProtection("restricted-ruleset", http.StatusForbidden, 2)
	for _, c := range []struct {
		branch  string
		want    bool
		wantErr bool
	}{
		{"main", true, false},
		{"release/1", true, false},
		{"unscripted", false, false},
		{"dev", false, false},
		// A forbidden classic read with no ruleset is unknown, never "unprotected".
		{"restricted", false, true},
		{"restricted-ruleset", true, false},
	} {
		got, err := h.caps.BranchProtectionForge.BranchProtected(c.branch)
		if got != c.want || (err != nil) != c.wantErr {
			t.Errorf("BranchProtected(%q) = (%v, %v), want (%v, err=%v)", c.branch, got, err, c.want, c.wantErr)
		}
	}
}

func testProbe(t *testing.T, h *connectorHarness) {
	if got, err := h.cf.Probe(); err != nil || got != connectorRepo {
		t.Errorf("Probe = (%q, %v), want %q", got, err, connectorRepo)
	}
}

// With main moved past two PRs, one lands by a clean rebase merge, and Rebase
// runs in core's git against the clone URL the Connector named, so the other
// ends up on top of main. (The fixture can advance the base once.)
func testBaseMovedOn(t *testing.T, h *connectorHarness) {
	landing := h.SeedLandablePR("412")
	behind := h.SeedLandablePR("411")
	branch := forge.AgentBranchName(h.BranchPrefix(), "411")
	h.backend.repo.AdvanceBase()
	if h.backend.repo.Rebased(branch) {
		t.Fatal("branch already contains the advanced base")
	}

	if err := h.cf.Merge(landing); err != nil || !h.backend.repo.Landed("412") {
		t.Errorf("Merge onto an advanced base: err=%v landed=%v", err, h.backend.repo.Landed("412"))
	}
	if err := h.cf.Rebase(behind); err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	if !h.backend.repo.Rebased(branch) {
		t.Error("branch does not contain the base after Rebase")
	}
}
