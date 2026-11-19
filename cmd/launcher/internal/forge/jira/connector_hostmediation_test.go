package jira_test

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/connectortracker"
	"spindrift.dev/launcher/internal/dedupmarker"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
	"spindrift.dev/launcher/internal/forge/jira"
)

// connectorHostHarness drives the host-posted comment and issue-filer contracts
// through the compiled Connector. Each read-back goes through the
// Connector's tracker, which proves the stored form restores to
// what the caller posted.
type connectorHostHarness struct{ *connectorHarness }

var (
	_ forgetest.HostPostedCommentHarness = connectorHostHarness{}
	_ forgetest.IssueFilerHarness        = connectorHostHarness{}
)

func (h connectorHostHarness) Tracker() forge.IssueTracker           { return h.tr }
func (h connectorHostHarness) IssueFilerTracker() forge.IssueTracker { return h.tr }

func (h connectorHostHarness) SeedCommentTarget(failing bool) (num string) {
	if failing {
		return "PROJ-999" // never seeded: the fake 404s the comment POST
	}
	h.SeedIssue(forge.Issue{Number: "PROJ-1", Title: "comment target"})
	return "PROJ-1"
}

func (h connectorHostHarness) SeedIssueFilerTarget(failing bool) (title, body string, labels []string) {
	if failing {
		return failingIssueTitle, "body", nil
	}
	return "widget: filed issue", "Filed by the host-mediation contract.", []string{"bug"}
}

func (h connectorHostHarness) CommentPosted(num, body string) bool {
	cl, ok := forge.As[forge.CommentLister](h.tr)
	if !ok {
		return false
	}
	got, err := cl.Comments(num)
	if err != nil {
		return false
	}
	return slices.ContainsFunc(got, func(c forge.Comment) bool { return c.Body == body })
}

func (h connectorHostHarness) IssuePosted(title, body string, labels []string) bool {
	h.mu.Lock()
	var keys []string
	for _, key := range h.order {
		if h.issues[key].title == title {
			keys = append(keys, key)
		}
	}
	h.mu.Unlock()
	for _, key := range keys {
		iss, err := h.tr.Issue(key)
		if err != nil {
			continue
		}
		if iss.Body == body && slices.Equal(iss.Labels, labels) &&
			slices.Equal(dedupmarker.Parse(iss.Body), dedupmarker.Parse(body)) {
			return true
		}
	}
	return false
}

func hostLifecycle() connectortracker.Lifecycle {
	return connectortracker.Lifecycle{Labels: testLabels, IssueType: "Task"}
}

// The host-mediation and issue-filer contracts hold over both builds of the
// tracker; the production one has no fallback.
func TestConnector_HostPostedCommentContract(t *testing.T) {
	if os.Getenv(connectorWasmEnv) == "" {
		t.Skipf("%s unset: no compiled jira Connector to test", connectorWasmEnv)
	}
	for _, b := range connectorBuilds {
		t.Run(b.name, func(t *testing.T) {
			admitted := map[string]int{}
			for _, fl := range connectorFlavours {
				t.Run(fl.name, func(t *testing.T) {
					h := b.harness(t, fl.cloud, dispatchkind.TrackerWork, "", jira.LifecycleHybrid, "Task")
					forgetest.RunHostPostedCommentContract(t, connectorHostHarness{h})
					h.countAdmitted(t, admitted)
				})
			}
			if admitted["POST /rest/api/2/issue"] == 0 {
				t.Errorf("no comment write was admitted: admitted = %v", admitted)
			}
		})
	}
}

func TestConnector_IssueFilerContract(t *testing.T) {
	if os.Getenv(connectorWasmEnv) == "" {
		t.Skipf("%s unset: no compiled jira Connector to test", connectorWasmEnv)
	}
	for _, b := range connectorBuilds {
		t.Run(b.name, func(t *testing.T) {
			admitted := map[string]int{}
			for _, fl := range connectorFlavours {
				t.Run(fl.name, func(t *testing.T) {
					h := b.harness(t, fl.cloud, dispatchkind.TrackerWork, "", jira.LifecycleHybrid, "Task")
					forgetest.RunIssueFilerContract(t, connectorHostHarness{h})
					h.countAdmitted(t, admitted)
				})
			}
			if admitted["POST /rest/api/2/issue"] == 0 {
				t.Errorf("no issue-filing write was admitted: admitted = %v", admitted)
			}
		})
	}
}

func (h *connectorHarness) storedComments(num string) []jiraWireComment {
	return slices.Clone(h.issueRecord(num).comments)
}

