package forgejo_test

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/dedupmarker"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgejo"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

// trackerHarness drives the tracker contracts through the Connector's tracker
// primitives, built by the production constructor under a forgejo row. It omits
// forgetest.StateSeeder, so state moves through TransitionState.
type trackerHarness struct {
	*connectorHarness
	tr forge.IssueTracker
}

var (
	_ forgetest.Harness                  = trackerHarness{}
	_ forgetest.NativeCapable            = trackerHarness{}
	_ forgetest.NativeFailureIsolatable  = trackerHarness{}
	_ forgetest.PriorityCapable          = trackerHarness{}
	_ forgetest.HostPostedCommentHarness = trackerHarness{}
	_ forgetest.CommentCapable           = trackerHarness{}
	_ forgetest.IssueFilerHarness        = trackerHarness{}
)

func newTrackerHarness(t *testing.T) trackerHarness {
	t.Helper()
	h := newConnectorHarness(t, true)
	useForgejoRow(t, h.srv.URL, func(*backendrows.Row) {})
	tr, err := buildTracker(h.srv)
	if err != nil {
		t.Fatal(err)
	}
	return trackerHarness{h, tr}
}

func (w trackerHarness) Tracker() forge.IssueTracker           { return w.tr }
func (w trackerHarness) IssueFilerTracker() forge.IssueTracker { return w.tr }
func (trackerHarness) IsolatesNativeFailure()                  {}
func (trackerHarness) IsPriorityCapable()                      {}

// SeedCommentTarget is a seeded issue, or one the forge has no record of.
func (w trackerHarness) SeedCommentTarget(failing bool) string {
	if failing {
		return "424242"
	}
	w.SeedIssue(forge.Issue{Number: "902", Title: "comment target"})
	return "902"
}

// CommentPosted reads the thread from the scripted backend: the contract is
// that the body reached the forge, whatever the Connector's read path serves.
func (w trackerHarness) CommentPosted(num, body string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	iss := w.trk.issues[atoi(num)]
	return iss != nil && slices.ContainsFunc(iss.comments, func(c fakeComment) bool { return c.body == body })
}

// SeedIssueFilerTarget fails on an empty title, which Forgejo refuses. The
// label is a repository label of the scripted forge.
func (w trackerHarness) SeedIssueFilerTarget(failing bool) (title, body string, labels []string) {
	if failing {
		return "", "body", nil
	}
	w.mu.Lock()
	w.defineLabel("agent-review-finding")
	w.mu.Unlock()
	marker := dedupmarker.Build([]string{"chore=widget-audit", "widget:leak"})
	return "widget leak", "## Finding\n\nThe widget leaks.\n\n" + marker, []string{"agent-review-finding"}
}

// IssuePosted looks for a filed issue by title and reads it back through the
// tracker; the marker is compared by its terms, as it round-trips only in the
// canonical layout.
func (w trackerHarness) IssuePosted(title, body string, labels []string) bool {
	for _, num := range w.numbersTitled(title) {
		iss, err := w.tr.Issue(num)
		if err == nil && iss.Body == body && slices.Equal(iss.Labels, labels) &&
			slices.Equal(dedupmarker.Parse(iss.Body), dedupmarker.Parse(body)) {
			return true
		}
	}
	return false
}

func (w trackerHarness) numbersTitled(title string) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var nums []string
	for n, i := range w.trk.issues {
		if i.title == title {
			nums = append(nums, strconv.Itoa(n))
		}
	}
	return nums
}

// testTrackerRoutes runs what the tracker contract does not reach: the label
// registry, the org labels, closing a merged issue, the research marker
// surviving a comment, and the finding's marker surviving filing.
func testTrackerRoutes(t *testing.T, w trackerHarness) {
	t.Run("LabelRegistry", func(t *testing.T) { testLabelRegistry(t, w) })
	t.Run("OrgLabelsResolveOnFiling", func(t *testing.T) { testOrgLabelsResolveOnFiling(t, w) })
	t.Run("UnreadableOrgFilesAndAppliesByName", func(t *testing.T) { testUnreadableOrgFilesAndAppliesByName(t, w) })
	t.Run("CloseMergedIssue", func(t *testing.T) { testCloseMergedIssue(t, w) })
	t.Run("ResearchMarker", func(t *testing.T) { testResearchMarkerSurvives(t, w) })
	t.Run("FindingDedup", func(t *testing.T) { testFindingDedupMarkerSurvivesFiling(t, w) })
}

