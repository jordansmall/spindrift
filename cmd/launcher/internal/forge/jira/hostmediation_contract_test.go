package jira_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dedupmarker"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
	"spindrift.dev/launcher/internal/forge/jira"
)

var (
	_ forgetest.IssueFilerHarness        = (*jiraHarness)(nil)
	_ forgetest.HostPostedCommentHarness = (*jiraHarness)(nil)
)

func (h *jiraHarness) SeedCommentTarget(failing bool) (num string) {
	if failing {
		return "PROJ-999" // never seeded: the fake 404s the comment POST
	}
	h.SeedIssue(forge.Issue{Number: "PROJ-1", Title: "comment target"})
	return "PROJ-1"
}

// CommentPosted reads the thread back through the client, so it proves the
// wire markup round-trips to the body the caller posted.
func (h *jiraHarness) CommentPosted(num, body string) bool {
	cl, ok := h.Tracker().(forge.CommentLister)
	if !ok {
		return false
	}
	got, err := cl.Comments(num)
	if err != nil {
		return false
	}
	return slices.ContainsFunc(got, func(c forge.Comment) bool { return c.Body == body })
}

func (h *jiraHarness) IssueFilerTracker() forge.IssueTracker { return h.tr }

func (h *jiraHarness) SeedIssueFilerTarget(failing bool) (title, body string, labels []string) {
	if failing {
		return failingIssueTitle, "body", nil
	}
	return "widget: filed issue", "Filed by the host-mediation contract.", []string{"bug"}
}

