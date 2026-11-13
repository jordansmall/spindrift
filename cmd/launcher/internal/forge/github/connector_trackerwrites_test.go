package github

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectortracker"
	"spindrift.dev/launcher/internal/dedupmarker"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
	"spindrift.dev/launcher/internal/tokenrefresh"
)

// trackerWriteLifecycle is the label-only lifecycle GitHub has: the Connector
// maps no status, so core swaps labels.
var trackerWriteLifecycle = connectortracker.Lifecycle{Labels: testLabels, VerdictLabels: forge.ResearchVerdictLabels()}

// trackerWriteHarness drives the tracker contracts through the Connector's write
// primitives. It forwards only what the contracts drive and omits
// forgetest.StateSeeder, so state moves through TransitionState; the native
// adapter behind connectortracker.New still fails every call, so a write that
// reaches it fails the run.
type trackerWriteHarness struct{ h *connectorHarness }

var (
	_ forgetest.Harness                  = trackerWriteHarness{}
	_ forgetest.NativeCapable            = trackerWriteHarness{}
	_ forgetest.NativeFailureIsolatable  = trackerWriteHarness{}
	_ forgetest.PriorityCapable          = trackerWriteHarness{}
	_ forgetest.HostPostedCommentHarness = trackerWriteHarness{}
	_ forgetest.IssueFilerHarness        = trackerWriteHarness{}
)

func newTrackerWriteHarness(t *testing.T) trackerWriteHarness {
	return trackerWriteHarness{newHarnessWith(t, false, connectorRepo, trackerWriteLifecycle, nil)}
}

func (w trackerWriteHarness) Tracker() forge.IssueTracker           { return w.h.tr }
func (w trackerWriteHarness) IssueFilerTracker() forge.IssueTracker { return w.h.tr }
func (w trackerWriteHarness) SeedIssue(i forge.Issue)               { w.h.SeedIssue(i) }
func (w trackerWriteHarness) FailNativeDeps(n string)               { w.h.FailNativeDeps(n) }
func (w trackerWriteHarness) SeedNativeDeps(n string, ids []string) {
	w.h.SeedNativeDeps(n, ids)
}
func (trackerWriteHarness) IsolatesNativeFailure() {}
func (trackerWriteHarness) IsPriorityCapable()     {}

// SeedCommentTarget is a seeded issue, or one GitHub has no record of.
func (w trackerWriteHarness) SeedCommentTarget(failing bool) string {
	if failing {
		return "901"
	}
	w.h.SeedIssue(forge.Issue{Number: "902", Title: "comment target"})
	return "902"
}

// CommentPosted reads the thread from the scripted backend: REST reports no
// minimized state, so the Connector serves no comment reads.
func (w trackerWriteHarness) CommentPosted(num, body string) bool {
	w.h.backend.mu.Lock()
	defer w.h.backend.mu.Unlock()
	i := w.h.backend.find(num)
	return i != nil && slices.Contains(i.comments, body)
}

// SeedIssueFilerTarget fails on an empty title, which GitHub refuses.
func (w trackerWriteHarness) SeedIssueFilerTarget(failing bool) (title, body string, labels []string) {
	if failing {
		return "", "body", nil
	}
	marker := dedupmarker.Build([]string{"chore=widget-audit", "widget:leak"})
	return "widget leak", "## Finding\n\nThe widget leaks.\n\n" + marker, []string{"agent-review-finding"}
}

// IssuePosted looks for a filed issue by title and reads it back through the
// tracker; the marker is compared by its terms, as it round-trips only in the
// canonical layout.
func (w trackerWriteHarness) IssuePosted(title, body string, labels []string) bool {
	for _, num := range w.numbersTitled(title) {
		iss, err := w.h.tr.Issue(num)
		if err == nil && iss.Body == body && slices.Equal(iss.Labels, labels) &&
			slices.Equal(dedupmarker.Parse(iss.Body), dedupmarker.Parse(body)) {
			return true
		}
	}
	return false
}

func (w trackerWriteHarness) numbersTitled(title string) []string {
	w.h.backend.mu.Lock()
	defer w.h.backend.mu.Unlock()
	var nums []string
	for n, i := range w.h.backend.issues {
		if i.title == title {
			nums = append(nums, strconv.Itoa(n))
		}
	}
	return nums
}

// testTrackerWrites runs the tracker contract through the Connector's write
// layers, then the label registry.
func testTrackerWrites(t *testing.T, w trackerWriteHarness) {
	forgetest.RunTrackerContract(t, w)
	t.Run("LabelRegistry", func(t *testing.T) { testLabelRegistry(t, w.h) })
	t.Run("ResearchMarker", func(t *testing.T) { testResearchMarkerSurvives(t, w) })
	t.Run("FindingDedup", func(t *testing.T) { testFindingDedupFindsFiledIssue(t, w) })
}