// A registry longer than the server's page cap lists whole, a created label
// joins it, org labels join the list, and creating one that exists is an error
// core can tell from success.
func testLabelRegistry(t *testing.T, w trackerHarness) {
	before, err := w.tr.ListLabels()
	if err != nil {
		t.Fatalf("ListLabels: %v", err)
	}
	const seeded = 2*forgejoServerPageCap + 5
	w.mu.Lock()
	for i := range seeded {
		w.defineLabel("seeded-" + strconv.Itoa(i))
	}
	w.mu.Unlock()
	w.seedOrgLabels("org-wide")

	if err := w.tr.CreateLabel("fresh", "a new label", "ededed"); err != nil {
		t.Fatalf("CreateLabel: %v", err)
	}
	got, err := w.tr.ListLabels()
	if want := len(before) + seeded + 2; err != nil || len(got) != want || !slices.Contains(got, "fresh") ||
		!slices.Contains(got, "org-wide") || !slices.Contains(got, "seeded-"+strconv.Itoa(seeded-1)) {
		t.Fatalf("ListLabels = %d labels, %v; want the %d seeded, the created and the org one on top of %d", len(got), err, seeded, len(before))
	}
	if err := w.tr.CreateLabel("fresh", "", "ededed"); err == nil {
		t.Error("CreateLabel of an existing label = nil, want an error")
	}
}

// A label defined only on the owner's org files: the name resolves against the
// union of the repository's labels and the org's.
func testOrgLabelsResolveOnFiling(t *testing.T, w trackerHarness) {
	w.seedOrgLabels("org-triage")
	filer := w.tr.(forge.HostPostedIssueFiler)

	if _, err := filer.PostIssue("org labelled", "body", []string{"org-triage"}); err != nil {
		t.Fatalf("PostIssue with an org label: %v", err)
	}
	if !w.IssuePosted("org labelled", "body", []string{"org-triage"}) {
		t.Error("issue filed with the org label was not recorded carrying it")
	}
	if _, err := filer.PostIssue("undefined labelled", "body", []string{"nowhere-defined"}); err == nil {
		t.Error("PostIssue with a label defined nowhere = nil, want an error")
	}
	if got := w.numbersTitled("undefined labelled"); len(got) != 0 {
		t.Errorf("issues filed with an undefined label = %v, want none", got)
	}
}

// With the org lookup refused for lack of auth, a label defined nowhere visible
// may still be an org label, so the issue files and the name is applied.
func testUnreadableOrgFilesAndAppliesByName(t *testing.T, w trackerHarness) {
	w.mu.Lock()
	prior := w.trk.orgStatus
	w.mu.Unlock()
	t.Cleanup(func() { w.scriptOrgStatus(prior) })
	w.scriptOrgStatus(http.StatusForbidden)

	if _, err := w.tr.(forge.HostPostedIssueFiler).PostIssue("org unreadable", "body", []string{"hidden-org-label"}); err != nil {
		t.Fatalf("PostIssue with an unreadable org: %v", err)
	}
	if !w.IssuePosted("org unreadable", "body", []string{"hidden-org-label"}) {
		t.Error("issue filed past an unreadable org did not carry the label")
	}
}

// Closing a merged issue closes an open one and leaves a closed one alone.
func testCloseMergedIssue(t *testing.T, w trackerHarness) {
	closer, ok := forge.As[forge.MergeCloser](w.tr)
	if !ok {
		t.Fatal("tracker does not implement forge.MergeCloser")
	}
	w.SeedIssue(forge.Issue{Number: "931", Title: "merged"})
	w.SeedIssue(forge.Issue{Number: "932", Title: "already closed", State: forge.IssueClosed})

	for _, num := range []string{"931", "932"} {
		if err := closer.CloseMergedIssue(num); err != nil {
			t.Fatalf("CloseMergedIssue(%s): %v", num, err)
		}
		iss, err := w.tr.Issue(num)
		if err != nil || iss.State != forge.IssueClosed {
			t.Errorf("Issue(%s) = %+v, %v; want it closed", num, iss, err)
		}
	}
}

// A research verdict reaches the thread with the marker line first, which is
// what the Box greps for to dedup research.
func testResearchMarkerSurvives(t *testing.T, w trackerHarness) {
	w.SeedIssue(forge.Issue{Number: "911", Title: "research"})
	body := forge.ResearchMarker + "\n## Verdict\n\nstatus=recommend"
	if err := w.tr.(forge.HostPostedCommenter).Comment("911", body); err != nil {
		t.Fatalf("Comment: %v", err)
	}
	if !w.CommentPosted("911", body) {
		t.Errorf("thread = %+v, want the verdict with the marker as its first line", w.trk.issues[911].comments)
	}
}