// IssuePosted reads the filed issue back through the client, so it proves the
// stored form round-trips to what the caller filed rather than comparing the
// fake's private record.
func (h *jiraHarness) IssuePosted(title, body string, labels []string) bool {
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

func TestJiraClient_IssueFilerContract(t *testing.T) {
	forgetest.RunIssueFilerContract(t, newJiraHarness(t))
}

func TestJiraClient_IssueFilerContract_Cloud(t *testing.T) {
	forgetest.RunIssueFilerContract(t, newJiraCloudHarness(t))
}

func TestJiraClient_HostPostedCommentContract(t *testing.T) {
	forgetest.RunHostPostedCommentContract(t, newJiraHarness(t))
}

func TestJiraClient_HostPostedCommentContract_Cloud(t *testing.T) {
	forgetest.RunHostPostedCommentContract(t, newJiraCloudHarness(t))
}

func filerOf(t *testing.T, h *jiraHarness) forge.HostPostedIssueFiler {
	t.Helper()
	f, ok := h.Tracker().(forge.HostPostedIssueFiler)
	if !ok {
		t.Fatal("jira tracker does not implement forge.HostPostedIssueFiler")
	}
	return f
}

const findingMarker = "<!-- spindrift-dedup: widget: leak, widget: race -->"

func TestJiraClient_PostIssue_StoresWikiBodyAndMarkerProperty(t *testing.T) {
	h := newJiraHarness(t)
	body := "## Finding\n\nThe **widget** leaks.\n\n" + findingMarker

	url, err := filerOf(t, h).PostIssue("widget leak", body, []string{"agent-review-finding", "chore"})
	if err != nil {
		t.Fatalf("PostIssue: %v", err)
	}
	if want := h.srv.URL + "/browse/PROJ-1"; url != want {
		t.Errorf("url = %q, want %q", url, want)
	}
	rec := h.issues["PROJ-1"]
	if rec.title != "widget leak" {
		t.Errorf("summary = %q", rec.title)
	}
	if want := "h2. Finding\n\nThe *widget* leaks."; rec.body != want {
		t.Errorf("description = %q, want wiki markup %q without the marker", rec.body, want)
	}
	if want := []string{"agent-review-finding", "chore"}; !slices.Equal(rec.labels, want) {
		t.Errorf("labels = %v, want exactly %v", rec.labels, want)
	}
	raw, ok := rec.props["spindrift.dedup"]
	if !ok {
		t.Fatalf("no %q property stored; props = %v", "spindrift.dedup", rec.props)
	}
	var v struct {
		Marker string `json:"marker"`
	}
	if err := json.Unmarshal(raw, &v); err != nil || v.Marker != findingMarker {
		t.Errorf("property value = %s, want {\"marker\": %q}", raw, findingMarker)
	}
}

func TestJiraClient_PostIssue_NoMarkerStoresNoProperty(t *testing.T) {
	h := newJiraHarness(t)
	if _, err := filerOf(t, h).PostIssue("plain", "just text", nil); err != nil {
		t.Fatalf("PostIssue: %v", err)
	}
	if len(h.issues["PROJ-1"].props) != 0 {
		t.Errorf("props = %v, want none for a marker-less body", h.issues["PROJ-1"].props)
	}
}

func TestJiraClient_PostIssue_UsesConfiguredIssueType(t *testing.T) {
	h := newJiraHarness(t)
	for _, tc := range []struct {
		issueType string
		wantErr   bool
	}{
		{"", false}, // defaults to Task
		{"Bug", false},
		{"Epic", true},
	} {
		tr := jira.NewJiraClient(jira.JiraConfig{BaseURL: h.srv.URL, ProjectKey: "PROJ", Token: "tok", IssueType: tc.issueType})
		_, err := tr.(forge.HostPostedIssueFiler).PostIssue("t", "b", nil)
		if (err != nil) != tc.wantErr {
			t.Errorf("IssueType %q: err = %v, wantErr %v", tc.issueType, err, tc.wantErr)
		}
	}
}

func TestJiraClient_PostIssue_RejectedCreateIsAnError(t *testing.T) {
	h := newJiraHarness(t)
	if _, err := filerOf(t, h).PostIssue(failingIssueTitle, "body", nil); err == nil {
		t.Fatal("PostIssue of a create Jira rejects with 400: got nil error")
	}
	if len(h.issues) != 0 {
		t.Errorf("a rejected create left %d issues", len(h.issues))
	}
}

// The dedup index and the tuning hold read the marker out of the body, so
// every read path must put the stored marker back as the body's last line.
func TestJiraClient_MarkerRoundTripsThroughEveryRead(t *testing.T) {
	for name, newHarness := range map[string]func(*testing.T) *jiraHarness{
		"DC":    newJiraHarness,
		"Cloud": newJiraCloudHarness,
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			body := "Some finding text.\n\n" + findingMarker
			if _, err := filerOf(t, h).PostIssue("finding", body, []string{testLabels.Dispatchable}); err != nil {
				t.Fatalf("PostIssue: %v", err)
			}
			want := dedupmarker.Parse(body)

			check := func(read string, iss forge.Issue) {
				t.Helper()
				if got := dedupmarker.Parse(iss.Body); !slices.Equal(got, want) {
					t.Errorf("%s: marker terms = %v, want %v (body %q)", read, got, want, iss.Body)
				}
				if !strings.HasSuffix(iss.Body, findingMarker) {
					t.Errorf("%s: body %q does not end with the marker", read, iss.Body)
				}
			}

			iss, err := h.tr.Issue("PROJ-1")
			if err != nil {
				t.Fatalf("Issue: %v", err)
			}
			check("Issue", iss)

			open, err := h.tr.ListOpenIssues()
			if err != nil || len(open) != 1 {
				t.Fatalf("ListOpenIssues = %v, %v", open, err)
			}
			check("ListOpenIssues", open[0])

			lister, ok := h.tr.(forge.LabeledBacklogLister)
			if !ok {
				t.Fatal("jira tracker does not implement forge.LabeledBacklogLister")
			}
			labeled, err := lister.ListIssuesWithLabels(forge.IssueOpen, []string{testLabels.Dispatchable})
			if err != nil || len(labeled) != 1 {
				t.Fatalf("ListIssuesWithLabels = %v, %v", labeled, err)
			}
			check("ListIssuesWithLabels", labeled[0])
		})
	}
}