// A registry longer than one page lists whole, a created label joins it, and
// creating one that exists is an error core can tell from success.
func testLabelRegistry(t *testing.T, h *connectorHarness) {
	h.backend.mu.Lock()
	for i := range 230 {
		h.backend.labels = append(h.backend.labels, fmt.Sprintf("seeded-%03d", i))
	}
	h.backend.mu.Unlock()

	if err := h.tr.CreateLabel("fresh", "a new label", "ededed"); err != nil {
		t.Fatalf("CreateLabel: %v", err)
	}
	got, err := h.tr.ListLabels()
	if err != nil || len(got) != 231 || !slices.Contains(got, "fresh") || !slices.Contains(got, "seeded-229") {
		t.Fatalf("ListLabels = %d labels, %v; want the 230 seeded and the created one", len(got), err)
	}
	if err := h.tr.CreateLabel("fresh", "", ""); err == nil {
		t.Error("CreateLabel of an existing label = nil, want an error")
	}
}

// A research verdict reaches the thread with the marker line first, which is
// what the Box greps for to dedup research.
func testResearchMarkerSurvives(t *testing.T, w trackerWriteHarness) {
	w.h.SeedIssue(forge.Issue{Number: "911", Title: "research"})
	body := forge.ResearchMarker + "\n## Verdict\n\nstatus=recommend"
	if err := w.h.tr.(forge.HostPostedCommenter).Comment("911", body); err != nil {
		t.Fatalf("Comment: %v", err)
	}
	if !w.CommentPosted("911", body) {
		t.Errorf("thread = %q, want the verdict with the marker as its first line", w.h.backend.find("911").comments)
	}
}

// A finding's dedup marker survives filing: the backlog dedup index finds the
// issue the Connector filed, and the new issue's URL names it.
func testFindingDedupFindsFiledIssue(t *testing.T, w trackerWriteHarness) {
	title, body, labels := w.SeedIssueFilerTarget(false)
	title += " (dedup)"
	url, err := w.h.tr.(forge.HostPostedIssueFiler).PostIssue(title, body, labels)
	if err != nil {
		t.Fatalf("PostIssue: %v", err)
	}
	nums := w.numbersTitled(title)
	if len(nums) != 1 {
		t.Fatalf("filed issues titled %q = %v, want one", title, nums)
	}
	if want := w.h.backend.web + "/issues/" + nums[0]; url != want {
		t.Errorf("url = %q, want %q", url, want)
	}
	open, err := w.h.tr.ListOpenIssues()
	if err != nil {
		t.Fatalf("ListOpenIssues: %v", err)
	}
	i := slices.IndexFunc(open, func(iss forge.Issue) bool { return iss.Number == nums[0] })
	if i < 0 || !slices.Equal(dedupmarker.Parse(open[i].Body), dedupmarker.Parse(body)) {
		t.Errorf("ListOpenIssues = %+v, want the filed issue %s carrying the marker's terms", open, nums[0])
	}
}

// A tracker write to a repository the Route table does not declare is refused
// by the host before it is sent, with the refusal named and audited. A refusal
// ends the Connector (ErrUnusable), so each write runs on its own.
func TestConnector_UndeclaredTrackerWritesAreRefusedUnsentAndAudited(t *testing.T) {
	for _, c := range []struct {
		name string
		do   func(forge.IssueTracker) error
	}{
		{"transition", func(tr forge.IssueTracker) error { return tr.TransitionState("1", forge.Untriaged, forge.Dispatchable) }},
		{"comment", func(tr forge.IssueTracker) error { return tr.(forge.HostPostedCommenter).Comment("1", "note") }},
		{"file", func(tr forge.IssueTracker) error {
			_, err := tr.(forge.HostPostedIssueFiler).PostIssue("t", "b", nil)
			return err
		}},
		{"createLabel", func(tr forge.IssueTracker) error { return tr.CreateLabel("x", "", "") }},
		{"listLabels", func(tr forge.IssueTracker) error { _, err := tr.ListLabels(); return err }},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newComposedHarness(t, false)
			tr, err := connectortracker.New(h.conn, failingWrites{t}, trackerWriteLifecycle,
				connectortracker.Target{API: h.backend.URL, Repo: hostileRepo})
			if err != nil {
				t.Fatal(err)
			}
			h.audit.Reset()
			h.backend.mu.Lock()
			before := h.backend.hits
			h.backend.mu.Unlock()

			err = c.do(tr)
			if !errors.Is(err, connectorhost.ErrRouteRefused) {
				t.Fatalf("write = %v, want ErrRouteRefused", err)
			}
			if !strings.Contains(err.Error(), "/repos/"+hostileRepo+"/") {
				t.Errorf("write = %v, want it to name the undeclared path", err)
			}
			h.backend.mu.Lock()
			sent := h.backend.hits - before
			h.backend.mu.Unlock()
			if sent != 0 {
				t.Errorf("backend saw %d requests, want none", sent)
			}
			entries := h.auditEntries(t)
			if len(entries) != 1 || entries[0].Decision != connectorhost.DecisionRefused ||
				!strings.HasPrefix(entries[0].Path, "/repos/"+hostileRepo+"/") {
				t.Errorf("audit = %+v, want one refused entry for the undeclared path", entries)
			}
		})
	}
}