// A research verdict's marker line is a comment property, not body text: the
// Connector stores the property, and the read restores the marker as the
// body's first line, which is what the Box greps for to dedup research.
func TestConnector_ResearchMarkerRoundTripsThroughTheProperty(t *testing.T) {
	if os.Getenv(connectorWasmEnv) == "" {
		t.Skipf("%s unset: no compiled jira Connector to test", connectorWasmEnv)
	}
	const verdict = "## Verdict\n\nstatus=recommend"
	body := forge.ResearchMarker + "\n" + verdict
	for _, fl := range connectorFlavours {
		t.Run(fl.name, func(t *testing.T) {
			h := newConnectorHarnessWith(t, fl.cloud, hostLifecycle())
			h.SeedIssue(forge.Issue{Number: "PROJ-1", Title: "t"})
			if err := h.tr.(forge.HostPostedCommenter).Comment("PROJ-1", body); err != nil {
				t.Fatalf("Comment: %v", err)
			}
			if err := h.tr.(forge.HostPostedCommenter).Comment("PROJ-1", "plain"); err != nil {
				t.Fatalf("Comment: %v", err)
			}

			stored := h.storedComments("PROJ-1")
			if len(stored) != 2 {
				t.Fatalf("stored %d comments, want 2", len(stored))
			}
			if want := "h2. Verdict\n\nstatus=recommend"; stored[0].Body != want {
				t.Errorf("stored body = %q, want %q: the marker must not ride in the body", stored[0].Body, want)
			}
			if len(stored[0].Properties) != 1 || stored[0].Properties[0].Key != "spindrift-research" || stored[0].Properties[0].Value != true {
				t.Errorf("stored properties = %+v, want one spindrift-research=true", stored[0].Properties)
			}
			if len(stored[1].Properties) != 0 {
				t.Errorf("a plain comment stored properties %+v, want none", stored[1].Properties)
			}

			cl, ok := forge.As[forge.CommentLister](h.tr)
			if !ok {
				t.Fatal("Connector tracker is not a forge.CommentLister")
			}
			got, err := cl.Comments("PROJ-1")
			if err != nil || len(got) != 2 {
				t.Fatalf("Comments = %v, %v", got, err)
			}
			if want := forge.ResearchMarker + "\nh2. Verdict\n\nstatus=recommend"; got[0].Body != want {
				t.Errorf("read body = %q, want the marker restored as first line: %q", got[0].Body, want)
			}
			if strings.Contains(got[1].Body, forge.ResearchMarker) {
				t.Errorf("plain comment read back with the marker: %q", got[1].Body)
			}
		})
	}
}

// A finding's dedup marker is an issue property, not description text: the
// Connector stores it apart, and the read restores it so the backlog dedup
// index finds the issue the Connector filed.
func TestConnector_FindingDedupFindsAConnectorFiledIssue(t *testing.T) {
	if os.Getenv(connectorWasmEnv) == "" {
		t.Skipf("%s unset: no compiled jira Connector to test", connectorWasmEnv)
	}
	const chore = "widget-audit"
	marker := dedupmarker.Build([]string{"chore=" + chore, "widget:leak"})
	body := "## Finding\n\nThe **widget** leaks.\n\n" + marker
	for _, fl := range connectorFlavours {
		t.Run(fl.name, func(t *testing.T) {
			h := newConnectorHarnessWith(t, fl.cloud, hostLifecycle())
			url, err := h.tr.(forge.HostPostedIssueFiler).PostIssue("widget leak", body, []string{"agent-review-finding"})
			if err != nil {
				t.Fatalf("PostIssue: %v", err)
			}
			if want := h.srv.URL + "/browse/PROJ-1"; url != want {
				t.Errorf("url = %q, want %q", url, want)
			}

			rec := h.issueRecord("PROJ-1")
			if want := "h2. Finding\n\nThe *widget* leaks."; rec.body != want {
				t.Errorf("stored description = %q, want %q: the marker must not ride in the body", rec.body, want)
			}
			var prop struct {
				Marker string `json:"marker"`
			}
			if err := json.Unmarshal(rec.props["spindrift.dedup"], &prop); err != nil || prop.Marker != marker {
				t.Errorf("props = %s, want spindrift.dedup holding %q (err %v)", rec.props["spindrift.dedup"], marker, err)
			}

			iss, err := h.tr.Issue("PROJ-1")
			if err != nil {
				t.Fatalf("Issue: %v", err)
			}
			if got, want := dedupmarker.Parse(iss.Body), dedupmarker.Parse(body); !slices.Equal(got, want) {
				t.Errorf("Issue marker terms = %v, want %v", got, want)
			}

			// settle.PriorFindings scans ListOpenIssues on a tracker without
			// forge.LabeledBacklogLister, as this one is; importing settle here
			// would pull its closure into the backend test's fileset.
			open, err := h.tr.ListOpenIssues()
			if err != nil {
				t.Fatalf("ListOpenIssues: %v", err)
			}
			if len(open) != 1 || open[0].Number != "PROJ-1" ||
				!slices.Equal(dedupmarker.Parse(open[0].Body), dedupmarker.Parse(marker)) {
				t.Errorf("ListOpenIssues = %+v, want the one open finding PROJ-1 carrying the marker's terms", open)
			}
		})
	}
}

// A body with no marker stores no property.
func TestConnector_PostIssueWithoutMarkerStoresNoProperty(t *testing.T) {
	if os.Getenv(connectorWasmEnv) == "" {
		t.Skipf("%s unset: no compiled jira Connector to test", connectorWasmEnv)
	}
	for _, fl := range connectorFlavours {
		t.Run(fl.name, func(t *testing.T) {
			h := newConnectorHarnessWith(t, fl.cloud, hostLifecycle())
			if _, err := h.tr.(forge.HostPostedIssueFiler).PostIssue("plain", "just text", nil); err != nil {
				t.Fatalf("PostIssue: %v", err)
			}
			if props := h.issueRecord("PROJ-1").props; len(props) != 0 {
				t.Errorf("props = %v, want none for a marker-less body", props)
			}
		})
	}
}

func TestConnector_PostIssueRejectedCreateIsAnError(t *testing.T) {
	if os.Getenv(connectorWasmEnv) == "" {
		t.Skipf("%s unset: no compiled jira Connector to test", connectorWasmEnv)
	}
	for _, fl := range connectorFlavours {
		t.Run(fl.name, func(t *testing.T) {
			h := newConnectorHarnessWith(t, fl.cloud, hostLifecycle())
			if _, err := h.tr.(forge.HostPostedIssueFiler).PostIssue(failingIssueTitle, "body", nil); err == nil {
				t.Fatal("PostIssue of a create Jira rejects with 400: got nil error")
			}
			h.mu.Lock()
			defer h.mu.Unlock()
			if len(h.issues) != 0 {
				t.Errorf("a rejected create left %d issues", len(h.issues))
			}
		})
	}
}