// A finding's dedup marker survives filing: the filed issue reads back
// carrying the marker's terms, and the new issue's URL names it.
func testFindingDedupMarkerSurvivesFiling(t *testing.T, w trackerHarness) {
	title, body, labels := w.SeedIssueFilerTarget(false)
	title += " (dedup)"
	url, err := w.tr.(forge.HostPostedIssueFiler).PostIssue(title, body, labels)
	if err != nil {
		t.Fatalf("PostIssue: %v", err)
	}
	nums := w.numbersTitled(title)
	if len(nums) != 1 {
		t.Fatalf("filed issues titled %q = %v, want one", title, nums)
	}
	if want := w.web + "/owner/repo/issues/" + nums[0]; url != want {
		t.Errorf("url = %q, want %q", url, want)
	}
	got, err := w.tr.Issue(nums[0])
	if err != nil {
		t.Fatalf("Issue(%s): %v", nums[0], err)
	}
	if !slices.Equal(dedupmarker.Parse(got.Body), dedupmarker.Parse(body)) {
		t.Errorf("Issue(%s) = %+v, want the marker's terms", nums[0], got)
	}
}

const routeLabel = "orders-api"

// An Issue route reaches the Connector on every tracker read and filing, which
// narrows the labels filter to the route's issues; an unrouted tracker sends
// none and sees the whole scope.
func TestConnectorTracker_IssueRouteNarrowsReadsAndFilesWithItsLabel(t *testing.T) {
	w := newTrackerHarness(t)
	w.SeedIssue(forge.Issue{Number: "1", Title: "in route", Labels: []string{testLabels.Dispatchable, routeLabel, "finding"}})
	w.SeedIssue(forge.Issue{Number: "2", Title: "other route", Labels: []string{testLabels.Dispatchable, "finding"}})

	if n, err := countReady(w.tr); err != nil || n != 2 {
		t.Fatalf("unrouted CountReady = %d, %v; want the whole scope, 2", n, err)
	}
	if got, err := listLabeled(w.tr, "finding"); err != nil || len(got) != 2 {
		t.Fatalf("unrouted ListIssuesWithLabels = %v, %v; want 2 issues", got, err)
	}

	tr, err := forgejo.Strategy.NewIssueTracker(backendkit.Shared{
		RepoSlug: connectorRepo, Labels: testLabels, VerdictLabels: forge.ResearchVerdictLabels(),
		Route: forge.Route{{Kind: "label", Value: routeLabel}},
	}, mapLookup(map[string]string{forgejo.ForgejoBaseURLEnv: w.srv.URL, forgejo.ForgejoTokenEnv: connectorToken}))
	if err != nil {
		t.Fatal(err)
	}
	if n, err := countReady(tr); err != nil || n != 1 {
		t.Errorf("routed CountReady = %d, %v; want 1", n, err)
	}
	if got, err := tr.ListIssues(forge.Dispatchable); err != nil || len(got) != 1 || got[0].Number != "1" {
		t.Errorf("routed ListIssues = %v, %v; want issue 1", got, err)
	}
	if got, err := listLabeled(tr, "finding"); err != nil || len(got) != 1 || got[0].Number != "1" {
		t.Errorf("routed ListIssuesWithLabels = %v, %v; want issue 1", got, err)
	}

	url, err := tr.(forge.HostPostedIssueFiler).PostIssue("routed finding", "body", []string{"finding"})
	if err != nil {
		t.Fatalf("PostIssue: %v", err)
	}
	num := atoi(url[strings.LastIndex(url, "/")+1:])
	w.mu.Lock()
	filed := w.trk.issues[num]
	var labels []string
	if filed != nil {
		labels = slices.Clone(filed.labels)
	}
	w.mu.Unlock()
	for _, want := range []string{"finding", routeLabel} {
		if !slices.Contains(labels, want) {
			t.Errorf("filed labels = %v, want %q among them", labels, want)
		}
	}
}

func countReady(tr forge.IssueTracker) (int, error) {
	dc, ok := forge.As[forge.DemandCounter](tr)
	if !ok {
		return 0, errors.New("tracker is not a forge.DemandCounter")
	}
	return dc.CountReady(true)
}

func listLabeled(tr forge.IssueTracker, labels ...string) ([]forge.Issue, error) {
	l, ok := forge.As[forge.LabeledBacklogLister](tr)
	if !ok {
		return nil, errors.New("tracker is not a forge.LabeledBacklogLister")
	}
	return l.ListIssuesWithLabels(forge.IssueOpen, labels)
}