// A write made after the token file rotates carries the new token, with no
// restart: the Connector's credential is read per request.
func TestConnector_TrackerWriteCarriesARotatedToken(t *testing.T) {
	file := filepath.Join(t.TempDir(), "token")
	rotate := func(token string) {
		t.Helper()
		tmp := file + ".tmp"
		if err := os.WriteFile(tmp, []byte(token+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, file); err != nil {
			t.Fatal(err)
		}
	}
	rotate(connectorToken)
	token := tokenrefresh.Follow("", file)
	h := newHarnessWith(t, false, connectorRepo, trackerWriteLifecycle, func(r *http.Request) error {
		r.Header.Set("Authorization", "Bearer "+token())
		return nil
	})
	const rotated = "rotated-token"
	h.backend.mu.Lock()
	h.backend.tokens = map[string]bool{"Bearer " + rotated: true}
	h.backend.mu.Unlock()
	h.SeedIssue(forge.Issue{Number: "921", Title: "rotation"})
	w := trackerWriteHarness{h}
	hc := h.tr.(forge.HostPostedCommenter)

	h.backend.mu.Lock()
	start := len(h.backend.auths)
	h.backend.mu.Unlock()
	if err := hc.Comment("921", "before"); err != nil {
		t.Fatalf("Comment before rotation: %v", err)
	}
	rotate(rotated)
	if err := hc.Comment("921", "after"); err != nil {
		t.Fatalf("Comment after rotation: %v", err)
	}

	h.backend.mu.Lock()
	auths := slices.Clone(h.backend.auths[start:])
	h.backend.mu.Unlock()
	if want := []string{"Bearer " + connectorToken, "Bearer " + rotated}; !slices.Equal(auths, want) {
		t.Errorf("writes carried %q, want %q", auths, want)
	}
	if !w.CommentPosted("921", "before") || !w.CommentPosted("921", "after") {
		t.Error("a write across the rotation did not land")
	}
}

// A lifecycle label with a "/" in it moves through the real host: the claim
// removes agent/ready and adds agent-in-progress. Removal must ride a Route the
// host's canonical-path check admits; an escaped "/" in a REST DELETE path
// would be refused after the add had already landed.
func TestConnectorClaimRemovesSlashBearingLabel(t *testing.T) {
	lc := trackerWriteLifecycle
	lc.Labels.Dispatchable = "agent/ready"
	h := newHarnessWith(t, false, connectorRepo, lc, nil)
	h.SeedIssue(forge.Issue{Number: "930", Title: "slash label", Labels: []string{"agent/ready", "keep me"}})

	if err := h.tr.TransitionState("930", forge.Dispatchable, forge.InProgress); err != nil {
		t.Fatalf("TransitionState(Dispatchable, InProgress): %v", err)
	}
	iss, err := h.tr.Issue("930")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if slices.Contains(iss.Labels, "agent/ready") || !slices.Contains(iss.Labels, "agent-in-progress") || !slices.Contains(iss.Labels, "keep me") {
		t.Errorf("labels = %v, want agent/ready gone, agent-in-progress added, keep me kept", iss.Labels)
	}
}

// A label removal on an issue GitHub has no record of surfaces as an error,
// not as a silent success: GitHub's NOT_FOUND GraphQL error reaches the caller.
func TestConnector_TransitionOfMissingIssueIsAnError(t *testing.T) {
	w := newTrackerWriteHarness(t)
	if err := w.h.tr.TransitionState("424242", forge.Dispatchable, forge.InProgress); err == nil {
		t.Fatal("TransitionState of a missing issue = nil, want an error")
	}
}
